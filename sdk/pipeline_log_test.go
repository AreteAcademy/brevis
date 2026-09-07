package sdk

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/from"
)

// fakeTarget is a Writer that returns a result and an error together, which is
// what every destination does on a refusal -- the result exists so RowErrors is
// readable afterwards.
type fakeTarget struct {
	falha bool
	rows  []string
}

func (fakeTarget) Describe() string { return "destino.teste" }

func (d fakeTarget) Write(context.Context, []Envelope, WriteOptions) (*LoadResult, error) {
	res := &LoadResult{RowsLoaded: 2, ErrorRows: d.rows}
	if d.falha {
		res.RowsLoaded = 0
		return res, context.DeadlineExceeded
	}
	return res, nil
}

func rodaCapturandoLog(t *testing.T, destino Writer) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":1},{"id":2}]`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(anterior)

	_ = runPipeline(context.Background(), &Pipeline{
		Source: Source{From: from.HTTP{URL: srv.URL}},
		Target: Target{To: destino},
	})

	return buf.String()
}

// A load that did not load must not log "loaded".
//
// The result comes back filled on the error path by design, so RowErrors is
// readable -- which makes the message the only thing that tells the two cases
// apart. And a "loaded" at INFO during a failure never reaches whoever watches
// ERROR.
func TestTheLogDoesNotSayLoadedWhenItDidNot(t *testing.T) {
	saida := rodaCapturandoLog(t, fakeTarget{falha: true, rows: []string{"linha 0 recusada"}})

	if strings.Contains(saida, "msg=loaded") {
		t.Errorf("uma carga que falhou logou \"loaded\":\n%s", saida)
	}
	if !strings.Contains(saida, `msg="load failed"`) {
		t.Errorf("a falha precisa aparecer na linha que resume:\n%s", saida)
	}
	if !strings.Contains(saida, "level=ERROR msg=\"load failed\"") {
		t.Errorf("a linha que resume uma falha tem de ser ERROR:\n%s", saida)
	}
	// And the counters are still there, which is why the result comes back.
	if !strings.Contains(saida, "lines=0") || !strings.Contains(saida, "records=2") {
		t.Errorf("os contadores se perderam na troca:\n%s", saida)
	}
	if !strings.Contains(saida, "row rejected") {
		t.Errorf("as linhas recusadas sumiram:\n%s", saida)
	}
}

func TestTheLogSaysLoadedWhenItLoaded(t *testing.T) {
	saida := rodaCapturandoLog(t, fakeTarget{})

	if !strings.Contains(saida, "level=INFO msg=loaded") {
		t.Errorf("uma carga que funcionou tem de logar loaded em INFO:\n%s", saida)
	}
	if strings.Contains(saida, "load failed") {
		t.Errorf("uma carga que funcionou não pode logar falha:\n%s", saida)
	}
}
