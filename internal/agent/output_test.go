package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/agent"
	"github.com/AreteAcademy/brevis/internal/execution"
)

// The documented way to publish context has to work on a host.
//
// `docs/07-context.md` teaches one way, as its headline example:
//
//	jq -n --arg n "$(date -Is)" '{published_at: $n}' > "$BREVIS_OUTPUT"
//
// `local` hands the step a temp file and `kubernetes` hands it
// /dev/termination-log. This executor was built around the `@brevis:` marker
// instead -- which the RUNNER reads for every executor, so it does work -- and
// the variable was never provided. A step written the way the docs teach did
// not merely publish nothing; it FAILED:
//
//	/bin/sh: : No such file or directory
//
// Loud rather than silent, which is the better of the two, and still a step
// that has to be rewritten to move. "Point your existing steps at a pod you
// run" is the whole pitch.
func TestTheDocumentedOutputPathWorksOnAHost(t *testing.T) {
	e := pair(t, agent.Options{})

	events := run(t, e, task("out-1",
		`echo '{"published_at":"2026-10-05","rows":7}' > "$BREVIS_OUTPUT"`))

	if got := last(events); got.Kind != execution.EventSucceeded {
		t.Fatalf("the step ended %s: the docs' own line does not run here", got.Kind)
	}

	value := publishedBy(t, events)
	var got map[string]any
	if err := json.Unmarshal([]byte(value), &got); err != nil {
		t.Fatalf("what was published is not JSON: %s", value)
	}
	if got["published_at"] != "2026-10-05" || got["rows"].(float64) != 7 {
		t.Errorf("published %v, want the object the step wrote", got)
	}
}

// A step that writes nothing publishes nothing, as it does on the other two.
func TestAnEmptyOutputPublishesNothing(t *testing.T) {
	e := pair(t, agent.Options{})
	events := run(t, e, task("out-2", `echo hello`))

	if got := last(events); got.Kind != execution.EventSucceeded {
		t.Fatalf("the step ended %s", got.Kind)
	}
	for _, ev := range events {
		if strings.Contains(ev.Message, `"type":"context"`) {
			t.Errorf("a step that wrote nothing published %q", ev.Message)
		}
	}
}

// And a step may publish through the marker directly, as it always could. The
// file is a second road to the same place, not a replacement.
func TestTheMarkerStillWorksDirectly(t *testing.T) {
	e := pair(t, agent.Options{})
	events := run(t, e, task("out-3",
		`echo '@brevis:{"type":"context","value":{"from":"the marker"}}'`))

	if value := publishedBy(t, events); !strings.Contains(value, "the marker") {
		t.Errorf("the marker published %q", value)
	}
}

// publishedBy finds the context the step announced, reading the stream the way
// the runner does.
func publishedBy(t *testing.T, events []execution.Event) string {
	t.Helper()
	var found string
	for _, ev := range events {
		body, ok := strings.CutPrefix(strings.TrimSpace(ev.Message), "@brevis:")
		if !ok {
			continue
		}
		var marker struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal([]byte(body), &marker); err != nil {
			continue
		}
		if marker.Type == "context" {
			found = string(marker.Value)
		}
	}
	if found == "" {
		t.Fatalf("nothing was published; the stream was:\n%s",
			strings.Join(logs(events), "\n"))
	}
	return found
}

// What the ENGINE sent for BREVIS_OUTPUT is overridden, because its path is on
// a filesystem this host cannot see.
//
// The runner picks a path in its own ContextDir and sends it with the rest of
// the environment. A step that honoured it would write a file on THIS host at
// a path that means something over THERE, and the engine would read its own
// empty one — publishing nothing, with nothing said. The pod executor makes
// the same override for the same reason, to the kubelet's termination message.
func TestTheEnginesOutputPathIsOverridden(t *testing.T) {
	e := pair(t, agent.Options{})

	tk := task("out-4", `echo '{"rows":3}' > "$BREVIS_OUTPUT"; echo "wrote to $BREVIS_OUTPUT"`)
	tk.Env = map[string]string{"BREVIS_OUTPUT": "/engine/context/step-0.json"}

	events := run(t, e, tk)
	if got := last(events); got.Kind != execution.EventSucceeded {
		t.Fatalf("the step ended %s", got.Kind)
	}
	if value := publishedBy(t, events); !strings.Contains(value, `"rows":3`) {
		t.Errorf("published %q", value)
	}
	for _, l := range logs(events) {
		if strings.Contains(l, "/engine/context/step-0.json") {
			t.Errorf("the step was handed the ENGINE's path: %q.\n\nIt would "+
				"write there, on this host, and the engine would read its own "+
				"file and find nothing — with nothing said", l)
		}
	}
}

// Too much is refused by name rather than truncated.
//
// This becomes one line in the log stream and one entry in the ring. Truncating
// would hand the engine something that still parses and says different data,
// which is the one outcome worse than publishing nothing.
func TestTooMuchPublishedIsRefusedRatherThanTruncated(t *testing.T) {
	e := pair(t, agent.Options{})
	events := run(t, e, task("out-5",
		`{ printf '{"big":"'; head -c 1100000 /dev/zero | tr '\0' 'x'; printf '"}'; } > "$BREVIS_OUTPUT"`))

	if got := last(events); got.Kind != execution.EventSucceeded {
		t.Fatalf("the step itself ended %s; publishing too much must not fail the step", got.Kind)
	}
	var told bool
	for _, ev := range events {
		if strings.Contains(ev.Message, "the limit is") {
			told = true
			if ev.Stream != "stderr" {
				t.Errorf("the warning came on %s", ev.Stream)
			}
		}
		if strings.Contains(ev.Message, `"type":"context"`) {
			t.Error("an oversized payload was published anyway")
		}
	}
	if !told {
		t.Error("a step published more than the limit and was not told")
	}
}

// A file that is not JSON cannot be the `value` of a marker without making the
// whole line unparseable, so it is refused here and said.
func TestAnOutputThatIsNotJSONIsRefusedAndSaid(t *testing.T) {
	e := pair(t, agent.Options{})
	events := run(t, e, task("out-6", `echo 'not json at all' > "$BREVIS_OUTPUT"`))

	if got := last(events); got.Kind != execution.EventSucceeded {
		t.Fatalf("the step ended %s", got.Kind)
	}
	var told bool
	for _, ev := range events {
		if strings.Contains(ev.Message, "are not JSON") {
			told = true
		}
		if strings.Contains(ev.Message, `"type":"context"`) {
			t.Error("a non-JSON payload was published")
		}
	}
	if !told {
		t.Error("a step wrote something that is not JSON and was not told")
	}
}

// The file does not survive the execution.
func TestThePublishedFileIsCleanedUp(t *testing.T) {
	e := pair(t, agent.Options{})
	events := run(t, e, task("out-7",
		`echo '{"a":1}' > "$BREVIS_OUTPUT"; echo "path=$BREVIS_OUTPUT"`))

	var path string
	for _, l := range logs(events) {
		if _, after, ok := strings.Cut(l, "path="); ok {
			path = strings.TrimSpace(after)
		}
	}
	if path == "" {
		t.Fatal("the step did not report its path")
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s outlived the execution: one file per step per attempt, on "+
			"somebody else's host, is a disk that fills quietly", path)
	}
}

// A step this host REFUSES leaves no file behind either.
//
// Found by a mutation that removed the wrong line and lived: there are two
// cleanups, and only one was covered. A refusal is not rare — an unresolvable
// secret is retried by the pipe — so one leaked file per attempt is a disk
// filling on somebody else's host, quietly, for a step that never ran.
func TestARefusedStepLeavesNoFileBehind(t *testing.T) {
	dir := t.TempDir()
	e := pair(t, agent.Options{SecretsDir: dir, AllowedSecrets: []string{"nothing"}})

	before := countTemps(t)

	tk := task("out-8", `echo hi`)
	tk.Secrets = map[string]string{"TOKEN": "forbidden/key"}
	if _, err := e.Execute(context.Background(), tk); err == nil {
		t.Fatal("a secret outside the allowlist was accepted")
	}

	if after := countTemps(t); after > before {
		t.Errorf("%d output file(s) survived a step that was never started: a "+
			"refusal leaves no process behind and must leave no file either",
			after-before)
	}
}

func countTemps(t *testing.T) int {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(os.TempDir(), "brevis-output-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return len(found)
}
