package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

func hashOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func guarded(t *testing.T, w *warehouse, keys string) (*Server, error) {
	t.Helper()
	t.Setenv("TEST_API_KEYS", keys)
	e := &Endpoint{
		Name: "clicks", Path: "/v1/clicks",
		SQL: "select k from t where w = {{ tenant }}",
	}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	return New(&Config{
		Name: "metrics-api", Env: "production",
		Listen:    Listen{Auth: &Auth{Type: "bearer", KeysFrom: "TEST_API_KEYS"}},
		Dialect:   postgres.Dialect{},
		Open:      func(context.Context) (dialect.Conn, error) { return w, nil },
		MaxRows:   10,
		Endpoints: []*Endpoint{e},
	})
}

func withKey(h http.Handler, target, key string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	h.ServeHTTP(rec, req)
	return rec
}

// THE TENANT IS THE KEY'S. Two keys, two tenants, and each request is
// bound to the tenant of the key that made it -- the caller never says
// which tenant it is, so it can never say a different one.
func TestEachKeyReadsAsItsOwnTenant(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	h, err := guarded(t, w, hashOf("key-for-acme-0123456789abcdef0123")+"=acme\n"+
		hashOf("key-for-globex-0123456789abcdef01")+"=globex\n")
	if err != nil {
		t.Fatal(err)
	}
	for key, tenant := range map[string]string{
		"key-for-acme-0123456789abcdef0123": "acme",
		"key-for-globex-0123456789abcdef01": "globex",
	} {
		w.asked = nil
		if rec := withKey(h, "/v1/clicks", key); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tenant, rec.Code, rec.Body)
		}
		if got := w.asked[0].Args; len(got) != 1 || got[0].Name != "tenant" || got[0].Value != tenant {
			t.Errorf("the key for %s bound %v", tenant, got)
		}
	}
}

// 401 FOR EVERYTHING BUT /health, unknown paths included: a 404 for an
// unknown path and a 401 for a known one is a map of the API drawn by
// status codes for whoever has no key.
func TestWithoutAKeyEverythingButHealthIs401(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	h, err := guarded(t, w, hashOf("a-key-long-enough-0123456789abcdef")+"=acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ target, key string }{
		{"/v1/clicks", ""}, {"/v1/clicks", "wrong"}, {"/v1/nothing", ""},
		{"/v1/clicks", hashOf("a-key-long-enough-0123456789abcdef")},
	} {
		if rec := withKey(h, c.target, c.key); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with %q answered %d", c.target, c.key, rec.Code)
		}
	}
	if rec := withKey(h, "/health", ""); rec.Code != http.StatusOK {
		t.Errorf("/health answered %d", rec.Code)
	}
	if len(w.asked) != 0 {
		t.Error("the warehouse was asked by a request with no valid key")
	}
}

// THE TENANT CANNOT BE SENT. `?tenant=globex` from acme's key is the
// attack, and it is refused before anything is rendered.
func TestATenantInTheQueryStringIsRefused(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	h, err := guarded(t, w, hashOf("a-key-long-enough-0123456789abcdef")+"=acme")
	if err != nil {
		t.Fatal(err)
	}
	rec := withKey(h, "/v1/clicks?tenant=globex", "a-key-long-enough-0123456789abcdef")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "key") {
		t.Errorf("answered %d %s", rec.Code, rec.Body)
	}
	if len(w.asked) != 0 {
		t.Error("the warehouse was asked")
	}
}

// REFUSED AT START, never at the first request: a key list nobody can use
// is an outage that a readiness probe would report as healthy.
func TestAKeyListThatCannotBeUsedRefusesToStart(t *testing.T) {
	h := hashOf("a-key-long-enough-0123456789abcdef")
	for name, c := range map[string]struct{ keys, says string }{
		"empty":             {"", "TEST_API_KEYS"},
		"no tenant":         {h, "line 1"},
		"empty tenant":      {h + "=", "line 1"},
		"not a hash":        {"abc=acme", "line 1"},
		"a key, not a hash": {"a-key-long-enough-0123456789abcdef=acme", "line 1"},
		"a hash twice":      {h + "=acme\n" + h + "=globex", "line 2"},
		"a bad tenant":      {h + "=ac me", "line 1"},
	} {
		_, err := guarded(t, &warehouse{}, c.keys)
		if err == nil {
			t.Errorf("%s: started", name)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %q does not say %q", name, err, c.says)
		}
		// THE LINE AND NEVER ITS CONTENT: a line of this variable is a
		// credential's hash, and an error ends up in a log.
		if strings.Contains(err.Error(), h) {
			t.Errorf("%s: the error quotes the hash", name)
		}
	}
}

// OUTSIDE BREVIS_ENV=local, NO AUTH IS NO START -- the gateway's rule,
// spelled the same way.
func TestOutsideLocalNoAuthRefusesToStart(t *testing.T) {
	_, err := New(&Config{Name: "x", Env: "production", Dialect: postgres.Dialect{}})
	if err == nil || !strings.Contains(err.Error(), "BREVIS_ENV=production requires `listen.auth`") {
		t.Errorf("started, or said %v", err)
	}
	_, err = New(&Config{Name: "x", Env: "", Dialect: postgres.Dialect{}})
	if err == nil {
		t.Error("an unset BREVIS_ENV started without auth")
	}
}

// LOCALLY, OPEN, AND THE TENANT IS "local": asking for a key on every
// laptop run only teaches a team to turn authentication off.
func TestLocallyWithoutAuthTheTenantIsLocal(t *testing.T) {
	w := &warehouse{cols: []string{"k"}}
	e := &Endpoint{Name: "clicks", Path: "/v1/clicks", SQL: "select k from t where w = {{ tenant }}"}
	if err := e.parse(postgres.Dialect{}); err != nil {
		t.Fatal(err)
	}
	h, err := New(&Config{Name: "x", Env: "local", Dialect: postgres.Dialect{}, MaxRows: 10,
		Open:      func(context.Context) (dialect.Conn, error) { return w, nil },
		Endpoints: []*Endpoint{e}})
	if err != nil {
		t.Fatal(err)
	}
	if rec := withKey(h, "/v1/clicks", ""); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := w.asked[0].Args[0].Value; got != "local" {
		t.Errorf("tenant bound as %v", got)
	}
}

func TestHashingAKeyPrintsItsLine(t *testing.T) {
	line, err := KeyLine("a-key-long-enough-0123456789abcdef", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if line != hashOf("a-key-long-enough-0123456789abcdef")+"=acme" {
		t.Errorf("line %q", line)
	}
	if _, err := KeyLine("short", "acme"); err == nil {
		t.Error("a 5-character key was accepted")
	}
	// WHITESPACE IS REFUSED, found in review: the guard trims a presented
	// key, so a key with a space at either end hashed as written could
	// never authenticate.
	for _, k := range []string{" a-key-long-enough-0123456789abcdef", "a-key-long-enough-0123456789abcdef\t", "a-key long-enough-0123456789abcdef"} {
		if _, err := KeyLine(k, "acme"); err == nil {
			t.Errorf("%q was hashed", k)
		}
	}
	if _, err := KeyLine("a-key-long-enough-0123456789abcdef", "ac me"); err == nil {
		t.Error("a tenant with a space was accepted")
	}
}
