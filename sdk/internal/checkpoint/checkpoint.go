// Package checkpoint keeps a run's raw extract so a second attempt of the SAME
// run does not have to touch the source again.
//
// The asset it protects is the vendor's quota: an extract that spent 4,803
// requests and a forty-minute window must not be redone because a column at the
// destination changed type.
//
// The depot is a directory of numbered parts plus a manifest:
//
//	{At}/{run_id}/{pipeline}/part-00000.ndjson
//	{At}/{run_id}/{pipeline}/part-00001.ndjson
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

// The names below are the ON-DISK FORMAT, and they stay in Portuguese on
// purpose while the code around them is English.
//
// They are data, not prose. A depot written by a released SDK holds
// `_completo` and `part-00000.ndjson`; a build looking for `_complete` finds
// nothing, logs "no usable checkpoint" and re-extracts -- which spends exactly
// the vendor quota this package exists to save. Renaming them is a migration,
// and it is not one worth doing for a word.
const (
	manifestFile    = "_completo"
	startFile       = "_inicio"
	partPattern     = "parte-%05d.ndjson"
	manifestVersion = 1

	// bytesPerPart bounds the writer's memory, not the extract's size: the
	// buffer is flushed on crossing it, so a 40 GB extract goes through here
	// holding 8 MB.
	bytesPerPart = 8 << 20
)

// The Numbers constants say how the payload's numbers were decoded at the source, and it is
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
	NumbersFloat   = "float"
	NumbersLiteral = "literal"
)

// Manifest is the `_completo` object.
//
// The json tags are the on-disk format and stay as they were written; see the
// note on manifestFile above.
type Manifest struct {
	Version   int      `json:"versao"`
	Records   int64    `json:"registros"`
	Parts     []string `json:"partes"`
	Numbers   string   `json:"numeros"`
	Pipeline  string   `json:"pipeline,omitempty"`
	Run       string   `json:"run,omitempty"`
	WrittenAt string   `json:"gravado_em"`
}

// Depot is a checkpoint directory, on disk or in an object store.
type Depot struct {
	path   string // as it was configured, for the message
	bucket string
	prefix string // ends in "/"
	scheme string
	store  core.Store // never nil: local becomes localDisk
}

// New opens the depot at a path. The store follows the drivers' rule: nil is
// the local filesystem, and a scheme that does not match the store is an error
// naming both.
func New(path string, store core.Store) (*Depot, error) {
	if path == "" {
		return nil, fmt.Errorf("checkpoint with no path")
	}
	loc, err := core.ParseLocation(asDirectory(path))
	if err != nil {
		return nil, fmt.Errorf("checkpoint %q: %w", path, err)
	}
	switch {
	case loc.Scheme == "" && store != nil:
		return nil, fmt.Errorf("checkpoint %q is a local path but got a %s Store; "+
			"drop the Store, or point At at %s://", path, store.Scheme(), store.Scheme())
	case loc.Scheme != "" && store == nil:
		return nil, fmt.Errorf("checkpoint %q needs a %s Store; pass one, "+
			"for example Store: %s.New(...)", path, loc.Scheme, loc.Scheme)
	case loc.Scheme != "" && store.Scheme() != loc.Scheme:
		return nil, fmt.Errorf("checkpoint %q is %s, but the Store serves %s",
			path, loc.Scheme, store.Scheme())
	}
	if loc.Scheme == "" {
		store = localDisk{}
	}
	return &Depot{
		path: path, bucket: loc.Bucket, prefix: loc.Prefix,
		scheme: loc.Scheme, store: store,
	}, nil
}

// Caminho is the whole depot, in the form you paste into a browser.
func (d *Depot) Path() string {
	if d.scheme == "" {
		return d.prefix
	}
	return d.scheme + "://" + d.bucket + "/" + d.prefix
}

func (d *Depot) key(name string) string { return d.prefix + name }

// Reserve proves writing works BEFORE the extraction starts.
//
// Without it the most common failure -- a credential without permission on the
// bucket -- would only surface once the first part filled up, that is, after
// having already spent part of the quota the checkpoint exists to save.
func (d *Depot) Reserve(ctx context.Context, pipeline, run string) error {
	marca, err := json.Marshal(map[string]string{
		"pipeline": pipeline, "run": run,
		"iniciado_em": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return d.store.Create(ctx, d.bucket, d.key(startFile), bytes.NewReader(marca))
}

// Manifest reads `_completo`. Missing or unreadable returns an error: the
// caller treats both the same way -- redo the extract -- and the message goes to
// the log so it does not become a "redone, and never said why".
func (d *Depot) Manifest(ctx context.Context) (*Manifest, error) {
	r, err := d.store.Open(ctx, d.bucket, d.key(manifestFile))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()

	var m Manifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("unreadable manifest: %w", err)
	}
	if m.Version != manifestVersion {
		return nil, fmt.Errorf("manifest at version %d, this build reads %d", m.Version, manifestVersion)
	}
	return &m, nil
}

// Check refuses a depot that is not whole, BEFORE the load starts.
//
// It checks the SET of parts, and that is enough because every part is written
// in one go -- a single PUT in object storage, a rename on disk. A part that
// exists is a whole part; what can be missing is the part, not a piece of it.
//
// The record count is checked on the re-read (see Reread), because there is no
// way to know how many lines an object holds without reading it -- and reading
// everything here would read the extract twice.
func (d *Depot) Check(ctx context.Context, m *Manifest) error {
	if len(m.Parts) == 0 {
		if m.Records == 0 {
			return nil // an empty extract, and that is legitimate
		}
		return fmt.Errorf("the manifest says %d records and lists no part", m.Records)
	}

	keys, err := d.store.List(ctx, d.bucket, d.prefix)
	if err != nil {
		return fmt.Errorf("listing the checkpoint: %w", err)
	}
	present := make(map[string]bool, len(keys))
	for _, k := range keys {
		present[path.Base(k)] = true
	}
	for _, p := range m.Parts {
		if !present[p] {
			return fmt.Errorf("part %q is missing, of the %d the manifest lists", p, len(m.Parts))
		}
	}
	return nil
}

// Reread yields the records in the order the extraction produced them.
//
// The order comes from the manifest, not from List: the manifest is what knows
// the original order, and a positional Key changes the ingestion_id if the
// sequence changes.
func (d *Depot) Reread(ctx context.Context, m *Manifest) iter.Seq2[core.Envelope, error] {
	return func(yield func(core.Envelope, error) bool) {
		var read int64
		for _, part := range m.Parts {
			ok, err := d.rereadPart(ctx, part, m.Numbers, &read, yield)
			if err != nil {
				yield(core.Envelope{}, err)
				return
			}
			if !ok {
				return // the consumer gave up
			}
		}
		// The count only closes here, and a divergence means an object was
		// tampered with after it was written. Shouting is right: carrying on
		// quietly would load fewer rows than the first attempt loaded.
		if read != m.Records {
			yield(core.Envelope{}, fmt.Errorf(
				"checkpoint corrupt at %s: the manifest says %d records and the parts hold %d",
				d.Path(), m.Records, read))
		}
	}
}

func (d *Depot) rereadPart(ctx context.Context, part, numbers string, read *int64,
	yield func(core.Envelope, error) bool) (bool, error) {

	r, err := d.store.Open(ctx, d.bucket, d.key(part))
	if err != nil {
		return false, fmt.Errorf("reading part %q of the checkpoint: %w", part, err)
	}
	defer func() { _ = r.Close() }()

	dec := json.NewDecoder(r)
	if numbers == NumbersLiteral {
		dec.UseNumber()
	}
	for dec.More() {
		var payload any
		if err := dec.Decode(&payload); err != nil {
			return false, fmt.Errorf("part %q, record %d: %w", part, *read, err)
		}
		*read++
		if !yield(core.Envelope{Payload: payload}, nil) {
			return false, nil
		}
	}
	return true, nil
}

// Write acumula o extract e o despeja em parts.
type Write struct {
	d        *Depot
	buf      bytes.Buffer
	buffered int64 // records in the buffer, not yet written
	written  int64 // records that already became a part

	parts []string

	numbers    string
	sabeNumero bool
}

// Writer starts a write into the depot.
func (d *Depot) Writer() *Write {
	return &Write{d: d, numbers: NumbersFloat}
}

// Add puts a record in the buffer. It only fails when the payload does not
// serialise, and then the record did NOT go in -- the distinction matters to
// whoever degrades, who needs to know whether this record still has to be
// yielded.
func (e *Write) Add(env core.Envelope) error {
	data, err := json.Marshal(env.Payload)
	if err != nil {
		return fmt.Errorf("record %d do checkpoint: %w", e.written+e.buffered, err)
	}

	// Once discovered, never again: the decoder is fixed per source, so the
	// first number that turns up decides the mode for the whole stream. Until
	// then the search stops at the first number found, and a payload with no
	// numbers at all costs nothing because there is nothing to preserve.
	if !e.sabeNumero {
		if achou, literal := formaDoNumero(env.Payload); achou {
			e.sabeNumero = true
			if literal {
				e.numbers = NumbersLiteral
			}
		}
	}

	e.buf.Write(data)
	e.buf.WriteByte('\n')
	e.buffered++
	return nil
}

// Full says there is enough to flush a part.
func (e *Write) Full() bool { return e.buf.Len() >= bytesPerPart }

// Flush writes the buffer as one part. On failure the buffer is left INTACT:
// the records stay pending, and whoever degrades yields them from Pending.
func (e *Write) Flush(ctx context.Context) error {
	if e.buf.Len() == 0 {
		return nil
	}
	name := fmt.Sprintf(partPattern, len(e.parts))
	if err := e.d.store.Create(ctx, e.d.bucket, e.d.key(name), bytes.NewReader(e.buf.Bytes())); err != nil {
		return fmt.Errorf("writing %s into the checkpoint: %w", name, err)
	}
	e.parts = append(e.parts, name)
	e.written += e.buffered
	e.buffered = 0
	e.buf.Reset()
	return nil
}

// Finish flushes what is left and writes the manifest LAST. It is the manifest
// that turns a directory of parts into a resumable checkpoint.
func (e *Write) Finish(ctx context.Context, pipeline, run string) error {
	if err := e.Flush(ctx); err != nil {
		return err
	}
	m := Manifest{
		Version: manifestVersion, Records: e.written, Parts: e.parts,
		Numbers: e.numbers, Pipeline: pipeline, Run: run,
		WrittenAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := e.d.store.Create(ctx, e.d.bucket, e.d.key(manifestFile), bytes.NewReader(data)); err != nil {
		return fmt.Errorf("writing the checkpoint's manifest: %w", err)
	}
	return nil
}

// Written describes what already became a part, to be re-read when the write
// failed midway. The count is exact, so Reread's check still holds on this
// path.
func (e *Write) Written() *Manifest {
	return &Manifest{
		Version: manifestVersion, Records: e.written,
		Parts: e.parts, Numbers: e.numbers,
	}
}

// Pending are the records sitting in the buffer that never reached an object.
//
// It decodes them back rather than keeping a second copy: that way the normal
// run pays no memory at all for a path that only executes when the bucket fails
// midway.
func (e *Write) Pending() iter.Seq2[core.Envelope, error] {
	data := e.buf.Bytes()
	numbers := e.numbers
	return func(yield func(core.Envelope, error) bool) {
		dec := json.NewDecoder(bytes.NewReader(data))
		if numbers == NumbersLiteral {
			dec.UseNumber()
		}
		for dec.More() {
			var payload any
			if err := dec.Decode(&payload); err != nil {
				yield(core.Envelope{}, fmt.Errorf("re-reading the checkpoint's buffer: %w", err))
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

func asDirectory(p string) string {
	if p == "" || strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}
