package extract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// paginatorWithMeta serves N pages and says, in the body, whether there is a
// next one.
func paginatorWithMeta(t *testing.T, paginas int, mente bool) (*httptest.Server, *int) {
	t.Helper()
	var pedidas int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidas++
		p := 1
		if v := r.URL.Query().Get("page"); v != "" {
			_, _ = fmt.Sscanf(v, "%d", &p)
		}
		temMais := p < paginas
		if mente {
			// It lies, always saying there is more; the safety net (an empty
			// page
			// vazia) tem de encerrar mesmo assim.
			temMais = true
		}
		linhas := `[{"n":1}]`
		if p > paginas {
			linhas = `[]`
		}
		_, _ = fmt.Fprintf(w, `{"pageMeta":{"hasNextPage":%t},"results":%s}`, temMais, linhas)
	}))
	t.Cleanup(srv.Close)
	return srv, &pedidas
}

func contar(t *testing.T, s core.Source) int {
	t.Helper()
	seq, err := JSON(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	n := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
		n++
	}
	return n
}

// TestMoreKeyStopsWithoutAskingForTheEmptyPage is item 12: stopping on an empty
// page costs one extra request PER SOURCE, and on a fan-out of hundreds of
// sources that is hundreds of requests per run.
func TestMoreKeyStopsWithoutAskingForTheEmptyPage(t *testing.T) {
	srv, pedidas := paginatorWithMeta(t, 3, false)

	linhas := contar(t, core.Source{
		URL: srv.URL, PageKey: "page", DataKey: "results", MoreKey: "pageMeta.hasNextPage",
	})
	if linhas != 3 {
		t.Errorf("%d linhas, esperado 3", linhas)
	}
	if *pedidas != 3 {
		t.Errorf("%d requisições, esperado 3 -- a quarta é a que o MoreKey economiza", *pedidas)
	}
}

// TestWithoutMoreKeyItAsksForTheEmptyPage is the other side: without it, the
// fourth request happens. It is the measure of what the item saves.
func TestWithoutMoreKeyItAsksForTheEmptyPage(t *testing.T) {
	srv, pedidas := paginatorWithMeta(t, 3, false)

	contar(t, core.Source{URL: srv.URL, PageKey: "page", DataKey: "results"})
	if *pedidas != 4 {
		t.Errorf("%d requisições, esperado 4 -- a quarta volta vazia e é o que encerra", *pedidas)
	}
}

// TestMoreKeyDoesNotReplaceTheSafetyNet: an API that lies in the field must not
// become an infinite loop.
func TestMoreKeyDoesNotReplaceTheSafetyNet(t *testing.T) {
	srv, pedidas := paginatorWithMeta(t, 2, true)

	linhas := contar(t, core.Source{
		URL: srv.URL, PageKey: "page", DataKey: "results", MoreKey: "pageMeta.hasNextPage",
	})
	if linhas != 2 {
		t.Errorf("%d linhas, esperado 2", linhas)
	}
	// The third comes back empty and ends it, despite the field saying there is
	// more.
	if *pedidas != 3 {
		t.Errorf("%d requisições; a parada por página vazia devia ter encerrado", *pedidas)
	}
}

// TestAnAbsentMoreKeyIsNotTheEndOfPagination: treating absent as the end would
// make pagination stop at the first page in silence, which is worse than not
// having the optimisation.
func TestAnAbsentMoreKeyIsNotTheEndOfPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"results":[{"n":1}]}`)
	}))
	defer srv.Close()

	// The error comes from the JSON and not from the iteration: the first page is
	// fetched early, on purpose, so a wrong path fails before the consumer drains
	// the sequence.
	_, err := JSON(context.Background(), core.Source{
		URL: srv.URL, DataKey: "results", MoreKey: "pageMeta.hasNextPage",
	}, nil)
	if err == nil {
		t.Fatal("um campo ausente passou como 'não há mais'")
	}
	for _, quero := range []string{"pageMeta", "check the path"} {
		if !strings.Contains(err.Error(), quero) {
			t.Errorf("o erro não diz %q: %v", quero, err)
		}
	}
}

// TestMoreKeyAtTheRoot: a path with no dot works too -- has_more is common at
// the
// raiz.
func TestMoreKeyAtTheRoot(t *testing.T) {
	var pedidas int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidas++
		p := 1
		if v := r.URL.Query().Get("page"); v != "" {
			_, _ = fmt.Sscanf(v, "%d", &p)
		}
		_, _ = fmt.Fprintf(w, `{"has_more":%t,"data":[{"n":%d}]}`, p < 2, p)
	}))
	defer srv.Close()

	if n := contar(t, core.Source{
		URL: srv.URL, PageKey: "page", DataKey: "data", MoreKey: "has_more",
	}); n != 2 {
		t.Errorf("%d linhas, esperado 2", n)
	}
	if pedidas != 2 {
		t.Errorf("%d requisições, esperado 2", pedidas)
	}
}

// TestANullMoreKeyIsTheEnd: null is the form several APIs use for "that is
// all".
func TestANullMoreKeyIsTheEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"next":null,"data":[{"n":1}]}`)
	}))
	defer srv.Close()

	if n := contar(t, core.Source{URL: srv.URL, DataKey: "data", MoreKey: "next"}); n != 1 {
		t.Errorf("%d linhas, esperado 1", n)
	}
}

// TestAMoreKeyNotLeadingToABooleanIsAnError: a field that exists and is not a
// boolean is an error naming the type, and not a guess about its
// truthiness.
func TestAMoreKeyNotLeadingToABooleanIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"meta":{"next":"sim"},"data":[{"n":1}]}`)
	}))
	defer srv.Close()

	_, err := JSON(context.Background(), core.Source{
		URL: srv.URL, DataKey: "data", MoreKey: "meta.next",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "boolean") {
		t.Errorf("erro = %v", err)
	}
}
