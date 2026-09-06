// Package checkpoint keeps a run's raw extract so a second attempt of the SAME
// run does not have to touch the source again.
//
// The asset it protects is the vendor's quota: an extract that spent 4,803
// requests and a forty-minute window must not be redone because a column at the
// destination changed type.
//
// The depot is a directory of numbered parts plus a manifest:
//
//	{At}/{run_id}/{pipeline}/parte-00000.ndjson
//	{At}/{run_id}/{pipeline}/parte-00001.ndjson
//	{At}/{run_id}/{pipeline}/_completo
//
// `_completo` is written LAST, and it is what authorises a resume. Without it
// the depot is an interrupted extract, and resuming from an interrupted extract
// would load half the data in silence -- the worst way to fail.
package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"path"
	"strings"
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

const (
	arquivoManifesto = "_completo"
	arquivoInicio    = "_inicio"
	versaoManifesto  = 1

	// bytesPorParte bounds the writer's memory, not the extract's size: the
	// buffer is flushed on crossing it, so a 40 GB extract goes through here
	// holding 8 MB.
	bytesPorParte = 8 << 20
)

// Numeros says how the payload's numbers were decoded at the source, and it is
// the one thing that makes the round trip through NDJSON faithful.
//
// A payload that came from a decoder with UseNumber carries json.Number, whose
// literal `19.0` survives; without it the re-read would return float64(19) and
// asText would say "19" where the first attempt said "19.0". Two attempts of the
// same run would produce different ingestion_ids -- exactly the guarantee the
// checkpoint exists to give.
//
// It is not declared by whoever configures: it is OBSERVED from the stream
// itself, because PreserveNumbers is a driver field and the SDK only sees the
// interface. A field somebody had to keep in sync with the driver would be a
// field that one day goes wrong, and the error would come out silently inside
// an id.
const (
	NumerosFloat   = "float"
	NumerosLiteral = "literal"
)

// Manifesto is the `_completo` object.
type Manifesto struct {
	Versao    int      `json:"versao"`
	Registros int64    `json:"registros"`
	Partes    []string `json:"partes"`
	Numeros   string   `json:"numeros"`
	Pipeline  string   `json:"pipeline,omitempty"`
	Run       string   `json:"run,omitempty"`
	GravadoEm string   `json:"gravado_em"`
}

// Deposito is a checkpoint directory, on disk or in an object store.
type Deposito struct {
	caminho string // como foi configurado, para a mensagem
	bucket  string
	prefixo string // termina em "/"
	esquema string
	store   core.Store // nunca nil: local vira discoLocal
}

// Novo opens the depot at a path. The store follows the drivers' rule: nil is
// the local filesystem, and a scheme that does not match the store is an error
// naming both.
func Novo(caminho string, store core.Store) (*Deposito, error) {
	if caminho == "" {
		return nil, fmt.Errorf("checkpoint sem caminho")
	}
	loc, err := core.ParseLocation(comoDiretorio(caminho))
	if err != nil {
		return nil, fmt.Errorf("checkpoint %q: %w", caminho, err)
	}
	switch {
	case loc.Scheme == "" && store != nil:
		return nil, fmt.Errorf("o checkpoint %q e um caminho local, mas recebeu um Store %s; "+
			"tire o Store, ou aponte At para %s://", caminho, store.Scheme(), store.Scheme())
	case loc.Scheme != "" && store == nil:
		return nil, fmt.Errorf("o checkpoint %q precisa de um Store %s; passe um, "+
			"por exemplo Store: %s.New(...)", caminho, loc.Scheme, loc.Scheme)
	case loc.Scheme != "" && store.Scheme() != loc.Scheme:
		return nil, fmt.Errorf("o checkpoint %q e %s, mas o Store atende %s",
			caminho, loc.Scheme, store.Scheme())
	}
	if loc.Scheme == "" {
		store = discoLocal{}
	}
	return &Deposito{
		caminho: caminho, bucket: loc.Bucket, prefixo: loc.Prefix,
		esquema: loc.Scheme, store: store,
	}, nil
}

// Caminho is the whole depot, in the form you paste into a browser.
func (d *Deposito) Caminho() string {
	if d.esquema == "" {
		return d.prefixo
	}
	return d.esquema + "://" + d.bucket + "/" + d.prefixo
}

func (d *Deposito) chave(nome string) string { return d.prefixo + nome }

// Reservar proves writing works BEFORE the extraction starts.
//
// Without it the most common failure -- a credential without permission on the
// bucket -- would only surface once the first part filled up, that is, after
// having already spent part of the quota the checkpoint exists to save.
func (d *Deposito) Reservar(ctx context.Context, pipeline, run string) error {
	marca, err := json.Marshal(map[string]string{
		"pipeline": pipeline, "run": run,
		"iniciado_em": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return d.store.Create(ctx, d.bucket, d.chave(arquivoInicio), bytes.NewReader(marca))
}

// Manifesto reads `_completo`. Missing or unreadable returns an error: the
// caller treats both the same way -- redo the extract -- and the message goes to
// the log so it does not become a "redone, and never said why".
func (d *Deposito) Manifesto(ctx context.Context) (*Manifesto, error) {
	r, err := d.store.Open(ctx, d.bucket, d.chave(arquivoManifesto))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()

	var m Manifesto
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("manifesto ilegivel: %w", err)
	}
	if m.Versao != versaoManifesto {
		return nil, fmt.Errorf("manifesto na versao %d, esta build le a %d", m.Versao, versaoManifesto)
	}
	return &m, nil
}

// Conferir refuses a depot that is not whole, BEFORE the load starts.
//
// It checks the SET of parts, and that is enough because every part is written
// in one go -- a single PUT in object storage, a rename on disk. A part that
// exists is a whole part; what can be missing is the part, not a piece of it.
//
// The record count is checked on the re-read (see Reler), because there is no
// way to know how many lines an object holds without reading it -- and reading
// everything here would read the extract twice.
func (d *Deposito) Conferir(ctx context.Context, m *Manifesto) error {
	if len(m.Partes) == 0 {
		if m.Registros == 0 {
			return nil // extract vazio, e isso e legitimo
		}
		return fmt.Errorf("o manifesto diz %d registros e nao lista nenhuma parte", m.Registros)
	}

	chaves, err := d.store.List(ctx, d.bucket, d.prefixo)
	if err != nil {
		return fmt.Errorf("listando o checkpoint: %w", err)
	}
	presentes := make(map[string]bool, len(chaves))
	for _, k := range chaves {
		presentes[path.Base(k)] = true
	}
	for _, p := range m.Partes {
		if !presentes[p] {
			return fmt.Errorf("falta a parte %q das %d que o manifesto lista", p, len(m.Partes))
		}
	}
	return nil
}

// Reler yields the records in the order the extraction produced them.
//
// The order comes from the manifest, not from List: the manifest is what knows
// the original order, and a positional Key changes the ingestion_id if the
// sequence changes.
func (d *Deposito) Reler(ctx context.Context, m *Manifesto) iter.Seq2[core.Envelope, error] {
	return func(yield func(core.Envelope, error) bool) {
		var lidos int64
		for _, parte := range m.Partes {
			ok, err := d.relerParte(ctx, parte, m.Numeros, &lidos, yield)
			if err != nil {
				yield(core.Envelope{}, err)
				return
			}
			if !ok {
				return // quem consome desistiu
			}
		}
		// The count only closes here, and a divergence means an object was
		// tampered with after it was written. Shouting is right: carrying on
		// quietly would load fewer rows than the first attempt loaded.
		if lidos != m.Registros {
			yield(core.Envelope{}, fmt.Errorf(
				"checkpoint corrompido em %s: o manifesto diz %d registros e as partes tem %d",
				d.Caminho(), m.Registros, lidos))
		}
	}
}

func (d *Deposito) relerParte(ctx context.Context, parte, numeros string, lidos *int64,
	yield func(core.Envelope, error) bool) (bool, error) {

	r, err := d.store.Open(ctx, d.bucket, d.chave(parte))
	if err != nil {
		return false, fmt.Errorf("lendo a parte %q do checkpoint: %w", parte, err)
	}
	defer func() { _ = r.Close() }()

	dec := json.NewDecoder(r)
	if numeros == NumerosLiteral {
		dec.UseNumber()
	}
	for dec.More() {
		var payload any
		if err := dec.Decode(&payload); err != nil {
			return false, fmt.Errorf("parte %q, registro %d: %w", parte, *lidos, err)
		}
		*lidos++
		if !yield(core.Envelope{Payload: payload}, nil) {
			return false, nil
		}
	}
	return true, nil
}

// Escrita acumula o extract e o despeja em partes.
type Escrita struct {
	d        *Deposito
	buf      bytes.Buffer
	noBuffer int64 // registros no buffer, ainda nao gravados
	gravados int64 // registros que ja viraram parte

	partes []string

	numeros    string
	sabeNumero bool
}

// Escrever starts a write into the depot.
func (d *Deposito) Escrever() *Escrita {
	return &Escrita{d: d, numeros: NumerosFloat}
}

// Add puts a record in the buffer. It only fails when the payload does not
// serialise, and then the record did NOT go in -- the distinction matters to
// whoever degrades, who needs to know whether this record still has to be
// yielded.
func (e *Escrita) Add(env core.Envelope) error {
	data, err := json.Marshal(env.Payload)
	if err != nil {
		return fmt.Errorf("registro %d do checkpoint: %w", e.gravados+e.noBuffer, err)
	}

	// Once discovered, never again: the decoder is fixed per source, so the
	// first number that turns up decides the mode for the whole stream. Until
	// then the search stops at the first number found, and a payload with no
	// numbers at all costs nothing because there is nothing to preserve.
	if !e.sabeNumero {
		if achou, literal := formaDoNumero(env.Payload); achou {
			e.sabeNumero = true
			if literal {
				e.numeros = NumerosLiteral
			}
		}
	}

	e.buf.Write(data)
	e.buf.WriteByte('\n')
	e.noBuffer++
	return nil
}

// Cheio says there is enough to flush a part.
func (e *Escrita) Cheio() bool { return e.buf.Len() >= bytesPorParte }

// Despejar writes the buffer as one part. On failure the buffer is left INTACT:
// the records stay pending, and whoever degrades yields them from Pendentes.
func (e *Escrita) Despejar(ctx context.Context) error {
	if e.buf.Len() == 0 {
		return nil
	}
	nome := fmt.Sprintf("parte-%05d.ndjson", len(e.partes))
	if err := e.d.store.Create(ctx, e.d.bucket, e.d.chave(nome), bytes.NewReader(e.buf.Bytes())); err != nil {
		return fmt.Errorf("gravando %s no checkpoint: %w", nome, err)
	}
	e.partes = append(e.partes, nome)
	e.gravados += e.noBuffer
	e.noBuffer = 0
	e.buf.Reset()
	return nil
}

// Finish flushes what is left and writes the manifest LAST. It is the manifest
// that turns a directory of parts into a resumable checkpoint.
func (e *Escrita) Finish(ctx context.Context, pipeline, run string) error {
	if err := e.Despejar(ctx); err != nil {
		return err
	}
	m := Manifesto{
		Versao: versaoManifesto, Registros: e.gravados, Partes: e.partes,
		Numeros: e.numeros, Pipeline: pipeline, Run: run,
		GravadoEm: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := e.d.store.Create(ctx, e.d.bucket, e.d.chave(arquivoManifesto), bytes.NewReader(data)); err != nil {
		return fmt.Errorf("gravando o manifesto do checkpoint: %w", err)
	}
	return nil
}

// Gravadas describes what already became a part, to be re-read when the write
// failed midway. The count is exact, so Reler's check still holds on this
// path.
func (e *Escrita) Gravadas() *Manifesto {
	return &Manifesto{
		Versao: versaoManifesto, Registros: e.gravados,
		Partes: e.partes, Numeros: e.numeros,
	}
}

// Pendentes are the records sitting in the buffer that never reached an object.
//
// It decodes them back rather than keeping a second copy: that way the normal
// run pays no memory at all for a path that only executes when the bucket fails
// midway.
func (e *Escrita) Pendentes() iter.Seq2[core.Envelope, error] {
	dados := e.buf.Bytes()
	numeros := e.numeros
	return func(yield func(core.Envelope, error) bool) {
		dec := json.NewDecoder(bytes.NewReader(dados))
		if numeros == NumerosLiteral {
			dec.UseNumber()
		}
		for dec.More() {
			var payload any
			if err := dec.Decode(&payload); err != nil {
				yield(core.Envelope{}, fmt.Errorf("relendo o buffer do checkpoint: %w", err))
				return
			}
			if !yield(core.Envelope{Payload: payload}, nil) {
				return
			}
		}
	}
}

// formaDoNumero looks for the payload's first numeric value and says whether it
// arrived as a literal (json.Number) or as a float64.
func formaDoNumero(v any) (achou, literal bool) {
	switch t := v.(type) {
	case json.Number:
		return true, true
	case float64, int, int64, float32:
		return true, false
	case map[string]any:
		for _, sub := range t {
			if achou, literal = formaDoNumero(sub); achou {
				return true, literal
			}
		}
	case []any:
		for _, sub := range t {
			if achou, literal = formaDoNumero(sub); achou {
				return true, literal
			}
		}
	}
	return false, false
}

func comoDiretorio(p string) string {
	if p == "" || strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}
