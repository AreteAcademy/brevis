package config

import (
	"os"

	"github.com/AreteAcademy/brevis/internal/auth"
	"strings"
	"testing"
	"time"
)

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("BREVIS_DATABASE_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error when BREVIS_DATABASE_URL is missing; the process must not start with no database")
	}
}

func TestLoadAppliesTheDefaults(t *testing.T) {
	t.Setenv("BREVIS_DATABASE_URL", "postgres://u:p@localhost:5432/db")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Env != "local" {
		t.Errorf("Env = %q, wanted local", c.Env)
	}
	if c.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, wanted :8080", c.HTTPAddr)
	}
	if c.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, wanted 15s", c.ShutdownTimeout)
	}
}

func TestLoadRejectsAnInvalidTimeout(t *testing.T) {
	t.Setenv("BREVIS_DATABASE_URL", "postgres://u:p@localhost:5432/db")
	t.Setenv("BREVIS_SHUTDOWN_TIMEOUT_SECONDS", "quinze")

	if _, err := Load(); err == nil {
		t.Fatal("expected an error on a non-numeric timeout, rather than falling back to the default in silence")
	}
}

// The bug the user saw: the compose had GOOGLE_PROJECT_ID in the scheduler's
// environment, but dbt inside the task got "Env var required but not provided".
// The task does not inherit the environment -- what it needs is declared.
func TestTheTasksEnvironmentPassesOnWhatWasDeclared(t *testing.T) {
	t.Setenv("GOOGLE_PROJECT_ID", "acme-dev")
	t.Setenv("STAGE", "local")
	t.Setenv("BREVIS_DATABASE_URL", "postgres://brevis:senha@db/brevis")

	env := TasksEnvironment([]string{"GOOGLE_PROJECT_ID", "STAGE"})

	if env["GOOGLE_PROJECT_ID"] != "acme-dev" || env["STAGE"] != "local" {
		t.Errorf("it did not pass on what was declared: %v", env)
	}
	if _, leaked := env["BREVIS_DATABASE_URL"]; leaked {
		t.Error("the database credential reached the task")
	}
	if env["PATH"] == "" {
		t.Error("with no PATH no command resolves")
	}
}

// Inheriting by default would hand the database credential to every step of
// every pipeline -- a workflow is an arbitrary command written by somebody
// else.
func TestWithNoDeclarationOnlyPathAndHome(t *testing.T) {
	t.Setenv("SEGREDO_QUALQUER", "nao-deve-vazar")
	env := TasksEnvironment(nil)
	if len(env) != 2 {
		t.Errorf("ambiente = %v, want apenas PATH e HOME", env)
	}
}

func TestALiteralValueAndAMissingVariable(t *testing.T) {
	_ = os.Unsetenv("NAO_EXISTE")
	env := TasksEnvironment([]string{"STAGE=prod", "NAO_EXISTE"})

	if env["STAGE"] != "prod" {
		t.Errorf("the literal was not applied: %v", env)
	}
	// Absent does not become an empty string: `GOOGLE_PROJECT_ID=""` would make
	// dbt fail later, with a worse message than the missing-variable one.
	if _, existe := env["NAO_EXISTE"]; existe {
		t.Error("variavel ausente virou string vazia")
	}
}

// The wildcard exists for whoever needs it; the exception exists because the
// orchestrator's configuration is never the task's business.
func TestTheWildcardDoesNotCarryBrevisOwnVariables(t *testing.T) {
	t.Setenv("MINHA_VAR", "valor")
	t.Setenv("BREVIS_DATABASE_URL", "postgres://brevis:senha@db/brevis")
	t.Setenv("BREVIS_BRAND_FILE", "/etc/brevis/brand.yaml")

	env := TasksEnvironment([]string{"*"})

	if env["MINHA_VAR"] != "valor" {
		t.Error("curinga deveria repassar as variaveis comuns")
	}
	for k := range env {
		if strings.HasPrefix(k, "BREVIS_") {
			t.Errorf("the wildcard carried %s into the task", k)
		}
	}
}

// A field declared in the struct but never filled compiles and goes unnoticed --
// which is what happened with the webhook: the variable was in the container, the
// binary was the new one, and the alert simply did not go out. This test ties the
// environment to the field.
func TestLoadReadsTheEnvironmentForEveryField(t *testing.T) {
	t.Setenv("BREVIS_DATABASE_URL", "postgres://x/y")
	t.Setenv("BREVIS_SLACK_WEBHOOK", "https://hooks.slack.com/services/abc")
	t.Setenv("BREVIS_UI_URL", "https://brevis.example.com")
	t.Setenv("BREVIS_TASK_ENV", "GOOGLE_PROJECT_ID,STAGE")
	t.Setenv("BREVIS_POD_SERVICE_ACCOUNT", "brevis-task")
	t.Setenv("BREVIS_POD_TOLERATIONS", "kubernetes.io/arch=arm64:NoSchedule")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.SlackWebhook != "https://hooks.slack.com/services/abc" {
		t.Errorf("SlackWebhook = %q", c.SlackWebhook)
	}
	if c.UIURL != "https://brevis.example.com" {
		t.Errorf("UIURL = %q", c.UIURL)
	}
	if len(c.TaskEnv) != 2 {
		t.Errorf("TaskEnv = %v", c.TaskEnv)
	}
	if c.Pods.ServiceAccount != "brevis-task" {
		t.Errorf("ServiceAccount = %q", c.Pods.ServiceAccount)
	}
	if len(c.Pods.Tolerations) != 1 || c.Pods.Tolerations[0].Efeito != "NoSchedule" {
		t.Errorf("Toleracoes = %+v", c.Pods.Tolerations)
	}
}

// Outside local, starting with no credential is refused. The interface fires
// pipelines that write into the data warehouse; open on the internet it is a
// remote control for the warehouse. A warning in the log would not do -- nobody
// reads the log of a process that
// funciona.
func TestOutsideLocalACredentialIsRequired(t *testing.T) {
	t.Setenv("BREVIS_DATABASE_URL", "postgres://x/y")
	t.Setenv("BREVIS_ENV", "prod")

	if _, err := Load(); err == nil {
		t.Fatal("BREVIS_ENV=prod started with no credential")
	}

	h, err := auth.GenerateHash("senha-de-teste-longa")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BREVIS_AUTH_USER", "operator")
	t.Setenv("BREVIS_AUTH_PASSWORD_HASH", h)
	t.Setenv("BREVIS_AUTH_SECRET", "a-test-secret-with-more-than-32-bytes")

	c, err := Load()
	if err != nil {
		t.Fatalf("with a complete credential it should start: %v", err)
	}
	if !c.Auth.Enabled() {
		t.Error("the credential did not reach the Config")
	}
}

// In local the interface may stay open: there the server listens on the
// developer's own machine, and asking for a password on every `make up` would
// push the team into switching
// a autenticacao de vez.
func TestLocalStartsWithNoCredential(t *testing.T) {
	t.Setenv("BREVIS_DATABASE_URL", "postgres://x/y")
	t.Setenv("BREVIS_ENV", "local")
	if _, err := Load(); err != nil {
		t.Fatalf("local should start with no credential: %v", err)
	}
}

// The four variables renamed on 2026-09-10 accept both names.
//
// This is the test that has to exist for the rename to be safe. Outside `local`
// the engine refuses to boot without a credential, so an installation that had
// BREVIS_AUTH_USUARIO set and got only the new name read would CrashLoopBackOff
// on a rollout that changed nothing else -- and the manifest that was correct
// yesterday is what the error would blame.
func TestTheRenamedVariablesAcceptTheOldNameToo(t *testing.T) {
	h, err := auth.GenerateHash("a-long-enough-test-password")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("the old names alone still boot", func(t *testing.T) {
		t.Setenv("BREVIS_DATABASE_URL", "postgres://x/y")
		t.Setenv("BREVIS_ENV", "prod")
		t.Setenv("BREVIS_AUTH_USUARIO", "operator")
		t.Setenv("BREVIS_AUTH_SENHA_HASH", h)
		t.Setenv("BREVIS_AUTH_SEGREDO", "a-test-secret-with-more-than-32-bytes")
		t.Setenv("BREVIS_POD_MANTER_EM_FALHA", "true")

		c, err := Load()
		if err != nil {
			t.Fatalf("an installation on the old names stopped booting: %v", err)
		}
		if !c.Auth.Enabled() {
			t.Error("the credential did not reach the Config through the old names")
		}
		if !c.Pods.KeepOnFailure {
			t.Error("BREVIS_POD_MANTER_EM_FALHA no longer reaches KeepOnFailure")
		}
	})

	t.Run("the new name wins when both are set", func(t *testing.T) {
		t.Setenv("BREVIS_DATABASE_URL", "postgres://x/y")
		t.Setenv("BREVIS_ENV", "prod")
		t.Setenv("BREVIS_AUTH_USUARIO", "old")
		t.Setenv("BREVIS_AUTH_USER", "new")
		t.Setenv("BREVIS_AUTH_SENHA_HASH", h)
		t.Setenv("BREVIS_AUTH_SEGREDO", "a-test-secret-with-more-than-32-bytes")

		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		// Which one wins decides what a migration looks like: an operator adds
		// the new name, deploys, and removes the old one later. If the old name
		// won, that middle step would silently keep the old value.
		if c.Auth.User != "new" {
			t.Errorf("Auth.User = %q, want the new name to win", c.Auth.User)
		}
	})

	t.Run("neither set leaves it empty", func(t *testing.T) {
		t.Setenv("BREVIS_DATABASE_URL", "postgres://x/y")
		t.Setenv("BREVIS_ENV", "local")

		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.Auth.User != "" || c.Pods.KeepOnFailure {
			t.Error("a variable nobody set came back with a value")
		}
	})
}
