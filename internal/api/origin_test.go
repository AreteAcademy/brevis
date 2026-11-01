package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCrossSiteGuardAndHeaders(t *testing.T) {
	s := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	s.mux.HandleFunc("POST /workflows/x/trigger", func(http.ResponseWriter, *http.Request) {})
	s.mux.HandleFunc("GET /data/endpoints/a/b", func(http.ResponseWriter, *http.Request) {})

	cases := []struct {
		name, method, path string
		hdr                map[string]string
		want               int
	}{
		{"cross-site POST", "POST", "/workflows/x/trigger", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"same-site POST", "POST", "/workflows/x/trigger", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		{"foreign Origin POST", "POST", "/workflows/x/trigger", map[string]string{"Origin": "http://evil.example"}, 403},
		{"same-origin POST", "POST", "/workflows/x/trigger", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://example.com"}, 200},
		{"same Origin POST", "POST", "/workflows/x/trigger", map[string]string{"Origin": "http://example.com"}, 200},
		{"no headers POST", "POST", "/workflows/x/trigger", nil, 200},
		{"cross-site GET run=1", "GET", "/data/endpoints/a/b?run=1", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"cross-site GET page", "GET", "/data/endpoints/a/b", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
		{"same-origin GET run=1", "GET", "/data/endpoints/a/b?run=1", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
		{"no headers GET run=1", "GET", "/data/endpoints/a/b?run=1", nil, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, c.path, nil) // Host: example.com
			for k, v := range c.hdr {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d", w.Code, c.want)
			}
			for k, v := range map[string]string{"X-Frame-Options": "DENY", "Referrer-Policy": "same-origin", "X-Content-Type-Options": "nosniff"} {
				if got := w.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
		})
	}
}
