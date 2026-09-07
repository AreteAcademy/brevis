// Package runcontext is what one step tells the next.
//
// A step publishes a small JSON object; the steps that depend on it read it.
// That is the whole feature, and everything here exists to keep it small and to
// turn its silent failures into messages.
//
// # The two variables
//
//	BREVIS_INPUT    {"<step id>": {<what it published>}}
//	BREVIS_OUTPUT   a path to write one JSON object to
//
// A task never learns where the output path points. Under Kubernetes it is
// /dev/termination-log, which the engine already reads to get the exit code;
// locally it is a temporary file. That is why this works in any language with
// no API call, no token and no port: it is reading an environment variable and
// writing a file.
//
// # Why the functions here are pure
//
// Assemble and Parse take values and return values. The decision about what a
// step may read, and whether what it wrote is usable, is then exercised with no
// database, no cluster and no engine -- which is the same reason
// load.CreationPlan in the SDK is a function and not a method on something
// holding a client.
package runcontext

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Environment variables. They are the transport, and a consumer using an SDK
// never sees them -- the way BREVIS_RUN_ID is the transport behind sdk.Run
// knowing it is under the engine.
const (
	EnvInput  = "BREVIS_INPUT"
	EnvOutput = "BREVIS_OUTPUT"
)

// MaxBytes is how much a step may publish.
//
// It is Kubernetes' number, not ours: the termination message is truncated at
// 4096 bytes. Inheriting it is the point.
//
// Every "context between tasks" feature fails the same way -- somebody puts
// DATA where only CONTEXT fits, and the orchestrator's database becomes the
// pipeline's bottleneck. A ceiling that belongs to the platform is stronger
// than a policy we would have to enforce, and four kilobytes hold a watermark,
// a count or a list of partitions. They do not hold the rows.
const MaxBytes = 4096

// Assemble builds BREVIS_INPUT for one step.
//
// `visible` is the set of step ids this step may read: its dependencies,
// transitively. Scoping to them does two things -- it keeps the variable small
// on a fifty-step DAG, where ARG_MAX is real, and it makes reading a step you
// do not depend on impossible rather than a race. A step that has not run yet
// has published nothing, and reading it would have returned an answer that
// depends on scheduling.
//
// A step in `visible` that published nothing is simply absent, which is
// different from the step not existing -- Missing tells those apart.
func Assemble(published map[string]json.RawMessage, visible []string) (string, error) {
	out := map[string]json.RawMessage{}
	for _, id := range visible {
		if raw, ok := published[id]; ok && len(raw) > 0 {
			out[id] = raw
		}
	}
	if len(out) == 0 {
		return "", nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("assembling %s: %w", EnvInput, err)
	}
	return string(b), nil
}

// Visible returns the ids a step may read: everything it depends on, and
// everything those depend on, transitively.
//
// Transitive and not direct, because the natural pipeline is exactly the case
// that needs it: `load` depends on `transform` depends on `extract`, and `load`
// legitimately wants the bucket `extract` published. Requiring it to be
// re-published at every hop would make every step copy its input forward.
//
// The workflow's own edges are the declaration. There is no second field to
// write and to keep in sync, and a cycle cannot widen the set because Validate
// already refuses one.
func Visible(edges map[string][]string, nodeID string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		for _, up := range edges[id] {
			if seen[up] {
				continue
			}
			seen[up] = true
			walk(up)
		}
	}
	walk(nodeID)

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Parse reads what a step wrote, and is where truncation stops being silent.
//
// Three outcomes, and the middle one is the reason this function exists:
//
//   - empty: the step published nothing. Legitimate, and the common case.
//   - not JSON: an ERROR, and the message says truncation first, because at
//     this size that is the cause nine times out of ten. Kubernetes TRUNCATES
//     the termination message rather than refusing it, so a 5 KB object
//     arrives cut mid-string -- and a naive read treats that as "published
//     nothing" and hands the next step a missing key with no error anywhere.
//   - an object: what the step published.
//
// A JSON value that is not an object is refused too. `["a"]` or `42` would key
// by step and then fail to merge, and the message a consumer would get is about
// a type rather than about the one line they wrote.
func Parse(raw []byte) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}

	if len(raw) >= MaxBytes {
		return nil, fmt.Errorf("the published context is %d bytes, at or over the %d-byte "+
			"ceiling, and it was almost certainly truncated: what a step publishes goes "+
			"through the container's termination message, which the platform CUTS rather "+
			"than refuses. Publish a reference -- a path, a watermark, a count -- and not "+
			"the data", len(raw), MaxBytes)
	}

	var probe any
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return nil, fmt.Errorf("the published context is not valid JSON, which at this size "+
			"usually means it was truncated: %w", err)
	}
	if _, ok := probe.(map[string]any); !ok {
		return nil, fmt.Errorf("the published context has to be a JSON object, and it is %T: "+
			"it is keyed by step id, so a list or a bare value has nowhere to go", probe)
	}
	return json.RawMessage(trimmed), nil
}

// Missing explains a key a step asked for and did not get.
//
// It exists so the engine and both SDKs give the same answer, and because the
// two causes need different fixes: a step that did not publish the key, and a
// step this one is not allowed to read.
func Missing(key, stepID string, visible []string) error {
	if len(visible) == 0 {
		return fmt.Errorf("no context is visible to this step: it declares no "+
			"depends_on, so nothing has published anything it may read (asked for %q)", key)
	}
	for _, id := range visible {
		if id == stepID {
			return fmt.Errorf("step %q published no key %q. It is visible to this step, so "+
				"either it did not publish that key or the name differs", stepID, key)
		}
	}
	return fmt.Errorf("step %q is not visible to this step: only what it depends on is, "+
		"which is %s. Add %q to depends_on", stepID, strings.Join(visible, ", "), stepID)
}
