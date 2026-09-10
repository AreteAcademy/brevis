package sdk_test

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

// TestTheSnapshotDoesNotDependOnThePositionInTheChain is the reason the
// snapshot is NOT a transformer.
//
// As a transformer, the snapshot would depend on position: placing it after a
// Compute would produce a "raw" record carrying the field the chain had just
// writing it. That produces no error -- it produces wrong data nobody notices
// until somebody queries it months later.
//
// Taken where the record leaves the source, no ordering can contaminate it.
func TestTheSnapshotDoesNotDependOnThePositionInTheChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"id":"1","temperatura":19.5}`)
	}))
	defer srv.Close()

	data, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.HTTP{URL: srv.URL}, Snapshot: "payload",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The chain writes AFTER the snapshot, and writes plenty.
	data = sdk.Transform(data,
		sdk.Compute("derivado", func(map[string]any) (any, error) { return "novo", nil }),
		sdk.Rename(map[string]string{"id": "source_key"}),
	)

	var line map[string]any
	for env, err := range data.Records {
		if err != nil {
			t.Fatal(err)
		}
		line = env.Payload.(map[string]any)
	}

	retrato, ok := line["payload"].(map[string]any)
	if !ok {
		t.Fatalf("sem retrato: %v", line)
	}
	if _, contaminado := retrato["derivado"]; contaminado {
		t.Errorf("o retrato carrega um campo que a cadeia escreveu: %v", retrato)
	}
	if retrato["id"] != "1" {
		t.Errorf("o retrato perdeu o nome original do campo: %v", retrato)
	}
	if line["source_key"] != "1" {
		t.Errorf("a cadeia não rodou sobre o registro: %v", line)
	}
}

// TestTheSnapshotRefusesToOverwriteWhatTheSourceSent: writing over it would
// lose the
// that came from the source, in silence.
func TestTheSnapshotRefusesToOverwriteWhatTheSourceSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"payload":"da fonte"}`)
	}))
	defer srv.Close()

	data, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.HTTP{URL: srv.URL}, Snapshot: "payload",
	})
	if err != nil {
		t.Fatal(err)
	}
	var visto error
	for _, err := range data.Records {
		if err != nil {
			visto = err
		}
	}
	if visto == nil {
		t.Fatal("gravou o retrato por cima de um campo da fonte")
	}
	if !strings.Contains(visto.Error(), "payload") {
		t.Errorf("o erro não nomeia o campo: %v", visto)
	}
}

// TestSkipWithoutDropsInsteadOfFailing: a row without the field that composes
// the key has no stable identity and cannot go in -- but it is also no reason
// to
// derrubar a janela inteira.
func TestSkipWithoutDropsInsteadOfFailing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id":"1"},{"id":null},{"outro":2},{"id":"4"}]`)
	}))
	defer srv.Close()

	data, err := sdk.Extract(context.Background(), sdk.Source{From: from.HTTP{URL: srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	data = sdk.Transform(data, sdk.SkipWithout("id"))

	var ids []string
	for env, err := range data.Records {
		if err != nil {
			t.Fatalf("SkipWithout derrubou a janela: %v", err)
		}
		ids = append(ids, env.Payload.(map[string]any)["id"].(string))
	}
	if len(ids) != 2 || ids[0] != "1" || ids[1] != "4" {
		t.Errorf("sobraram %v; esperava as duas com id -- nulo e ausente são a mesma coisa aqui", ids)
	}
}

// TestTheNamespaceChangesTheID is item 1: the namespace belongs to whoever uses
// it, not to the
// biblioteca.
func TestTheNamespaceChangesTheID(t *testing.T) {
	record := func() map[string]any {
		return map[string]any{
			"provider": "acme", "entity": "pedidos",
			"source_key": "1", "record_ts": "2026-09-05T12:00:00Z",
		}
	}

	fallback, err := sdk.IngestionID()(record())
	if err != nil {
		t.Fatal(err)
	}
	meu := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	outro, err := sdk.Namespace(meu).IngestionID()(record())
	if err != nil {
		t.Fatal(err)
	}

	defaultID := fallback.(map[string]any)[sdk.ColumnIngestionID].(string)
	idOutro := outro.(map[string]any)[sdk.ColumnIngestionID].(string)
	if defaultID == idOutro {
		t.Error("namespaces diferentes produziram o mesmo id; então o namespace não é usado")
	}
}

// TestTheDefaultNamespaceHasNotChanged is the guarantee that stops the feature
// from breaking whoever has already written: the id for somebody who chooses no
// namespace has to be byte for byte the one from before.
func TestTheDefaultNamespaceHasNotChanged(t *testing.T) {
	output, err := sdk.IngestionID()(map[string]any{
		"provider": "open_meteo", "entity": "hourly",
		"source_key": "123", "record_ts": "2026-09-05T12:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := output.(map[string]any)[sdk.ColumnIngestionID].(string)

	// This value has been the same since v0.1.x, checked against Python's
	// uuid.uuid5. If it changes, every row already written has lost its
	// identity.
	const congelado = "7e18f9f9-37c4-5033-abce-def940db4cba"
	if got != congelado {
		t.Errorf("o id padrão mudou: %s, era %s", got, congelado)
	}
}

// TestAChosenNamespaceIsDeterministic: choosing a namespace must not introduce
// variation between runs.
func TestAChosenNamespaceIsDeterministic(t *testing.T) {
	meu := sdk.Namespace(uuid.MustParse("11111111-2222-3333-4444-555555555555"))
	var previous string
	for i := 0; i < 5; i++ {
		output, err := meu.IngestionID()(map[string]any{
			"provider": "a", "entity": "b", "source_key": "c", "record_ts": "d",
		})
		if err != nil {
			t.Fatal(err)
		}
		got := output.(map[string]any)[sdk.ColumnIngestionID].(string)
		if i > 0 && got != previous {
			t.Fatalf("o id variou entre execuções: %s e %s", previous, got)
		}
		previous = got
	}
}

// countingTarget records every batch it receives.
type countingTarget struct {
	levas  [][]int
	failAt int // > 0: a leva N falha
}

func (d *countingTarget) Describe() string { return "destino de teste" }

func (d *countingTarget) Write(_ context.Context, envs []sdk.Envelope, _ sdk.WriteOptions) (*sdk.LoadResult, error) {
	var ids []int
	for _, e := range envs {
		ids = append(ids, e.Payload.(map[string]any)["i"].(int))
	}
	d.levas = append(d.levas, ids)
	if d.failAt > 0 && len(d.levas) == d.failAt {
		return &sdk.LoadResult{RowsLoaded: 0}, fmt.Errorf("a leva %d falhou", d.failAt)
	}
	return &sdk.LoadResult{RowsLoaded: int64(len(envs))}, nil
}

// TestFlushEveryWritesInBatches: a long read must not have the whole batch alive
// in memory, and the destination builds a second copy of it to serialize.
func TestFlushEveryWritesInBatches(t *testing.T) {
	target := &countingTarget{}
	res, err := sdk.Load(context.Background(), dataOf(t, 10), sdk.Target{
		To: target, FlushEvery: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(target.levas) != 4 {
		t.Fatalf("%d levas, esperado 4 (3+3+3+1)", len(target.levas))
	}
	if len(target.levas[3]) != 1 {
		t.Errorf("a última leva tem %d, esperado 1", len(target.levas[3]))
	}

	// The Result sums the batches. A Rows counting only the last one would lie
	// to whoever reads the pipeline's line.
	if res.Rows != 10 || res.Records != 10 {
		t.Errorf("Rows=%d Records=%d, esperado 10 e 10", res.Rows, res.Records)
	}
}

// TestFlushEveryZeroAccumulatesEverything: the default does not change.
func TestFlushEveryZeroAccumulatesEverything(t *testing.T) {
	target := &countingTarget{}
	if _, err := sdk.Load(context.Background(), dataOf(t, 10), sdk.Target{To: target}); err != nil {
		t.Fatal(err)
	}
	if len(target.levas) != 1 || len(target.levas[0]) != 10 {
		t.Errorf("levas = %v; sem FlushEvery a carga é uma só", target.levas)
	}
}

// TestFlushEveryFailingMidwaySaysWhatWentIn: the load stops being atomic, and
// hiding that the previous batches were written would be worse than saying so
// -- whoever
// re-runs it needs to know that 6 rows are already there.
func TestFlushEveryFailingMidwaySaysWhatWentIn(t *testing.T) {
	target := &countingTarget{failAt: 3}
	res, err := sdk.Load(context.Background(), dataOf(t, 10), sdk.Target{
		To: target, FlushEvery: 3,
	})
	if err == nil {
		t.Fatal("a leva falhou e o Load deu certo")
	}
	if res == nil {
		t.Fatal("o Result não voltou; quem reexecuta não saberia o que já entrou")
	}
	if res.Rows != 6 {
		t.Errorf("Rows = %d, esperado 6 -- as duas primeiras levas gravaram", res.Rows)
	}
}

func dataOf(t *testing.T, n int) *sdk.Data {
	t.Helper()
	data, err := sdk.Extract(context.Background(), sdk.Source{From: sourceOfN{n}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type sourceOfN struct{ n int }

func (sourceOfN) Describe() string { return "fonte de teste" }
func (f sourceOfN) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	return func(yield func(sdk.Envelope, error) bool) {
		for i := 0; i < f.n; i++ {
			if !yield(sdk.Envelope{Payload: map[string]any{"i": i}}, nil) {
				return
			}
		}
	}, nil
}
