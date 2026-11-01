package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/AreteAcademy/brevis/internal/auth"
)

// Concurrent logins beyond the hash slots are refused at once with a 429, and
// do not run the (0.2 s, CPU-bound) hash: that is what kept /health from
// answering. A repeating wrong password from one address ends up locked out.
func TestLoginDoesNotRunTheHashBeyondTheSlotsNorForALockedSource(t *testing.T) {
	var calls atomic.Int32
	entered, release := make(chan struct{}, 16), make(chan struct{})
	gate := &auth.Gate{
		Cred: auth.Credential{User: "op", Hash: "x"},
		Check: func(_, _ string) bool {
			calls.Add(1)
			entered <- struct{}{}
			<-release
			return false
		},
	}
	mux := http.NewServeMux()
	dataUI(catalogFake{}).RegistrarLogin(mux, gate)

	post := func(addr string) int {
		req := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"username": {"nobody"}, "password": {"secret-typed-in-the-wrong-box"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "nobody") && rec.Code == http.StatusTooManyRequests {
			t.Error("the throttled page echoes the username")
		}
		return rec.Code
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); post("10.0.0.1:1000") }()
		<-entered
	}
	for i := 0; i < 20; i++ {
		if code := post("10.0.0.2:1000"); code != http.StatusTooManyRequests {
			t.Fatalf("excess attempt = %d; expected 429", code)
		}
	}
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 2 {
		t.Errorf("the hash ran %d times; expected 2 (the slots)", n)
	}

	// Past the free failures the same source is locked, with no hash run.
	for i := 0; i < 5; i++ {
		post("10.0.0.3:1000")
	}
	n := calls.Load()
	if code := post("10.0.0.3:2000"); code != http.StatusTooManyRequests || calls.Load() != n {
		t.Errorf("locked source: status %d, hashes %d -> %d", code, n, calls.Load())
	}
}
