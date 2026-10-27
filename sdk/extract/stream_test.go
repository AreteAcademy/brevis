package extract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// A Stream reads the body itself, so DataKey would sit there unwrapping
// nothing. It is refused before a single request goes out, as it is with
// Records (#140).
func TestAStreamWithADataKeyIsRefusedBeforeAnyRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	_, err := Stream(context.Background(), core.Source{URL: srv.URL, DataKey: "data"},
		func(core.Response, func(any) error) error { return nil })
	if err == nil {
		t.Fatal("a Stream with a DataKey was accepted; DataKey would be ignored")
	}
	if !strings.Contains(err.Error(), "Stream") || !strings.Contains(err.Error(), "DataKey") {
		t.Errorf("the error does not name the two fields: %v", err)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("%d request(s) went out before the refusal", n)
	}
}
