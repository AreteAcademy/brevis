package core

import (
	"log/slog"
	"strings"
	"testing"
)

func coreLine(fields map[string]any) []Envelope {
	return []Envelope{{Payload: fields}}
}

func TestColumnsRefusesAColumnNobodyDelivered(t *testing.T) {
	err := CheckColumns(
		[]string{"ingestion_id", "provider", "entity", "payload"},
		coreLine(map[string]any{"ingestion_id": "x", "provider": "p", "payload": "{}"}),
	)
	if err == nil {
		t.Fatal("uma coluna declarada e não entregue landa NULL sem ninguém saber")
	}
	if !strings.Contains(err.Error(), "entity") {
		t.Errorf("o erro não nomeia a coluna: %v", err)
	}
	// And it says what the row actually has, so the fix comes out of one
	// reading.
	if !strings.Contains(err.Error(), "provider") {
		t.Errorf("o erro não lista o que a linha tem: %v", err)
	}
}

// Criterion 3: a field in the row the declaration does not list.
func TestColumnsRefusesAnUndeclaredField(t *testing.T) {
	err := CheckColumns(
		[]string{"provider", "payload"},
		coreLine(map[string]any{"provider": "p", "payload": "{}", "surpresa": 1}),
	)
	if err == nil {
		t.Fatal("um campo não declarado seria escrito numa tabela que nunca o mencionou")
	}
	if !strings.Contains(err.Error(), "surpresa") {
		t.Errorf("o erro não nomeia o campo: %v", err)
	}
}

// The Metadata block's two columns can be declared, and that is the spec's
// point:
// inside Transform they never could, because they do not exist there yet.
func TestColumnsAcceptsTheMetadataColumns(t *testing.T) {
	err := CheckColumns(
		[]string{"ingestion_id", "ingestion_loaded_at", "provider", "entity", "source_key", "payload"},
		coreLine(map[string]any{
			"ingestion_id": "u", "ingestion_loaded_at": "2026-01-01T00:00:00Z",
			"provider": "p", "entity": "e", "source_key": "k", "payload": "{}",
		}),
	)
	if err != nil {
		t.Fatalf("as seis colunas do DDL deveriam passar: %v", err)
	}
}

func TestResolvePrecedence(t *testing.T) {
	t.Setenv("BREVIS_TEST_X", "from-the-environment")

	if got := Resolve("explicit-value", "BREVIS_TEST_X", "default-value"); got.Value != "explicit-value" || got.Where != "explicit" {
		t.Errorf("o explícito tem de vencer: %+v", got)
	}
	if got := Resolve("", "BREVIS_TEST_X", "default-value"); got.Value != "from-the-environment" || got.Where != "BREVIS_TEST_X" {
		t.Errorf("o ambiente vem depois, e o log nomeia a variável: %+v", got)
	}
	if got := Resolve("", "BREVIS_TEST_ABSENT", "default-value"); got.Value != "default-value" || got.Where != "default" {
		t.Errorf("o padrão fecha a lista: %+v", got)
	}
}

func TestEnvIntFallsBackToTheDefaultInsteadOfBreaking(t *testing.T) {
	t.Setenv("BREVIS_TEST_N", "not-a-number")
	if got := EnvInt("BREVIS_TEST_N", 7); got != 7 {
		t.Errorf("um valor ilegível não pode derrubar a pipeline: %d", got)
	}
	t.Setenv("BREVIS_TEST_N", "42")
	if got := EnvInt("BREVIS_TEST_N", 7); got != 42 {
		t.Errorf("EnvInt = %d", got)
	}
}

// Does an invalid log level take the pipeline down? No: it warns and carries
// on.
func TestAnInvalidLogLevelDoesNotBringItDown(t *testing.T) {
	t.Setenv(EnvLogLevel, "nao-existe")
	if got := LogLevel(); got != slog.LevelInfo {
		t.Errorf("LogLevel = %v, esperado info", got)
	}
	t.Setenv(EnvLogLevel, "debug")
	if got := LogLevel(); got != slog.LevelDebug {
		t.Errorf("LogLevel = %v, esperado debug", got)
	}
}

// EnvIntRenamed accepts the name BREVIS_SDK_LIMITE_INLINE used to have.
//
// This one is a CONSUMER's variable: a fetcher that tuned BigQuery's inline
// threshold set it in its own deployment. Dropping the old name would move that
// threshold back to 5000 with nothing failing and the bill changing, which is
// why the fallback exists and why this test does.
func TestEnvIntRenamedAcceptsTheOldName(t *testing.T) {
	const newName, formerName = "BREVIS_TEST_INLINE_LIMIT", "BREVIS_TEST_LIMITE_INLINE"

	t.Run("the old name alone is read", func(t *testing.T) {
		t.Setenv(formerName, "120")
		if got := EnvIntRenamed(newName, formerName, 5000); got != 120 {
			t.Errorf("got %d, want the old name to still be read", got)
		}
	})

	t.Run("the new name wins over the old", func(t *testing.T) {
		t.Setenv(formerName, "120")
		t.Setenv(newName, "300")
		if got := EnvIntRenamed(newName, formerName, 5000); got != 300 {
			t.Errorf("got %d, want the new name to win", got)
		}
	})

	t.Run("neither set gives the default", func(t *testing.T) {
		if got := EnvIntRenamed(newName, formerName, 5000); got != 5000 {
			t.Errorf("got %d, want the default", got)
		}
	})

	t.Run("an unparseable new name does not fall back to the old", func(t *testing.T) {
		// Otherwise a typo in the new name would silently resurrect a value the
		// operator believes they replaced.
		t.Setenv(formerName, "120")
		t.Setenv(newName, "not-a-number")
		if got := EnvIntRenamed(newName, formerName, 5000); got != 5000 {
			t.Errorf("got %d, want the default rather than the old value", got)
		}
	})
}
