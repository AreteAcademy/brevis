package gateway_test

import (
	"os"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
)

const valid = `
name: g
streams:
  - name: clicks
    path: /v1/clicks
    identity: {provider: web, entity: click, source_key: event_id, record_ts: occurred_at}
    sink: {type: pubsub, project: p, topic: t}
    dead_letter: {type: files, path: ./dead/}
`

// Every refusal this file makes, and why each one is worth a test: a gateway
// that starts on a config it half understood drops events for a reason nobody
// can see.
func TestTheConfigRefusesWhatWouldFailSilently(t *testing.T) {
	for _, c := range []struct {
		name string
		yaml string
		says string
	}{
		{
			// The one that matters most. Somebody writing `disk` believes their
			// events survive a crash; accepting the word and behaving like
			// memory is the failure this field exists to prevent.
			name: "a durability tier that does not exist yet",
			yaml: strings.Replace(valid, "    sink:", "    buffer: {durability: disk}\n    sink:", 1),
			says: "only \"memory\" is implemented",
		},
		{
			name: "a sink nobody implemented",
			yaml: strings.Replace(valid, "type: pubsub", "type: kafka", 1),
			says: "only pubsub and files are implemented",
		},
		{
			// The formula is frozen over exactly four fields, so three of them
			// is a DIFFERENT id, not a weaker one.
			name: "an identity missing one of the four",
			yaml: strings.Replace(valid, ", record_ts: occurred_at", "", 1),
			says: "record_ts",
		},
		{
			// Two streams on one path is a silent winner, decided by the order
			// of a map somewhere.
			name: "two streams on one path",
			yaml: valid + `  - name: other
    path: /v1/clicks
    identity: {provider: w, entity: e, source_key: k, record_ts: t}
    sink: {type: pubsub, project: p, topic: t}
    dead_letter: {type: files, path: ./dead/}
`,
			says: "both listen on",
		},
		{
			name: "a format that would have to be guessed",
			yaml: strings.Replace(valid, "    sink:", "    format: csv\n    sink:", 1),
			says: "use json, array or ndjson",
		},
		{
			// A typo in a key is a setting that silently does nothing, and in
			// this file that means a durability or a flush the operator
			// believes is in force.
			name: "a key that is a typo",
			yaml: strings.Replace(valid, "    sink:", "    hokk: enrich\n    sink:", 1),
			says: "hokk",
		},
		{
			name: "a path that is not one",
			yaml: strings.Replace(valid, "path: /v1/clicks", "path: v1/clicks", 1),
			says: "has to start with /",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := load(t, c.yaml)
			if err == nil {
				t.Fatal("it was accepted")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not say %q: %v", c.says, err)
			}
		})
	}
}

// The defaults exist so a minimal file works, and each one is a number with a
// reason. A test that configures nothing is the one that catches a default
// quietly becoming zero.
func TestAMinimalConfigGetsWorkingDefaults(t *testing.T) {
	cfg, err := load(t, valid)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Streams[0]
	if s.Format != gateway.FormatJSON {
		t.Errorf("format defaulted to %q", s.Format)
	}
	if s.Buffer.Durability != gateway.DurabilityMemory {
		t.Errorf("durability defaulted to %q", s.Buffer.Durability)
	}
	if s.Buffer.Flush.Records == 0 || s.Buffer.Flush.Every == 0 {
		t.Errorf("a flush that never fires: %+v", s.Buffer.Flush)
	}
	if cfg.Listen.MaxBody == 0 {
		t.Error("max_body defaulted to zero, which accepts nothing")
	}
	if cfg.Listen.Addr == "" {
		t.Error("addr defaulted to empty")
	}
}

// max_body is written the way an operator writes one.
func TestASizeIsReadTheWayItIsWritten(t *testing.T) {
	cfg, err := load(t, strings.Replace(valid, "name: g", "name: g\nlisten: {max_body: 2MiB}", 1))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen.MaxBody != 2<<20 {
		t.Errorf("2MiB read as %d bytes", cfg.Listen.MaxBody)
	}
}

// A stream naming a hook nobody registered must stop the gateway from
// STARTING, not start it and drop every event of that stream.
func TestAnUnknownHookIsRefusedAtLoadAndNamesWhatExists(t *testing.T) {
	cfg, err := load(t, strings.Replace(valid, "    sink:", "    hook: missing\n    sink:", 1))
	if err != nil {
		t.Fatal(err)
	}

	hooks := gateway.NewHooks()
	hooks.MustRegister("enrich", func(e map[string]any) (map[string]any, error) { return e, nil })

	_, err = gateway.New(cfg, hooks)
	if err == nil {
		t.Fatal("the gateway started with a hook it cannot run")
	}
	if !strings.Contains(err.Error(), "enrich") {
		t.Errorf("the error does not list the hooks that exist: %v", err)
	}

	// And with none registered at all, the message says how hooks get there --
	// because "no hook called x" with an empty list is a dead end.
	_, err = gateway.New(cfg, gateway.NewHooks())
	if err == nil || !strings.Contains(err.Error(), "compiled into this binary") {
		t.Errorf("with no hooks at all the error is: %v", err)
	}
}

// A duplicate registration is refused rather than overwritten: a silently
// replaced hook is a bug that surfaces in production, with the wrong one
// running.
func TestADuplicateHookIsRefused(t *testing.T) {
	hooks := gateway.NewHooks()
	fn := func(e map[string]any) (map[string]any, error) { return e, nil }
	if err := hooks.Register("enrich", fn); err != nil {
		t.Fatal(err)
	}
	if err := hooks.Register("enrich", fn); err == nil {
		t.Error("the second registration silently replaced the first")
	}
}

func load(t *testing.T, yaml string) (*gateway.Config, error) {
	t.Helper()
	path := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return gateway.Load(path)
}
