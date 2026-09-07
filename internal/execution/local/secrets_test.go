package local

import (
	"os"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// TestALocalSecretComesFromTheEnginesEnvironment: em Kubernetes a coordenada aponta um
// Secret; in local there is no Secret at all, so the engine reads the variable of
// the same
// nome do proprio ambiente.
func TestALocalSecretComesFromTheEnginesEnvironment(t *testing.T) {
	t.Setenv("GABRIEL_SESSION_COOKIE", "session=abc==")

	env, err := ambienteDaTask(execution.TaskExec{
		NodeID:  "fetch_occurrences",
		Env:     map[string]string{"BREVIS_LOG_LEVEL": "info"},
		Secrets: map[string]string{"GABRIEL_SESSION_COOKIE": "gabriel-session/cookie"},
	})
	if err != nil {
		t.Fatalf("ambienteDaTask: %v", err)
	}
	if !contains(env, "GABRIEL_SESSION_COOKIE=session=abc==") {
		t.Errorf("the secret did not reach the process: %v", env)
	}
	if !contains(env, "BREVIS_LOG_LEVEL=info") {
		t.Errorf("o env literal sumiu: %v", env)
	}
}

// TestAMissingSecretFailsBeforeRunning: string vazia viraria um cookie vazio e
// a 401 further down, blaming the API for a variable nobody exported.
func TestAMissingSecretFailsBeforeRunning(t *testing.T) {
	casos := map[string]func(*testing.T){
		// Setenv registra o cleanup; Unsetenv logo depois deixa a variavel
		// genuinely absent and the original value comes back at the end of the
		// test.
		"not set": func(t *testing.T) {
			t.Setenv("GABRIEL_SESSION_COOKIE", "x")
			if err := os.Unsetenv("GABRIEL_SESSION_COOKIE"); err != nil {
				t.Fatal(err)
			}
		},
		"vazia": func(t *testing.T) { t.Setenv("GABRIEL_SESSION_COOKIE", "") },
	}
	for name, preparar := range casos {
		t.Run(name, func(t *testing.T) {
			preparar(t)

			_, err := ambienteDaTask(execution.TaskExec{
				NodeID:  "fetch_occurrences",
				Secrets: map[string]string{"GABRIEL_SESSION_COOKIE": "gabriel-session/cookie"},
			})
			if err == nil {
				t.Fatal("a missing secret got through")
			}
			for _, exigido := range []string{"GABRIEL_SESSION_COOKIE", "fetch_occurrences", "gabriel-session/cookie"} {
				if !strings.Contains(err.Error(), exigido) {
					t.Errorf("the error does not say %q: %v", exigido, err)
				}
			}
		})
	}
}

// TestATaskDoesNotInheritTheEnginesEnvironmentByAccident: the rule that already
// existed. `secrets:` is the by-name opt-in against it, not an open gate.
func TestATaskDoesNotInheritTheEnginesEnvironmentByAccident(t *testing.T) {
	t.Setenv("SEGREDO_DO_ORQUESTRADOR", "should not leak")

	env, err := ambienteDaTask(execution.TaskExec{NodeID: "a", Env: map[string]string{"OK": "1"}})
	if err != nil {
		t.Fatalf("ambienteDaTask: %v", err)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "SEGREDO_DO_ORQUESTRADOR=") {
			t.Errorf("the task inherited the engine's environment: %v", env)
		}
	}
}

// TestASecretOverridesTheLiteral: if both exist on the same task, the secret
// wins -- but the domain already refuses the collision, so this only pins down
// that the order is not accidental.
func TestASecretOverridesTheLiteral(t *testing.T) {
	t.Setenv("TOKEN", "do-ambiente")

	env, err := ambienteDaTask(execution.TaskExec{
		NodeID:  "a",
		Env:     map[string]string{"TOKEN": "literal"},
		Secrets: map[string]string{"TOKEN": "cofre/token"},
	})
	if err != nil {
		t.Fatalf("ambienteDaTask: %v", err)
	}
	if !contains(env, "TOKEN=do-ambiente") {
		t.Errorf("the literal beat the secret: %v", env)
	}
}

func contains(env []string, procurado string) bool {
	for _, kv := range env {
		if kv == procurado {
			return true
		}
	}
	return false
}
