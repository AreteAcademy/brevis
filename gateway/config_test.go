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
			says: "only pubsub, postgres, bigquery, mysql, redshift, files are implemented",
		},
		{
			// The one refusal in this file that is about somebody else's data.
			// Appending a redelivery into a table somebody counts and merging
			// into a log that wanted every arrival are both wrong, and the
			// gateway cannot tell which table it was handed.
			name: "a table with no write mode",
			yaml: strings.Replace(valid,
				"sink: {type: pubsub, project: p, topic: t}",
				"sink: {type: postgres, dsn_from: PG_DSN, table: landing.clicks}", 1),
			says: "`write` is empty",
		},
		{
			name: "a write mode that does not exist",
			yaml: strings.Replace(valid,
				"sink: {type: pubsub, project: p, topic: t}",
				"sink: {type: postgres, dsn_from: PG_DSN, table: landing.clicks, write: replace}", 1),
			says: "use append or merge",
		},
		{
			// upsert has a refusal of its own, and a longer one, because it is
			// the word somebody reaches for MEANING merge. Falling through to
			// "use append or merge" would leave them believing merge is upsert,
			// and the two differ on whether a correction overwrites.
			name: "upsert, which is a real mode nobody implemented",
			yaml: strings.Replace(valid,
				"sink: {type: pubsub, project: p, topic: t}",
				"sink: {type: postgres, dsn_from: PG_DSN, table: landing.clicks, write: upsert}", 1),
			says: "first delivery wins",
		},
		{
			// A DSN carries a password and this file is in git, so the field
			// takes the NAME of an environment variable. Leaving it out is the
			// mistake; writing the string into it is the one the name prevents.
			name: "a table with nowhere to get the connection string",
			yaml: strings.Replace(valid,
				"sink: {type: pubsub, project: p, topic: t}",
				"sink: {type: postgres, table: landing.clicks, write: append}", 1),
			says: "name the environment variable",
		},
		{
			name: "a postgres sink with no table",
			yaml: strings.Replace(valid,
				"sink: {type: pubsub, project: p, topic: t}",
				"sink: {type: postgres, dsn_from: PG_DSN, write: append}", 1),
			says: "`table` is empty",
		},
		{
			// BigQuery's name is three parts and they are three FIELDS here.
			// Accepting the dotted form would create a table literally called
			// "landing.clicks" inside the declared dataset.
			name: "a bigquery table written the way every other one is",
			yaml: withSink("{type: bigquery, project: p, dataset: landing, table: landing.clicks, write: append}"),
			says: "the project and the dataset are their own fields",
		},
		{
			name: "a bigquery sink with no dataset",
			yaml: withSink("{type: bigquery, project: p, table: clicks, write: append}"),
			says: "`dataset` is empty",
		},
		{
			// Redshift is columnar: a row-by-row INSERT pays the cost of a
			// block, so the only workable load is COPY from S3. No staging
			// prefix means no load at all.
			name: "redshift with nowhere to stage",
			yaml: withSink("{type: redshift, dsn_from: RS_DSN, table: landing.clicks, iam_role: arn:x, write: append}"),
			says: "Redshift has no inline path",
		},
		{
			name: "redshift staging that is not S3",
			yaml: withSink("{type: redshift, dsn_from: RS_DSN, table: landing.clicks, iam_role: arn:x, staging: gs://b/p/, write: append}"),
			says: "Redshift COPYs from S3",
		},
		{
			// A key in a COPY's URL ends up in the cluster's query log, which
			// plenty of people read. The driver will not take one at all.
			name: "redshift with no role",
			yaml: withSink("{type: redshift, dsn_from: RS_DSN, table: landing.clicks, staging: s3://b/p/, write: append}"),
			says: "`iam_role` is empty",
		},
		{
			// The `write` refusals are one function now, so every table-shaped
			// sink has to reach it -- a sink added without wiring it would
			// default silently to appending.
			name: "mysql with no write mode",
			yaml: withSink("{type: mysql, dsn_from: MY_DSN, table: landing.clicks}"),
			says: "`write` is empty",
		},
		{
			name: "bigquery with no write mode",
			yaml: withSink("{type: bigquery, project: p, dataset: landing, table: clicks}"),
			says: "`write` is empty",
		},
		{
			name: "redshift with no write mode",
			yaml: withSink("{type: redshift, dsn_from: RS_DSN, table: landing.clicks, staging: s3://b/p/, iam_role: arn:x}"),
			says: "`write` is empty",
		},
		{
			name: "upsert on mysql, refused the same way as everywhere",
			yaml: withSink("{type: mysql, dsn_from: MY_DSN, table: landing.clicks, write: upsert}"),
			says: "first delivery wins",
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

// withSink swaps the valid config's destination for the one under test.
func withSink(sink string) string {
	return strings.Replace(valid, "sink: {type: pubsub, project: p, topic: t}",
		"sink: "+sink, 1)
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
	if err == nil || !strings.Contains(err.Error(), "compiled into the binary") {
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
