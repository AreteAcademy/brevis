package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/auth"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

var logRun = uuid.MustParse("3f2a9c1e-7b44-4d0e-9a51-0c6d2e8f1a77")

func exitCode(n int) *int { return &n }

func logUI(e execsFake) *api.UI {
	return api.NewUI(nil, nil, e, nil, nil, nil, branding.Default(), slog.New(slog.DiscardHandler))
}

func download(t *testing.T, ui *api.UI, path string) *http.Response {
	t.Helper()
	mux := http.NewServeMux()
	ui.Registrar(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Result()
}

// THE WHOLE LOG, TO KEEP OR TO SEND: every attempt of every step, in the
// order they ran, each under a header saying which it is and how it ended --
// the first attempt counted as 1, like the reader does.
func TestTheRunsLogDownloadsAsOneTextFile(t *testing.T) {
	ui := logUI(execsFake{
		run: dom.Run{ID: logRun, WorkflowSlug: "daily_sales", Status: dom.StatusFailed},
		logs: []postgres.StepLog{
			{NodeID: "extract", MapIndex: dom.Unmapped, Attempt: 0, Status: "success", ExitCode: exitCode(0), Log: "fetched 1209 rows\n"},
			{NodeID: "load", MapIndex: 2, Attempt: 0, Status: "failed", ExitCode: exitCode(1), Log: "Error: boom"},
			{NodeID: "load", MapIndex: 2, Attempt: 1, Status: "failed", Err: "deadline exceeded"},
		},
	})
	res := download(t, ui, "/runs/"+logRun.String()+"/log.txt")
	body, _ := io.ReadAll(res.Body)

	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("got %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if got := res.Header.Get("Content-Disposition"); got != `attachment; filename="daily_sales-3f2a9c1e.log"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	want := "== extract · attempt 1 · success · exit 0\n" +
		"fetched 1209 rows\n" +
		"\n== load[2] · attempt 1 · failed · exit 1\n" +
		"Error: boom\n" +
		"\n== load[2] · attempt 2 · failed\n" +
		"deadline exceeded\n"
	if string(body) != want {
		t.Errorf("the log =\n%s\nwant\n%s", body, want)
	}
}

// THE FILE NAME CARRIES NOTHING A HEADER CAN BE BROKEN WITH: a slug is the
// author's text, and a quote or a newline in it would end the header.
func TestTheLogsFileNameIsSanitized(t *testing.T) {
	ui := logUI(execsFake{run: dom.Run{ID: logRun, WorkflowSlug: "a\"b\r\n/c d"}})
	res := download(t, ui, "/runs/"+logRun.String()+"/log.txt")
	if got := res.Header.Get("Content-Disposition"); got != `attachment; filename="a_b___c_d-3f2a9c1e.log"` {
		t.Errorf("Content-Disposition = %q", got)
	}
}

// A RUN THAT DOES NOT EXIST IS A 404, AND AN ID THAT IS NOT ONE IS A 400.
func TestTheLogOfNoRunIsRefused(t *testing.T) {
	if res := download(t, logUI(execsFake{err: pgx.ErrNoRows}), "/runs/"+logRun.String()+"/log.txt"); res.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown run = %d, want 404", res.StatusCode)
	}
	if res := download(t, logUI(execsFake{}), "/runs/not-a-uuid/log.txt"); res.StatusCode != http.StatusBadRequest {
		t.Errorf("a malformed id = %d, want 400", res.StatusCode)
	}
}

// THE RUN'S PAGE OFFERS IT, next to the step output, in the reader's language.
func TestTheRunsPageLinksToItsLog(t *testing.T) {
	ui := logUI(execsFake{
		run:  dom.Run{ID: logRun, WorkflowSlug: "daily_sales", Status: dom.StatusFailed},
		logs: []postgres.StepLog{{NodeID: "load", MapIndex: dom.Unmapped, Status: "failed", Log: "Error: boom"}},
	})
	href := `href="/runs/` + logRun.String() + `/log.txt"`
	for lang, label := range map[string]string{"": ">Download log<", "pt-BR": ">Baixar log<", "es": ">Descargar log<"} {
		body := pageIn(t, ui, "/runs/"+logRun.String(), lang, "", false)
		if !strings.Contains(body, href) || !strings.Contains(body, label) {
			t.Errorf("%s: the run's page has no %s link to its log", lang, label)
		}
	}
}

// THE LOG IS BEHIND THE SAME LOGIN AS EVERY PAGE: a log carries what a step
// printed, and that can be a customer's data.
func TestTheLogIsNotGivenToSomebodyNotSignedIn(t *testing.T) {
	ui := logUI(execsFake{
		run:  dom.Run{ID: logRun, WorkflowSlug: "daily_sales"},
		logs: []postgres.StepLog{{NodeID: "load", MapIndex: dom.Unmapped, Log: "a customer's row"}},
	})
	srv := api.NewServerAutenticado(slog.New(slog.DiscardHandler), nil, ui,
		auth.Credential{User: "operator", Hash: "x", Secret: []byte("0123456789abcdef0123456789abcdef")}, true)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs/"+logRun.String()+"/log.txt", nil))
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "a customer's row") {
		t.Errorf("signed out, the log = %d %q", rec.Code, rec.Body.String())
	}
}
