package serve

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// refuser is a warehouse whose dry run fails with a real message.
type refuser struct{ fake }

// The shape BigQuery actually answers with, including the thing F8 is about:
// a dataset name, and whether it exists.
const warehouseWords = `Not found: Dataset acme-prod:payroll was not found in location US`

func (*refuser) Estimate(context.Context, string) (int64, error) {
	return 0, errors.New(warehouseWords)
}

func priceless(t *testing.T, token string) *Service {
	t.Helper()
	s, err := New(Options{
		Env: EnvLocal, Rows: 100, Bytes: 10 << 30, Token: token, Audit: &bytes.Buffer{},
		Open: func(context.Context, Table) (dialect.Conn, error) { return &refuser{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A SERVICE ANYBODY CAN REACH DOES NOT ANSWER "DOES THIS DATASET EXIST".
//
// CHECKPOINT B's F8. A failed dry run carries the warehouse's own words
// through, and that is deliberate: a syntax error is the one thing the person
// who typed the SQL needs back. On an AUTHENTICATED service it tells a caller
// nothing they could not read out of INFORMATION_SCHEMA themselves.
//
// With no token it is different in kind. `BREVIS_ENV=local` starts without
// one -- the banner says "anybody who reaches this port can read the
// warehouse" -- and on that service the message is a free existence oracle:
// point it at `acme-prod:payroll` and the warehouse says whether payroll is
// there, without any credential at all.
func TestAnOpenServiceDoesNotRepeatTheWarehousesWords(t *testing.T) {
	w := ask(t, priceless(t, ""), queryBody("SELECT * FROM payroll.salaries"))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	for _, leak := range []string{"payroll", "acme-prod", "Not found", "location US"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("an open service repeated %q: %s", leak, w.Body)
		}
	}
	// AND STILL SAYS WHAT HAPPENED. "The warehouse refused it" is not a
	// secret; which dataset exists is.
	if !strings.Contains(w.Body.String(), "would not run") {
		t.Errorf("the refusal says nothing at all: %s", w.Body)
	}
}

// A SERVICE WITH A TOKEN STILL ANSWERS PROPERLY, which is the half worth
// keeping: whoever holds the token can already SELECT from anything the
// credential reads, so withholding a syntax error from them buys nothing and
// costs them the one message they need.
func TestAnAuthenticatedServiceStillCarriesTheMessage(t *testing.T) {
	s := priceless(t, "a-token")
	r := httptest.NewRequest(http.MethodPost, "/v1/query",
		strings.NewReader(queryBody("SELECT * FROM payroll.salaries")))
	r.Header.Set("Authorization", "Bearer a-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if !strings.Contains(w.Body.String(), "Not found") {
		t.Errorf("an authenticated caller lost the warehouse's message: %s", w.Body)
	}
}
