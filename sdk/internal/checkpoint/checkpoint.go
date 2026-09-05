// Package checkpoint guarda o extract bruto de uma execucao para que uma
// segunda tentativa da MESMA run nao precise tocar na origem de novo.
//
// O ativo protegido e a quota do fornecedor: um extract que gastou 4.803
// requisicoes e uma janela de 40 minutos nao pode ser refeito porque uma
// coluna do destino mudou de tipo.
//
// O deposito e um diretorio com partes numeradas e um manifesto:
//
//	{At}/{provider}/{entity}/{run_id}/parte-00000.ndjson
//	{At}/{provider}/{entity}/{run_id}/parte-00001.ndjson
//	{At}/{provider}/{entity}/{run_id}/_completo
//
// O `_completo` e escrito POR ULTIMO e e o que autoriza a retomada. Sem ele o
// deposito e um extract interrompido, e retomar de um extract interrompido
// carregaria metade dos dados em silencio -- que e o pior jeito de falhar.
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

	// bytesPorParte limita a memoria da escrita, nao o tamanho do extract: o
	// buffer e despejado ao cruzar isto, entao um extract de 40 GB passa por
	// aqui com 8 MB na mao.
	bytesPorParte = 8 << 20
)

// Numeros diz como os numeros do payload foram decodificados na origem, e e a
// unica coisa que faz a volta pelo NDJSON ser fiel.
//
// Um payload que veio de um decodificador com UseNumber carrega json.Number,
// cujo literal `19.0` sobrevive; sem isso a releitura devolveria float64(19) e
// o asText diria "19" onde a primeira tentativa disse "19.0". Duas tentativas
// da mesma run produziriam ingestion_id diferentes -- exatamente a garantia
// que o checkpoint existe para dar.
//
// Nao e declarado por quem configura: e OBSERVADO do proprio fluxo, porque
// PreserveNumbers e campo do driver e o SDK so ve a interface. Um campo que
// alguem tivesse de manter em sincronia com o driver seria um campo que um dia
// fica errado, e o erro sairia calado num id.
const (
	NumerosFloat   = "float"
	NumerosLiteral = "literal"
)

// Manifesto e o `_completo`.
type Manifesto struct {
	Versao    int      `json:"versao"`
	Registros int64    `json:"registros"`
	Partes    []string `json:"partes"`
	Numeros   string   `json:"numeros"`
	Pipeline  string   `json:"pipeline,omitempty"`
	Run       string   `json:"run,omitempty"`
	GravadoEm string   `json:"gravado_em"`
}

// Deposito e um diretorio de checkpoint, no disco ou num object store.
type Deposito struct {
	caminho string // como foi configurado, para a mensagem
	bucket  string
	prefixo string // termina em "/"
	esquema string
	store   core.Store // nunca nil: local vira discoLocal
}

// Novo abre o deposito em caminho. O store segue a regra dos drivers: nil e o
// disco local, e um esquema que nao casa com o store e erro que nomeia os dois.
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

// Caminho e o deposito inteiro, do jeito que se cola num navegador.
func (d *Deposito) Caminho() string {
	if d.esquema == "" {
		return d.prefixo
	}
	return d.esquema + "://" + d.bucket + "/" + d.prefixo
}

func (d *Deposito) chave(nome string) string { return d.prefixo + nome }

// Reservar prova que da para gravar ANTES de a extracao comecar.
//
// Sem isto a falha mais comum -- credencial sem permissao no bucket -- so
// apareceria depois de a primeira parte encher, ou seja, depois de ja ter
// gastado parte da quota que o checkpoint existe para poupar.
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

// Manifesto le o `_completo`. Ausente ou ilegivel devolve erro: quem chama
// trata os dois do mesmo jeito -- refazer o extract -- e a mensagem entra no
// log para nao virar um "refiz e nao disse por que".
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

// Conferir recusa um deposito que nao esta inteiro, ANTES de a carga comecar.
//
// Confere o conjunto de partes, e isso basta porque toda parte e escrita de uma
// vez so -- um PUT unico no object store, um rename no disco. Uma parte que
// existe e uma parte inteira; o que pode faltar e a parte, nao um pedaco dela.
//
// A contagem de registros e conferida na releitura (ver Reler), porque nao ha
// como saber quantas linhas tem um objeto sem le-lo -- e ler tudo aqui seria
// ler o extract duas vezes.
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

// Reler devolve os registros na ordem em que a extracao os produziu.
//
// A ordem sai do manifesto, e nao do List: o manifesto e quem sabe a ordem
// original, e uma Key posicional muda o ingestion_id se a sequencia mudar.
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
		// A contagem so fecha aqui, e uma divergencia significa objeto
		// adulterado depois de escrito. Gritar e o certo: seguir calado
		// carregaria menos linhas do que a primeira tentativa carregou.
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

// Escrever comeca uma escrita no deposito.
func (d *Deposito) Escrever() *Escrita {
	return &Escrita{d: d, numeros: NumerosFloat}
}

// Add guarda um registro no buffer. So falha se o payload nao serializar, e
// nesse caso o registro NAO entrou -- a distincao importa para quem degrada,
// que precisa saber se ainda tem de ceder este registro.
func (e *Escrita) Add(env core.Envelope) error {
	data, err := json.Marshal(env.Payload)
	if err != nil {
		return fmt.Errorf("registro %d do checkpoint: %w", e.gravados+e.noBuffer, err)
	}

	// Uma vez descoberto, nunca mais: o decodificador e fixo por origem, entao
	// o primeiro numero que aparecer decide o modo do fluxo inteiro. Ate la a
	// busca para no primeiro numero encontrado, e um payload sem numero nenhum
	// nao custa nada porque nao ha o que preservar.
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

// Cheio diz que ja da para despejar uma parte.
func (e *Escrita) Cheio() bool { return e.buf.Len() >= bytesPorParte }

// Despejar grava o buffer como uma parte. Se falhar, o buffer fica INTACTO:
// os registros continuam pendentes, e quem degrada os cede de Pendentes.
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

// Fechar despeja o que sobrou e escreve o manifesto POR ULTIMO. E o manifesto
// que transforma um diretorio de partes num checkpoint retomavel.
func (e *Escrita) Fechar(ctx context.Context, pipeline, run string) error {
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

// Gravadas descreve o que ja virou parte, para ser relido quando a escrita
// falhou no meio. A contagem e exata, entao a conferencia de Reler continua
// valendo neste caminho.
func (e *Escrita) Gravadas() *Manifesto {
	return &Manifesto{
		Versao: versaoManifesto, Registros: e.gravados,
		Partes: e.partes, Numeros: e.numeros,
	}
}

// Pendentes sao os registros que estao no buffer e nunca chegaram a um objeto.
//
// Decodifica de volta em vez de guardar uma segunda copia: assim a execucao
// normal nao paga memoria nenhuma por um caminho que so roda quando o bucket
// falha no meio.
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

// formaDoNumero procura o primeiro valor numerico do payload e diz se ele veio
// como literal (json.Number) ou como float64.
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
