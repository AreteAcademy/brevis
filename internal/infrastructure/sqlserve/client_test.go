package sqlserve_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

func serving(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

// The happy path, and the two things the engine actually needs from it.
func TestAPreviewComesBackAsColumnsAndRows(t *testing.T) {
	var gotAuth, gotBody, gotMethod, gotPath string
	s := serving(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMethod, gotPath = r.Header.Get("Authorization"), r.Method, r.URL.Path
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"columns":["k","v"],"rows":[[1,"a"],[2,null]],"truncated":true,"limit":3}`))
	})

	c := sqlserve.New(s.URL, "t0ken")
	got, err := c.Preview(context.Background(), "bigquery://acme-prod/bronze/orders", 3)
	if err != nil {
		t.Fatal(err)
	}

	if gotMethod != http.MethodPost || gotPath != "/v1/preview" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer t0ken" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	// THE TARGET TRAVELS IN THE BODY. A target in a URL is a target in a
	// proxy log, a browser's history and a Referer header.
	if !strings.Contains(gotBody, "bigquery://acme-prod/bronze/orders") {
		t.Errorf("body = %q", gotBody)
	}
	if len(got.Columns) != 2 || len(got.Rows) != 2 || !got.Truncated {
		t.Fatalf("%+v", got)
	}
	// NULL SURVIVES to the page, which is where it gets drawn differently
	// from an empty string.
	if got.Rows[1][1] != nil {
		t.Errorf("a NULL arrived as %#v", got.Rows[1][1])
	}
}

// A REFUSAL FROM serve IS SHOWN, because it is the sentence somebody needs:
// "that target is not a table", "no connection for it". It was written to be
// read by a person and it already refuses to echo its input.
func TestServesRefusalIsCarriedThrough(t *testing.T) {
	s := serving(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"this target cannot be previewed: only BigQuery destinations can be read today"}`))
	})
	_, err := sqlserve.New(s.URL, "").Preview(context.Background(), "postgres://db/s/t", 3)
	if err == nil {
		t.Fatal("a 400 came back as success")
	}
	if !strings.Contains(err.Error(), "only BigQuery destinations") {
		t.Errorf("the reason was lost: %v", err)
	}
}

// AND EVERY OTHER FAILURE IS NOT. A 401 means the console's token is wrong,
// which is an operator's problem and not a reader's; a 500 is the service's
// business. Repeating either to a browser says something about a service the
// reader cannot reach and should not learn about.
func TestOtherFailuresDoNotLeakTheServicesWords(t *testing.T) {
	for _, c := range []struct {
		code          int
		body, wantNot string
	}{
		{http.StatusUnauthorized, `{"error":"a valid bearer token is required"}`, "bearer"},
		{http.StatusInternalServerError, "panic: runtime error at 0xdeadbeef", "0xdeadbeef"},
		{http.StatusBadGateway, `{"error":"dial tcp 10.0.3.7:5432: refused"}`, "10.0.3.7"},
	} {
		s := serving(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
			_, _ = w.Write([]byte(c.body))
		})
		_, err := sqlserve.New(s.URL, "").Preview(context.Background(), "bigquery://acme-prod/b/t", 3)
		if err == nil {
			t.Fatalf("%d came back as success", c.code)
		}
		if strings.Contains(strings.ToLower(err.Error()), c.wantNot) {
			t.Errorf("%d leaked %q: %v", c.code, c.wantNot, err)
		}
	}
}

// A SERVICE THAT IS NOT THERE IS A SENTENCE, not a stack trace. The page has
// to render with a reason where the grid would be, so the error has to be one
// somebody can read -- and must not carry the address, which is internal.
func TestAnUnreachableServiceIsReadable(t *testing.T) {
	// A port nothing listens on.
	_, err := sqlserve.New("http://127.0.0.1:1", "").Preview(
		context.Background(), "bigquery://acme-prod/b/t", 3)
	if err == nil {
		t.Fatal("an unreachable service came back as success")
	}
	if strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("the address reached the message: %v", err)
	}
}

// IT DOES NOT WAIT FOREVER. A console request blocked on a warehouse is a
// browser tab that hangs, and the first outbound call this engine makes is
// where that rule gets set.
func TestItGivesUp(t *testing.T) {
	// BOUNDED ON BOTH SIDES. The first version blocked on
	// `<-r.Context().Done()` alone, and `httptest.Server.Close` waits for
	// outstanding handlers -- so the test hung for 25 s and failed on its own
	// timeout rather than on the thing it measures. A handler that always
	// finishes keeps the failure about the client.
	s := serving(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	c := sqlserve.New(s.URL, "")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { _, err := c.Preview(ctx, "bigquery://acme-prod/b/t", 3); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a hanging service came back as success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("it waited past its own context")
	}
}
