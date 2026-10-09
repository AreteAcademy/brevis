package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// writable is a warehouse that answers whether its credential may write.
type writable struct {
	priced
	can   bool
	fail  error
	asked int
	mu    sync.Mutex
}

func (w *writable) CanWrite(context.Context, string) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.asked++
	if w.fail != nil {
		// TRUE *AND* AN ERROR, deliberately. A warehouse that answered
		// `false` beside its error would let a caller ignore the error and
		// still be right by accident -- which is how the rule "true is the
		// only answer you may trust" goes untested.
		return true, w.fail
	}
	return w.can, nil
}

func credentialService(t *testing.T, env string, c dialect.Conn, audit *bytes.Buffer) *Service {
	t.Helper()
	token := ""
	if env != EnvLocal {
		token = "s3cret"
	}
	s, err := New(Options{
		Env: env, Token: token, Rows: 100, Bytes: 10 << 30, Audit: audit,
		Open: func(context.Context, Table) (dialect.Conn, error) { return c, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func askAs(t *testing.T, s *Service, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if s.opt.Token != "" {
		r.Header.Set("Authorization", "Bearer "+s.opt.Token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// A CREDENTIAL THAT CAN WRITE MAKES EVERY OTHER LIMIT ON THIS SURFACE
// DECORATIVE, and it is the finding CHECKPOINT B ranked highest.
//
// The classifier is the only thing between a browser and a write, and its own
// comment says what it cannot do: a function call writes whatever the function
// writes. The read-only role is the defence there -- so a role nobody checked
// is a role nobody has.
func TestAWritableCredentialIsRefusedOutsideLocal(t *testing.T) {
	var audit bytes.Buffer
	w := &writable{priced: priced{estimate: 1}, can: true}
	res := askAs(t, credentialService(t, "production", w, &audit), "/v1/query", queryBody("SELECT 1"))

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("a writable credential answered %d", res.Code)
	}
	if w.ran != 0 {
		t.Error("the query ran on a credential that can write")
	}
	// Two lines: the finding itself, once per connection, and the query it
	// refused. Both have to name it, and both have to carry a stamp.
	lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d audit lines:\n%s", len(lines), audit.String())
	}
	seen := map[string]string{}
	for _, raw := range lines {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("not JSON: %s", raw)
		}
		if line["at"] == "" {
			t.Errorf("an audit line carries no time: %s", raw)
		}
		seen[line["event"].(string)], _ = line["outcome"].(string)
	}
	for _, event := range []string{"credential", "query"} {
		if seen[event] != "writable-credential" {
			t.Errorf("the %s line says %q", event, seen[event])
		}
	}
}

// LOCALLY IT IS SAID OUT LOUD AND ALLOWED. On a laptop the credential is the
// developer's own account, which can write everything; refusing there would
// make the tool unusable and teach somebody to turn the check off. It is the
// same line BREVIS_ENV already draws for the token.
func TestLocallyAWritableCredentialIsSaidAndAllowed(t *testing.T) {
	var audit bytes.Buffer
	w := &writable{priced: priced{estimate: 1}, can: true}
	res := askAs(t, credentialService(t, EnvLocal, w, &audit), "/v1/query", queryBody("SELECT 1"))

	if res.Code != http.StatusOK {
		t.Fatalf("locally a writable credential answered %d", res.Code)
	}
	if !strings.Contains(audit.String(), "writable-credential") {
		t.Errorf("nothing was said about it:\n%s", audit.String())
	}
}

// A READ-ONLY CREDENTIAL IS NOT REFUSED, which is the test that stops this
// from being a check that refuses everything.
func TestAReadOnlyCredentialRunsAndTheCheckSaysSo(t *testing.T) {
	var audit bytes.Buffer
	w := &writable{priced: priced{estimate: 1}, can: false}
	res := askAs(t, credentialService(t, "production", w, &audit), "/v1/query", queryBody("SELECT 1"))
	if res.Code != http.StatusOK {
		t.Fatalf("a read-only credential answered %d", res.Code)
	}
	// A CHECK THAT ONLY SPEAKS WHEN IT IS UNHAPPY cannot be told apart from
	// one that never ran, so the good answer is on the stream too.
	if !strings.Contains(audit.String(), `"outcome":"read-only"`) {
		t.Errorf("the check that passed said nothing:\n%s", audit.String())
	}
}

// AN ERROR IS NOT A YES, even when the yes is right there beside it.
// "Permission denied" is a no, "that dataset does not exist" is also a no,
// and they arrive the same way -- so a probe that could not run refuses
// nothing, and the value it returned alongside its error is not read.
func TestAProbeThatCouldNotRunRefusesNothing(t *testing.T) {
	w := &writable{priced: priced{estimate: 1}, fail: errors.New("bigquery: not found")}
	res := askAs(t, credentialService(t, "production", w, &bytes.Buffer{}), "/v1/query", queryBody("SELECT 1"))
	if res.Code != http.StatusOK {
		t.Fatalf("a probe that failed answered %d", res.Code)
	}
}

// A WAREHOUSE THAT CANNOT BE ASKED IS NOT REFUSED. Postgres has no dry run,
// so it implements none of this, and refusing it would be refusing a dialect
// for a question it was never asked.
func TestAWarehouseThatCannotBeProbedRuns(t *testing.T) {
	res := askAs(t, credentialService(t, "production", &priced{estimate: 1}, &bytes.Buffer{}),
		"/v1/query", queryBody("SELECT 1"))
	if res.Code != http.StatusOK {
		t.Fatalf("an unprobeable warehouse answered %d", res.Code)
	}
}

// ONCE PER CONNECTION, not once per query. A round trip per query for an
// answer that cannot change while the process lives is a round trip nobody
// gets anything for.
func TestTheCredentialIsProbedOncePerConnection(t *testing.T) {
	w := &writable{priced: priced{estimate: 1}, can: false}
	s := credentialService(t, "production", w, &bytes.Buffer{})
	for range 3 {
		askAs(t, s, "/v1/query", queryBody("SELECT 1"))
	}
	if w.asked != 1 {
		t.Errorf("the credential was probed %d times for one connection", w.asked)
	}
}

// AND THE PREVIEW IS BEHIND IT TOO. A writable credential is just as wrong
// where the statement is composed by this service, and `read` is one function
// for both endpoints precisely so a limit cannot be added to one of them.
func TestThePreviewIsBehindTheSameCheck(t *testing.T) {
	w := &writable{priced: priced{estimate: 1}, can: true}
	s := credentialService(t, "production", w, &bytes.Buffer{})
	rec := askAs(t, s, "/v1/preview", `{"target":"bigquery://acme-prod/bronze/orders"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("a preview on a writable credential answered %d", rec.Code)
	}
}

// "COULD NOT CHECK" IS NOT "CHECKED AND FINE", and writing one when the other
// happened is worse than saying nothing at all.
//
// Found by RUNNING it: the first real `serve` against the seeded catalog
// probed `demo-project`, which has BigQuery disabled, and the audit line said
// `"outcome":"read-only"`. An operator reading that would believe the
// credential had been examined and cleared. It had not been examined at all.
func TestAProbeThatFailedSaysSoRatherThanSayingItIsFine(t *testing.T) {
	var audit bytes.Buffer
	w := &writable{priced: priced{estimate: 1}, fail: errors.New("bigquery: not enabled")}
	res := askAs(t, credentialService(t, "production", w, &audit), "/v1/query", queryBody("SELECT 1"))

	if res.Code != http.StatusOK {
		t.Fatalf("a query on an unprobed credential answered %d", res.Code)
	}
	if strings.Contains(audit.String(), `"outcome":"read-only"`) {
		t.Errorf("a probe that failed reported a clean bill of health:\n%s", audit.String())
	}
	if !strings.Contains(audit.String(), `"outcome":"unprobed"`) {
		t.Errorf("the audit line does not say the check could not run:\n%s", audit.String())
	}
}

// A PROBE THAT FAILED IS TRIED AGAIN. Caching it would mean one transient
// failure turns the check off for the life of the process -- and the whole
// point of the check is that nobody notices when it is off.
func TestAFailedProbeIsNotRemembered(t *testing.T) {
	w := &writable{priced: priced{estimate: 1}, fail: errors.New("bigquery: not enabled")}
	s := credentialService(t, "production", w, &bytes.Buffer{})
	for range 3 {
		askAs(t, s, "/v1/query", queryBody("SELECT 1"))
	}
	if w.asked != 3 {
		t.Errorf("a probe that failed was retried %d times out of 3", w.asked)
	}
}
