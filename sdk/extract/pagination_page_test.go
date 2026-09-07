package extract

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// numberedPage returns three one-row pages and then an empty one, recording the
// sequence of numbers it received.
func numberedPage(t *testing.T, chave string, vistos *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bruto := r.URL.Query().Get(chave)
		*vistos = append(*vistos, bruto)

		n, err := strconv.Atoi(bruto)
		if err != nil {
			http.Error(w, "sem numero de pagina: "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		if len(*vistos) > 6 { // trava contra loop infinito no teste
			http.Error(w, "paginou demais", http.StatusInternalServerError)
			return
		}
		if n >= 3 {
			_, _ = fmt.Fprint(w, `{"results":[]}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"results":[{"n":%d}]}`, n)
	}))
}

// TestPageKeyAdvancesOneAtATime: the reason the field exists. Before it the
// recipe
// was OffsetKey "page" with PageSize 1, which worked by accident.
func TestPageKeyAdvancesOneAtATime(t *testing.T) {
	var vistos []string
	srv := numberedPage(t, "page", &vistos)
	defer srv.Close()

	linhas := colher(t, core.Source{URL: srv.URL, PageKey: "page", DataKey: "results"})

	if linhas != 2 {
		t.Errorf("linhas = %d, esperado 2 (paginas 1 e 2)", linhas)
	}
	if got := strings.Join(vistos, ","); got != "1,2,3" {
		t.Errorf("paginas pedidas = %q, esperado \"1,2,3\"", got)
	}
}

// TestPageKeyNumbersTheFirstRequest: without it the server picks its own default
// and the SDK guesses the next number -- guessing wrong skips a page
// inteira em silencio.
func TestPageKeyNumbersTheFirstRequest(t *testing.T) {
	casos := []struct {
		nome      string
		url       func(string) string
		primeira  int
		sequencia string
	}{
		{"padrao comeca em 1", func(u string) string { return u }, 0, "1,2,3"},
		{"FirstPage escolhe onde comecar", func(u string) string { return u }, 2, "2,3"},
		{"numero na propria url vence", func(u string) string { return u + "?page=2" }, 0, "2,3"},
		{"a url vence ate contra FirstPage", func(u string) string { return u + "?page=2" }, 9, "2,3"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var vistos []string
			srv := numberedPage(t, "page", &vistos)
			defer srv.Close()

			colher(t, core.Source{
				URL:       c.url(srv.URL),
				PageKey:   "page",
				FirstPage: c.primeira,
				DataKey:   "results",
			})
			if got := strings.Join(vistos, ","); got != c.sequencia {
				t.Errorf("paginas pedidas = %q, esperado %q", got, c.sequencia)
			}
		})
	}
}

// TestAZeroIndexedAPISaysSoInTheURL: FirstPage cannot express "start at zero",
// because zero is the value of somebody who set nothing. The documented way out
// is putting page zero in the URL. This test exists so the doc stays true.
func TestAZeroIndexedAPISaysSoInTheURL(t *testing.T) {
	var vistos []string
	srv := numberedPage(t, "page", &vistos)
	defer srv.Close()

	colher(t, core.Source{URL: srv.URL + "?page=0", PageKey: "page", DataKey: "results"})
	if got := strings.Join(vistos, ","); got != "0,1,2,3" {
		t.Errorf("paginas pedidas = %q, esperado \"0,1,2,3\"", got)
	}
}

// TestPaginationRefusesTwoStrategies: with two set, one would be read and the
// other ignored in silence -- which is the defect this SDK keeps finding.
func TestPaginationRefusesTwoStrategies(t *testing.T) {
	casos := []core.Source{
		{URL: "http://x", PageKey: "page", OffsetKey: "offset"},
		{URL: "http://x", CursorKey: "c", PageKey: "page"},
		{URL: "http://x", FollowLinks: true, OffsetKey: "offset"},
	}
	for _, s := range casos {
		if _, err := JSON(context.Background(), s, nil); err == nil {
			t.Errorf("aceitou duas estrategias: %+v", s)
		}
	}
}

// TestPageSizeAloneIsRefused: PageSize is OffsetKey's step. Set without it, it
// does nothing -- and whoever wrote it believed it did.
func TestPageSizeAloneIsRefused(t *testing.T) {
	_, err := JSON(context.Background(), core.Source{URL: "http://x", PageSize: 100}, nil)
	if err == nil {
		t.Fatal("PageSize sem OffsetKey passou")
	}
	if !strings.Contains(err.Error(), "PageKey") {
		t.Errorf("o erro nao aponta para PageKey: %v", err)
	}

	_, err = JSON(context.Background(), core.Source{URL: "http://x", FirstPage: 2}, nil)
	if err == nil {
		t.Fatal("FirstPage sem PageKey passou")
	}
}

func colher(t *testing.T, s core.Source) int {
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
