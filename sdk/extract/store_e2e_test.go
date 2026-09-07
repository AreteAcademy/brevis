package extract

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestTheSecondRunUsesWhatCameFromTheVolume e a prova do §7.13 da spec: rodar
// twice with the seed REMOVED after the first, and the second authenticating
// with what came out of the store.
//
// It is literally what the consumer asked for -- to stop re-pasting the cookie
// once per
// janela.
func TestTheSecondRunUsesWhatCameFromTheVolume(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(core.EnvCredentialDir, dir)
	t.Setenv(core.EnvCredentialKey, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")

	var mu sync.Mutex
	var seenValues []string
	generation := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session")
		mu.Lock()
		if err == nil {
			seenValues = append(seenValues, c.Value)
		}
		mu.Unlock()
		if err != nil {
			_, _ = fmt.Fprint(w, `null`)
			return
		}
		generation++
		http.SetCookie(w, &http.Cookie{Name: "session", Value: fmt.Sprintf("rotacionado-%d", generation)})
		_, _ = fmt.Fprintf(w, `{"expires":%q}`, time.Now().Add(30*24*time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/api/proxy/occurrences", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	runIt := func(seed string) error {
		source := core.Source{
			URL: srv.URL + "/api/proxy/occurrences",
			Auth: &core.Credential{
				Value: func(context.Context) (string, error) {
					if seed == "" {
						return "", fmt.Errorf("GABRIEL_SESSION_COOKIE nao esta definida")
					}
					return "session=" + seed, nil
				},
				Apply: core.AsCookie,
				Refresh: &core.Refresh{
					URL:       srv.URL + "/api/auth/session",
					ExpiresAt: core.JSONField("expires"),
					Store:     core.FileStore{Name: "gabriel-session"},
				},
			},
		}
		seq, err := JSON(context.Background(), source, nil)
		if err != nil {
			return err
		}
		for _, err := range seq {
			if err != nil {
				return err
			}
		}
		return nil
	}

	// First: the seed exists, and the rotation is written.
	if err := runIt("colado-pelo-humano"); err != nil {
		t.Fatalf("primeira execucao: %v", err)
	}

	// Second: the seed is GONE. Only the volume has a credential.
	if err := runIt(""); err != nil {
		t.Fatalf("segunda execucao, sem a semente: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenValues) != 2 {
		t.Fatalf("a renovacao recebeu credencial %d vezes, esperado 2: %v", len(seenValues), seenValues)
	}
	if seenValues[0] != "colado-pelo-humano" {
		t.Errorf("a primeira execucao usou %q", seenValues[0])
	}
	if seenValues[1] != "rotacionado-1" {
		t.Errorf("a segunda execucao usou %q; esperava o valor que a primeira gravou", seenValues[1])
	}
}

// TestAFailedWriteDoesNotStopTheRun: the load already happened; what was lost is
// the rotation. But it has to shout, and in Stats -- not only in the log.
func TestAFailedWriteDoesNotStopTheRun(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "novo"})
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	mux.HandleFunc("/dados", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var stats core.Stats
	seq, err := JSON(context.Background(), core.Source{
		URL:   srv.URL + "/dados",
		Stats: &stats,
		Auth: &core.Credential{
			Value: func(context.Context) (string, error) { return "session=velho", nil },
			Apply: core.AsCookie,
			Refresh: &core.Refresh{
				URL:   srv.URL + "/auth",
				Store: storeQueNaoGrava{},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("a execucao morreu por causa da escrita: %v", err)
	}
	lines := 0
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
		lines++
	}
	if lines != 1 {
		t.Errorf("linhas = %d; a extracao devia ter acontecido", lines)
	}
	if stats.CredentialStoreError == "" {
		t.Error("a falha de escrita nao chegou a Stats; so o log a teria")
	}
}

type storeQueNaoGrava struct{}

func (storeQueNaoGrava) Load() (string, error) { return "", nil }
func (storeQueNaoGrava) Save(string) error     { return fmt.Errorf("disco cheio") }
func (storeQueNaoGrava) Describe() string      { return "store de teste" }

// TestTheCredentialNeverAppearsInALog is §7.8 of the spec. A value that leaks
// into the log leaks into the log aggregator, which plenty of people read -- and
// it would be repeating, somewhere else, the mistake of keeping a credential
// where one is not kept.
func TestTheCredentialNeverAppearsInALog(t *testing.T) {
	const secret = "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2R0NNIn0..MUlTVFJP"
	const rotated = "ROTACIONADO-eyJhbGciOiJkaXI"

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(core.EnvCredentialDir, dir)
	t.Setenv(core.EnvCredentialKey, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/session", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: rotated})
		_, _ = fmt.Fprintf(w, `{"expires":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	})
	mux.HandleFunc("/api/proxy/dados", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ok":1}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var buf bytes.Buffer
	previous := slog.Default()
	// Debug: se algo vazasse so no nivel mais baixo, o teste tem de ver.
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	seq, err := JSON(context.Background(), core.Source{
		URL: srv.URL + "/api/proxy/dados",
		Auth: &core.Credential{
			Value: func(context.Context) (string, error) { return "session=" + secret, nil },
			Apply: core.AsCookie,
			Refresh: &core.Refresh{
				URL:       srv.URL + "/api/auth/session",
				ExpiresAt: core.JSONField("expires"),
				Store:     core.FileStore{Name: "gabriel-session"},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, err := range seq {
		if err != nil {
			t.Fatalf("iterando: %v", err)
		}
	}

	log := buf.String()
	for name, value := range map[string]string{"the seed": secret, "the rotated one": rotated} {
		if strings.Contains(log, value) {
			t.Errorf("%s leaked into the log:\n%s", name, log)
		}
		// Not even truncated: a prefix of a credential is still part of one.
		if strings.Contains(log, value[:16]) {
			t.Errorf("%s leaked into the log, truncated:\n%s", name, log)
		}
	}
}
