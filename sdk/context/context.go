// Package context is what one step tells the next.
//
//	import bctx "github.com/AreteAcademy/brevis/sdk/context"
//
//	bucket, err := bctx.String("extract.bucket")
//	err = bctx.Set("rows", 48213)
//
// It is the Go half of a contract that is not Go's: the same two environment
// variables the Python library reads, in the same wire format. A Go step and a
// Python step in one workflow exchange context without either knowing which
// wrote it, and a `bash` step with `jq` joins in without a library at all.
//
// # It depends on nothing
//
// Not on the rest of this SDK, not on a driver, not on the network. Importing
// it costs `encoding/json`, `os` and `strings`. A fetcher that wants context
// and nothing else pays for context and nothing else, which is the same rule
// every driver in this module follows.
//
// # How it reaches the engine
//
// It does not. There is no API call, no token and no port: the engine hands a
// step BREVIS_INPUT when it starts it and reads BREVIS_OUTPUT back when it
// ends. Under Kubernetes that path is /dev/termination-log, which the engine
// already reads to get the exit code.
package context

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// The transport. A consumer never names these; they are here because a bash
// step does.
const (
	EnvInput  = "BREVIS_INPUT"
	EnvOutput = "BREVIS_OUTPUT"
)

// MaxBytes is how much a step may publish, and it is the platform's number
// rather than this SDK's: the container's termination message is truncated at
// 4096 bytes.
//
// Inheriting a ceiling is stronger than enforcing a policy. Four kilobytes hold
// a watermark, a count or a list of partitions; they do not hold the rows, and
// that is the boundary this feature is for.
const MaxBytes = 4096

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

var (
	once     sync.Once
	incoming map[string]map[string]any
	inputErr error
)

func read() (map[string]map[string]any, error) {
	once.Do(func() {
		raw := strings.TrimSpace(os.Getenv(EnvInput))
		if raw == "" {
			incoming = map[string]map[string]any{}
			return
		}
		if err := json.Unmarshal([]byte(raw), &incoming); err != nil {
			inputErr = fmt.Errorf("%s is not the expected JSON. The engine writes it, so "+
				"this is a version mismatch rather than anything you did: %w", EnvInput, err)
		}
	})
	return incoming, inputErr
}

// Value reads one value published by a step this one depends on.
//
// The key is ALWAYS qualified by the step that wrote it -- "extract.bucket" --
// and a bare "bucket" is refused naming the steps that published it.
//
// That is not ceremony. Steps are isolated: `extract` and `transform` each
// write only their own namespace, so both can publish `bucket` without either
// losing it. A bare key throws that away the moment two of them do, and
// searching and picking one is precedence by accident -- it works until it
// silently does not, and somebody spends an afternoon on a value that came from
// the wrong step.
//
// A key that is absent returns ok=false with no error. A STEP that is absent is
// an error, because it means the pipeline does not say what the code assumes.
func Value(key string) (any, bool, error) {
	step, field, ok := strings.Cut(key, ".")
	if !ok || step == "" || field == "" {
		return nil, false, unqualified(key)
	}

	all, err := read()
	if err != nil {
		return nil, false, err
	}
	values, present := all[step]
	if !present {
		return nil, false, notVisible(step, all)
	}
	v, found := values[field]
	return v, found, nil
}

// String reads a text value. A value that is not a string is an error naming
// the type, because a silent "" downstream is a flag with no argument.
func String(key string) (string, error) {
	v, ok, err := Value(key)
	if err != nil || !ok {
		return "", err
	}
	s, isString := v.(string)
	if !isString {
		return "", fmt.Errorf("context %q holds a %T, not a string", key, v)
	}
	return s, nil
}

// Int reads a whole number.
//
// JSON has one number type, so an integer arrives as a float64. A value with a
// fractional part is an error rather than a truncation: silently turning 1.5
// into 1 is the class of difference this project has already paid for once, in
// ingestion_id.
func Int(key string) (int, error) {
	v, ok, err := Value(key)
	if err != nil || !ok {
		return 0, err
	}
	f, isNumber := v.(float64)
	if !isNumber {
		return 0, fmt.Errorf("context %q holds a %T, not a number", key, v)
	}
	if f != float64(int(f)) {
		return 0, fmt.Errorf("context %q holds %v, which is not a whole number", key, f)
	}
	return int(f), nil
}

// Into decodes one step's whole published object into a struct or a map.
func Into(step string, dst any) error {
	all, err := read()
	if err != nil {
		return err
	}
	values, present := all[step]
	if !present {
		return notVisible(step, all)
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func unqualified(key string) error {
	all, _ := read()
	var owners []string
	for step, values := range all {
		if _, ok := values[key]; ok {
			owners = append(owners, step)
		}
	}
	sort.Strings(owners)
	if len(owners) > 0 {
		quoted := make([]string, len(owners))
		for i, s := range owners {
			quoted[i] = fmt.Sprintf("%q", s+"."+key)
		}
		return fmt.Errorf("%q does not say which step published it, and %d did. Ask for "+
			"one of: %s", key, len(owners), strings.Join(quoted, ", "))
	}
	return fmt.Errorf("%q does not say which step published it. The form is \"<step>.%s\" "+
		"-- context is keyed by the step that wrote it, so two steps can publish the same "+
		"name without either losing it", key, key)
}

func notVisible(step string, all map[string]map[string]any) error {
	visible := make([]string, 0, len(all))
	for id := range all {
		visible = append(visible, id)
	}
	sort.Strings(visible)
	if len(visible) == 0 {
		return fmt.Errorf("no context is visible to this step, so %q cannot be read. Add it "+
			"to this step's depends_on in the workflow", step)
	}
	return fmt.Errorf("step %q is not visible to this step. Visible: %s. Add %q to "+
		"depends_on", step, strings.Join(visible, ", "), step)
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

var (
	mu        sync.Mutex
	published = map[string]any{}
)

// Set publishes one value for the steps that depend on this one.
//
//	bctx.Set("rows", 48213)
//	bctx.Set("watermark", "2026-09-07T03:00:00Z")
//
// Calls MERGE: those two leave both keys published, and setting the same key
// twice keeps the last write. Replacing wholesale would let a helper that
// publishes one key silently erase what its caller published -- a bug that only
// appears in the step after this one.
//
// There is no step argument, and that is what makes the isolation structural:
// the only namespace a process can write is its own.
//
// # Why this writes immediately, and Python's does not
//
// Python defers to `atexit`, so one call reaches the disk at the end. Go has no
// atexit, and the alternative -- requiring a `defer Flush()` -- makes a
// forgotten line into silent data loss, which is the failure this whole feature
// exists to prevent. So each Set rewrites the file, which at four kilobytes
// costs nothing.
//
// The visible difference is that a Go step killed mid-run publishes what it had
// set so far, and a Python one publishes nothing. Neither is read: when a step
// fails, nothing below it runs, and its retry republishes from scratch.
func Set(key string, value any) error {
	if key == "" {
		return fmt.Errorf("context key is empty")
	}
	if err := serializable(key, value); err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	previous, had := published[key]
	published[key] = value

	encoded, err := encode()
	if err != nil {
		return err
	}
	if len(encoded) > MaxBytes {
		// The message is built BEFORE the rollback: it names what is holding the
		// bytes, and rolling back first empties the very map it reads. A test
		// caught that -- the refusal arrived saying "Holding: " with nothing
		// after it, which is the half of the message that makes it useful.
		err := tooLarge(len(encoded))

		// Put it back, so a refused Set leaves nothing behind: otherwise the
		// next Set fails on a key the caller believes it never set.
		if had {
			published[key] = previous
		} else {
			delete(published, key)
		}
		return err
	}
	return write(encoded)
}

// SetAll publishes several values at once, and is the same merge.
func SetAll(values map[string]any) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := Set(k, values[k]); err != nil {
			return err
		}
	}
	return nil
}

// Published is what this step has published so far, for a test or a log line.
func Published() map[string]any {
	mu.Lock()
	defer mu.Unlock()
	out := make(map[string]any, len(published))
	for k, v := range published {
		out[k] = v
	}
	return out
}

func encode() ([]byte, error) {
	b, err := json.Marshal(published)
	if err != nil {
		return nil, fmt.Errorf("the context is not serializable: %w", err)
	}
	return b, nil
}

func serializable(key string, value any) error {
	if _, err := json.Marshal(value); err != nil {
		return fmt.Errorf("context key %q holds a %T, which is not JSON: %w", key, value, err)
	}
	return nil
}

func tooLarge(size int) error {
	mu2 := make([]string, 0, len(published))
	for k, v := range published {
		b, _ := json.Marshal(v)
		mu2 = append(mu2, fmt.Sprintf("%s (%d bytes)", k, len(b)))
	}
	sort.Strings(mu2)
	return fmt.Errorf("the context is %d bytes and the ceiling is %d. It travels in the "+
		"container's termination message, which the platform TRUNCATES rather than refuses "+
		"-- so an oversized object arrives cut in half and reads downstream as 'this step "+
		"published nothing'. Holding: %s. Publish a reference (a path, a watermark, a "+
		"count) and leave the data where it is",
		size, MaxBytes, strings.Join(mu2, ", "))
}

// write puts the whole object where the engine will read it.
//
// Nothing happens outside Brevis: no output path means nothing is going to read
// it, and a step has to stay runnable by hand. Unlike the Python library this
// does not log a line, because a Go fetcher under sdk.Run already reports its
// own context and a second voice there would be noise.
func write(encoded []byte) error {
	path := os.Getenv(EnvOutput)
	if path == "" {
		return nil
	}
	// Written directly, never renamed into place: /dev/termination-log is
	// provided by the platform, and a rename over it would fail. Atomicity is
	// not needed because the engine reads it once, after this process is gone.
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("publishing the context to %s: %w. The steps depending on this "+
			"one will not see it", path, err)
	}
	return nil
}

// resetOnce exists for this package's own tests: a real step is one process
// reading one input, and has no reason to read it twice.
func resetOnce() sync.Once { return sync.Once{} }
