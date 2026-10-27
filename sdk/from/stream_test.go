package from_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

// What a fetcher compares emit's error with is sdk.ErrStopped, so it can tell
// "nobody is reading" from a failure while it closes up.
func TestAFetcherLearnsOfTheStopAsSdkErrStopped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("rows"))
	}))
	defer srv.Close()

	var stop error
	seq, err := from.HTTP{URL: srv.URL,
		Stream: func(r sdk.Response, emit func(any) error) error {
			for {
				if err := emit("row"); err != nil {
					stop = err
					return err
				}
			}
		}}.Read(context.Background(), sdk.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for range seq {
		break
	}
	if !errors.Is(stop, sdk.ErrStopped) {
		t.Errorf("emit returned %v after the break, want sdk.ErrStopped", stop)
	}
}
