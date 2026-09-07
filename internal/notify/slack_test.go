package notify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/notify"
)

func capture(t *testing.T, status int, resposta string) (*notify.Slack, *string) {
	t.Helper()
	var recebido string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		recebido = string(b)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resposta))
	}))
	t.Cleanup(srv.Close)

	s := notify.NovoSlack(srv.URL, "prod")
	return s, &recebido
}

func alert() notify.Alerta {
	quando := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	return notify.Alerta{
		Workflow: "id_verification", RunID: "1f2e3d4c-0000-0000-0000-000000000000",
		Status: "failed", Trigger: "schedule", Tentativas: 3, LogicalDate: &quando,
		Err:     "level 1: step \"run\": exited with code 2\nDatabase Error in model x",
		Tags:    []string{"acme", "id", "dbt"},
		URLBase: "https://brevis.example.com",
	}
}

func TestTheMessageCarriesTheFailuresContext(t *testing.T) {
	s, recebido := capture(t, 200, "ok")
	if err := s.Falhou(context.Background(), alert()); err != nil {
		t.Fatal(err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(*recebido), &payload); err != nil {
		t.Fatalf("the payload is not JSON: %v", err)
	}

	// `text` outside the blocks is what shows in the phone's notification; without
	// it
	// o Slack mostra "This content can't be displayed" no preview.
	texto, _ := payload["text"].(string)
	if !strings.Contains(texto, "id_verification") {
		t.Errorf("texto de preview = %q", texto)
	}

	corpo := *recebido
	for _, esperado := range []string{
		"id_verification",                  // pipeline
		"`id`",                             // domain, coming from the tags
		"FAILED",                           // status
		"schedule",                         // origin
		"exited with code 2",               // a causa
		"brevis.example.com/runs/1f2e3d4c", // link direto
		expectedLogicalDate(),              // data logica, no fuso de quem formata
	} {
		if !strings.Contains(corpo, esperado) {
			t.Errorf("the message is missing %q:\n%s", esperado, corpo)
		}
	}
}

// The error message carries stderr; Slack's block has a 3000-character limit and
// REFUSES the whole message when it overflows -- truncating is what makes sure
// the alert arrives.
func TestALongErrorIsTruncated(t *testing.T) {
	s, recebido := capture(t, 200, "ok")
	a := alert()
	a.Err = strings.Repeat("a very long stack-trace line ", 200)

	if err := s.Falhou(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if len(*recebido) > 3000 {
		t.Errorf("a payload of %d bytes; Slack's block would refuse it", len(*recebido))
	}
	if !strings.Contains(*recebido, "truncado") {
		t.Error("it truncated without saying it truncated")
	}
}

// The environment in the header: a staging alert at three in the morning must not
// be
// indistinguivel de um de producao.
func TestTheEnvironmentShowsInTheHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "(dev)") {
			t.Errorf("the header has no environment: %s", b)
		}
	}))
	defer srv.Close()

	s := notify.NovoSlack(srv.URL, "dev")
	if err := s.Falhou(context.Background(), alert()); err != nil {
		t.Fatal(err)
	}
}

// Slack answers plain text on an error ("invalid_payload"), not JSON. Passing the
// body along tells a revoked webhook from a malformed payload without opening a
// browser.
func TestASlackErrorCarriesTheReason(t *testing.T) {
	s, _ := capture(t, 403, "invalid_token")
	err := s.Falhou(context.Background(), alert())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "invalid_token") {
		t.Errorf("the error has no reason from Slack: %v", err)
	}
}

// With no webhook configured it is not an error: the installation simply does not
// alert.
func TestWithNoWebhookItDoesNothing(t *testing.T) {
	if err := (&notify.Slack{}).Falhou(context.Background(), alert()); err != nil {
		t.Errorf("an installation with no webhook became an error: %v", err)
	}
}

// Sem tags, o dominio sai do prefixo do slug em vez de ficar anonimo.
func TestTheDomainFallsBackToTheSlugsPrefix(t *testing.T) {
	s, recebido := capture(t, 200, "ok")
	a := alert()
	a.Tags = nil
	a.Workflow = "platform_workspace"

	if err := s.Falhou(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*recebido, "`platform`") {
		t.Errorf("the domain was not derived from the slug:\n%s", *recebido)
	}
}

// expectedLogicalDate rende a data do alert no fuso do PROCESSO.
//
// Pinning "01/09/2026 01:00" tied the test to UTC-3: it passed on the machine of
// whoever wrote it and failed in CI, which runs in UTC -- and that is how it took
// the v0.4.0 release down, on the one gate nobody had exercised.
func expectedLogicalDate() string {
	return time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC).Local().Format("02/01/2006 15:04")
}

// The time alone is ambiguous, and the ambiguity is not theoretical: the pod
// formats in UTC and whoever reads it is in UTC-3. Without the zone, the same
// event is "01:00" to one and "04:00" to the other, and nobody notices they are
// talking about the same failure.
func TestTheLogicalDateSaysTheZone(t *testing.T) {
	s, recebido := capture(t, 200, "ok")
	if err := s.Falhou(context.Background(), alert()); err != nil {
		t.Fatal(err)
	}

	fuso := time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC).Local().Format("MST")
	if !strings.Contains(*recebido, expectedLogicalDate()+" "+fuso) {
		t.Errorf("the logical date came out with no zone %q:\n%s", fuso, *recebido)
	}
}
