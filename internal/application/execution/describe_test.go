package execution

import (
	"strings"
	"testing"
)

const describedLine = `@brevis:{"type":"pipeline","v":1,"fields":[` +
	`{"path":"source.driver","value":"from.HTTP"},` +
	`{"path":"source.host","value":"api.example.com"},` +
	`{"path":"transform.1.map","value":["main.normalise"]},` +
	`{"path":"target.driver","value":"bigquery.Table"},` +
	`{"path":"target.dedup","value":"merge"}]}`

// The pipeline's description is a marker like any other: consumed, never in
// the step's log, and kept with the hash the ENGINE computed.
func TestAPipelinesDescriptionIsConsumedAndKept(t *testing.T) {
	var c stageCollector
	if !c.line(describedLine) {
		t.Fatal("the description reached the step's log")
	}
	cfg := c.Config()
	if cfg == nil || !strings.HasPrefix(cfg.Hash, "sha256:") || len(cfg.Body) == 0 {
		t.Fatalf("config = %+v", cfg)
	}
	if c.ConfigRefused != "" {
		t.Fatalf("refused: %s", c.ConfigRefused)
	}
}

// It does not spend the phases' budget: a pipeline that describes itself
// must not push its own phases off the screen.
func TestADescriptionDoesNotSpendThePhasesBudget(t *testing.T) {
	var c stageCollector
	c.line(describedLine)
	if c.seen != 0 {
		t.Fatalf("seen = %d, want 0", c.seen)
	}
}

// A description carrying a credential is refused whole, and says why -- the
// second wall, behind the SDK's allowlist.
func TestADescriptionCarryingACredentialIsRefused(t *testing.T) {
	var c stageCollector
	line := strings.Replace(describedLine, `"api.example.com"`, `"https://u:hunter2@api.example.com"`, 1)
	if !c.line(line) {
		t.Fatal("a refused description reached the step's log")
	}
	if c.Config() != nil {
		t.Fatal("a description with a password was kept")
	}
	if !strings.Contains(c.ConfigRefused, "source.host") {
		t.Fatalf("refused = %q, want it to name the setting", c.ConfigRefused)
	}
}

func TestAnOversizedDescriptionIsRefused(t *testing.T) {
	var c stageCollector
	line := strings.Replace(describedLine, `"merge"`, `"`+strings.Repeat("x", describeCeiling)+`"`, 1)
	c.line(line)
	if c.Config() != nil || !strings.Contains(c.ConfigRefused, "large") {
		t.Fatalf("config = %+v, refused = %q", c.Config(), c.ConfigRefused)
	}
}

func TestADescriptionWithNoFieldsIsRefused(t *testing.T) {
	var c stageCollector
	c.line(`@brevis:{"type":"pipeline","v":1,"fields":{"not":"a list"}}`)
	if c.Config() != nil || c.ConfigRefused == "" {
		t.Fatalf("config = %+v, refused = %q", c.Config(), c.ConfigRefused)
	}
}

// A setting's NAME is checked as its value is -- an SDK user's own
// Configuration picks its keys -- and the refusal, which the page shows,
// does not repeat what it refused.
func TestACredentialInASettingsNameIsRefusedWithoutRepeatingIt(t *testing.T) {
	var c stageCollector
	c.line(strings.Replace(describedLine, `"source.host"`, `"postgres://loader:hunter2@db/shop"`, 1))
	if c.Config() != nil {
		t.Fatal("a description with a password in a name was kept")
	}
	if c.ConfigRefused == "" || strings.Contains(c.ConfigRefused, "hunter2") {
		t.Fatalf("refused = %q, want a reason that does not repeat the credential", c.ConfigRefused)
	}
}
