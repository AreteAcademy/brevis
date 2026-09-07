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

// paddedJWT imitates a NextAuth token: base64 with "=" padding, which is the
// character that breaks whoever splits name=value on every "=".
const paddedJWT = "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2R0NNIn0..QUJDRA=="

// TestTheCallersCookieArrivesWhole: the consumer wrote 36 lines to assemble
// Set-Cookie ao header, e a armadilha foi cortar o JWT no segundo "=". Aqui o
// the cookie has to arrive identical to what the caller passed.
func TestTheCallersCookieArrivesWhole(t *testing.T) {
	var recebido string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session-token")
		if err != nil {
			http.Error(w, "sem cookie", http.StatusUnauthorized)
			return
		}
		recebido = c.Value
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	}))
	defer srv.Close()

	drenar(t, core.Source{
		URL:    srv.URL,
		Header: map[string][]string{"Cookie": {"session-token=" + paddedJWT}},
	})

	if recebido != paddedJWT {
		t.Errorf("o servidor recebeu %q, o caller mandou %q", recebido, paddedJWT)
	}
}

// TestARenewedCookieSurvivesIntoTheNextPage: it is exactly what the consumer's
// cookie.go did by hand. If the jar disappears, page 2 goes with the old
// token.
func TestARenewedCookieSurvivesIntoTheNextPage(t *testing.T) {
	var vistos []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session-token")
		if err != nil {
			http.Error(w, "sem cookie", http.StatusUnauthorized)
			return
		}
		vistos = append(vistos, c.Value)

		if len(vistos) == 1 {
			// a api renova a sessao no meio da caminhada
			http.SetCookie(w, &http.Cookie{Name: "session-token", Value: "renovado==", Path: "/"})
			_, _ = fmt.Fprint(w, `{"results":[{"n":1}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"results":[]}`)
	}))
	defer srv.Close()

	drenar(t, core.Source{
		URL:     srv.URL,
		PageKey: "page",
		DataKey: "results",
		Header:  map[string][]string{"Cookie": {"session-token=" + paddedJWT}},
	})

	if len(vistos) != 2 {
		t.Fatalf("esperava 2 requisicoes, houve %d", len(vistos))
	}
	if vistos[1] != "renovado==" {
		t.Errorf("a pagina 2 foi com %q; o Set-Cookie da pagina 1 nao pegou", vistos[1])
	}
}

// TestTheCookieDoesNotGoTwice: the caller's header and the jar are the same
// thing. If both go, the server receives two values for the same name and
// escolhe um deles -- silenciosamente o errado.
func TestTheCookieDoesNotGoTwice(t *testing.T) {
	var bruto string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bruto = r.Header.Get("Cookie")
		http.SetCookie(w, &http.Cookie{Name: "session-token", Value: "renovado==", Path: "/"})
		if strings.Contains(bruto, "renovado") {
			_, _ = fmt.Fprint(w, `{"results":[]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"results":[{"n":1}]}`)
	}))
	defer srv.Close()

	drenar(t, core.Source{
		URL:     srv.URL,
		PageKey: "page",
		DataKey: "results",
		Header:  map[string][]string{"Cookie": {"session-token=" + paddedJWT}},
	})

	if n := strings.Count(bruto, "session-token="); n != 1 {
		t.Errorf("session-token aparece %d vezes no header: %q", n, bruto)
	}
}

// TestAMalformedCookieFailsEarly: an invalid cookie header has to complain at
// assembly time, not become a 401 at the server.
func TestAMalformedCookieFailsEarly(t *testing.T) {
	_, err := JSON(context.Background(), core.Source{
		URL:    "http://exemplo.invalido",
		Header: map[string][]string{"Cookie": {"isso nao e um cookie"}},
	}, nil)
	if err == nil {
		t.Fatal("cookie invalido passou")
	}
	if !strings.Contains(err.Error(), "Cookie") {
		t.Errorf("o erro nao diz que o problema e o Cookie: %v", err)
	}
}

// TestTheCallersHeaderIsNotMutated: the header belongs to the consumer, and they
// may reuse the
// mesmo mapa em outra pipeline.
func TestTheCallersHeaderIsNotMutated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	}))
	defer srv.Close()

	h := map[string][]string{"Cookie": {"session-token=" + paddedJWT}}
	drenar(t, core.Source{URL: srv.URL, Header: h})

	if got := http.Header(h).Get("Cookie"); got != "session-token="+paddedJWT {
		t.Errorf("o SDK mexeu no header do caller: %q", got)
	}
}

func drenar(t *testing.T, s core.Source) {
	t.Helper()
	seq, err := JSON(context.Background(), s, nil)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
	}
}

// TestASecurePrefixedCookieDoesNotVanishSilently: o nome real do cookie do NextAuth
// starts with __Secure-, which in a browser spec only applies over https. If
// the
// jar aplicasse essa regra, o cookie sumiria antes de sair -- e o SDK falharia
// with a 401 without ever saying it discarded the credential.
//
// The stdlib's jar does not apply the prefix rule. This test exists for the day
// that changes.
func TestASecurePrefixedCookieDoesNotVanishSilently(t *testing.T) {
	var visto string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visto = r.Header.Get("Cookie")
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	}))
	defer srv.Close()

	drenar(t, core.Source{URL: srv.URL, Auth: &core.Credential{
		Value: func(context.Context) (string, error) {
			return "__Secure-authjs.session-token=abc==", nil
		},
		Apply: core.AsCookie,
	}})

	if visto == "" {
		t.Fatal("o cookie __Secure- sumiu antes de chegar ao servidor")
	}
}
