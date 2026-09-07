package consumer_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

// Written from outside the SDK's module on purpose.
//
// The same class of defect got past tests three times when those tests lived
// inside the package and proved what the author could see, rather than what a
// consumer can reach: a Data.Stats() that did not exist, three With* options with
// no re-export, and the cmd/brevis CI never built.
//
// This package is in the examples module, which has a replace to ../sdk. It
// compiles against the working tree and runs in CI, so a break in the public
// surface shows up here before it becomes a release.

func source(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"a","v":1}`))
	}))
}

// run executes the Pipeline the way the binary would and returns what was
// logged.
func run(t *testing.T, p sdk.Pipeline) string {
	t.Helper()
	r, w, _ := os.Pipe()
	stderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = stderr }()

	err := sdk.Execute(context.Background(), &p, []string{"-dry-run"})
	_ = w.Close()

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, e := r.Read(buf)
		sb.Write(buf[:n])
		if e != nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return sb.String()
}

func pipeline(url string) sdk.Pipeline {
	return sdk.Pipeline{
		Name:   "proof/entity",
		Source: sdk.Source{From: from.HTTP{URL: url}},
		Target: sdk.Target{
			To: bigquery.Table{
				Project: "p",
				Name:    "prova",
				// nil: the engine decides, or nobody does.
				CreateTable: nil,
			},
		},
	}
}

func TestWithNoEngineNothingHappens(t *testing.T) {
	srv := source(t)
	defer srv.Close()

	log := run(t, pipeline(srv.URL))

	if strings.Contains(log, "under Brevis") {
		t.Error("run by hand, the fetcher should not even know this exists")
	}
}

func TestWithTheEngineTheFetcherKnowsItIsRunByIt(t *testing.T) {
	srv := source(t)
	defer srv.Close()

	t.Setenv("BREVIS_RUN_ID", "run-1")
	t.Setenv("BREVIS_RUN_FIRST", "true")
	t.Setenv("BREVIS_RUN_ATTEMPT", "1")

	log := run(t, pipeline(srv.URL))

	if !strings.Contains(log, "under Brevis") {
		t.Errorf("the engine's context did not arrive: %s", log)
	}
	if !strings.Contains(log, "first=true") {
		t.Errorf("the first run was not reported: %s", log)
	}
}

func TestBeforeSeesTheParams(t *testing.T) {
	srv := source(t)
	defer srv.Close()

	t.Setenv("BREVIS_RUN_ID", "run-1")
	t.Setenv("BREVIS_RUN_PARAMS", `{"load_full":"true"}`)

	var viu string
	p := pipeline(srv.URL)
	p.Before = func(ctx context.Context, p *sdk.Pipeline) error {
		viu = p.Run.Params["load_full"]
		return nil
	}
	run(t, p)

	if viu != "true" {
		t.Errorf(`p.Run.Params["load_full"] = %q, esperado "true"`, viu)
	}
}

func TestParamsIsNeverNil(t *testing.T) {
	srv := source(t)
	defer srv.Close()

	// With no engine at all: reading a missing key must not blow up.
	p := pipeline(srv.URL)
	p.Before = func(ctx context.Context, p *sdk.Pipeline) error {
		_ = p.Run.Params["qualquer"]
		return nil
	}
	run(t, p) // a panic here fails the test
}

func TestBoolEstaExportado(t *testing.T) {
	// sdk.Bool is the only way to express an explicit refusal, and three options
	// have already ended up unreachable for not having been re-exported.
	if v := sdk.Bool(false); v == nil || *v {
		t.Error("sdk.Bool(false) should return a pointer to false")
	}
}

func TestTheEnginesConstantsAreExported(t *testing.T) {
	// The consumer needs them to write a test like this one.
	for nome, v := range map[string]string{
		"EnvRunID":         sdk.EnvRunID,
		"EnvRunFirst":      sdk.EnvRunFirst,
		"EnvRunParams":     sdk.EnvRunParams,
		"ParamCreateTable": sdk.ParamCreateTable,
	} {
		if v == "" {
			t.Errorf("%s is empty", nome)
		}
	}
}

// The preview exists so the consumer can see the data. If they cannot reach the
// fields, or if the writer is not theirs, the feature does not exist for the
// person who matters.
func TestAConsumerTurnsThePreviewOnAndPicksWhere(t *testing.T) {
	srv := source(t)

	var out strings.Builder
	data, err := sdk.Extract(context.Background(), sdk.Source{
		From:          from.HTTP{URL: srv.URL},
		Preview:       2,
		PreviewBytes:  2048,
		PreviewWriter: &out,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for range data.Records {
	}

	if out.Len() == 0 {
		t.Fatal("the consumer asked for a preview and nothing was written to their writer")
	}
	if !strings.Contains(out.String(), "1 row · 2 columns") {
		t.Errorf("the footer did not come along:\n%s", out.String())
	}
}

// A number the consumer cannot read is a number that does not exist.
func TestAConsumerReadsTheSizeOfWhatWasExtracted(t *testing.T) {
	srv := source(t)

	data, err := sdk.Extract(context.Background(), sdk.Source{From: from.HTTP{URL: srv.URL}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for range data.Records {
	}

	if data.Stats().Bytes <= 0 {
		t.Errorf("Data.Stats().Bytes = %d depois de drenar o fluxo", data.Stats().Bytes)
	}
}

// The payload is the client's. With no Metadata block the SDK asks for no
// provenance, reads no field of the record, and writes nothing beyond what it
// received.
func TestAConsumerLoadsWithNoProvenanceAtAll(t *testing.T) {
	t.Setenv("GOOGLE_PROJECT_ID", "um-projeto")

	srv := source(t)
	data, err := sdk.Extract(context.Background(), sdk.Source{From: from.HTTP{URL: srv.URL}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	// No Provider, no Entity, no Key. Only where to write.
	_, err = sdk.Load(context.Background(), data, sdk.Target{To: bigquery.Table{Name: "minha_tabela"}})

	// With no credential the load never reaches BigQuery, and that is enough: what
	// this test proves is that the facade's validation lets it through. An error
	// naming Provider, Entity or Key would be the regression.
	if err != nil {
		for _, proibido := range []string{"Provider", "Entity", "Key"} {
			if strings.Contains(err.Error(), proibido) {
				t.Errorf("the SDK still demands %s with no Metadata block: %v", proibido, err)
			}
		}
	}
}

// And with the flag on it does demand them, because then there is something to
// build.
func TestAConsumerComposesTheColumnsInTransform(t *testing.T) {
	srv := source(t)

	data, err := sdk.Extract(context.Background(), sdk.Source{From: from.HTTP{URL: srv.URL}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	data = sdk.Transform(data, sdk.Accept("id"))

	n := 0
	for env, err := range data.Records {
		if err != nil {
			t.Fatalf("registro %d: %v", n, err)
		}
		obj, ok := env.Payload.(map[string]any)
		if !ok {
			t.Fatalf("registro %d mudou de forma: %T", n, env.Payload)
		}
		if len(obj) != 1 {
			t.Errorf("record %d has %d fields, expected only the declared one: %v", n, len(obj), obj)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no record got through")
	}
}

func TestAConsumerSeesTheSchemaFailWhenTheFieldGoes(t *testing.T) {
	srv := source(t)

	data, err := sdk.Extract(context.Background(), sdk.Source{From: from.HTTP{URL: srv.URL}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	data = sdk.Transform(data, sdk.Accept("campo_que_a_fonte_parou_de_mandar"))

	for _, err := range data.Records {
		if err == nil {
			t.Fatal("a field declared and missing has to be an error, not a NULL column")
		}
		if !strings.Contains(err.Error(), "campo_que_a_fonte_parou_de_mandar") {
			t.Errorf("the error does not name the field: %v", err)
		}
		return
	}
	t.Fatal("the flow finished with no error at all")
}

// AutoID is the whole declaration: nothing from the record goes into the id, so
// nothing about the record has to be described.

// The two ingestion transformers are the surface that replaced the block. If the
// consumer cannot reach them, the change did not happen for the person who
// matters.
func TestAConsumerWritesBothColumnsInTheChain(t *testing.T) {
	srv := source(t)

	data, err := sdk.Extract(context.Background(), sdk.Source{From: from.HTTP{URL: srv.URL}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	data = sdk.Transform(data,
		sdk.Compute("provider", func(map[string]any) (any, error) { return "prova", nil }),
		sdk.Compute("entity", func(map[string]any) (any, error) { return "linhas", nil }),
		sdk.ComputeText("source_key", sdk.Key("id")),
		sdk.IngestionID("provider", "entity", "source_key", "id"),
		sdk.IngestionLoadedAt(),
	)

	n := 0
	for env, err := range data.Records {
		if err != nil {
			t.Fatalf("registro %d: %v", n, err)
		}
		linha := env.Payload.(map[string]any)
		for _, coluna := range []string{sdk.ColumnIngestionID, sdk.ColumnIngestionLoadedAt} {
			if v, tem := linha[coluna]; !tem || v == "" {
				t.Errorf("record %d came out without %s: %v", n, coluna, linha)
			}
		}
		n++
	}
	if n == 0 {
		t.Fatal("no record got through")
	}
}
