// Package persist reads and writes context that outlives the run that made it.
//
// It is NOT the package next door. `sdk/context` carries what one step
// published to the steps below it, in the run that published it — 4096 bytes
// through the container's termination message, and no network at all. This one
// carries a value that has to still be there next week:
//
//	                  sdk/context              sdk/persist
//	lives in          the termination message  object storage
//	lives for         the run                  until somebody overwrites it
//	key               qualified by the step    chosen by whoever saves
//	ceiling           4096 bytes               1 MB
//	needs a store     no                       yes
//
// The case it was built for: one step reads an inventory of 11,276 telemetric
// stations, and another builds a batch query from their codes. That is 99 KB —
// twenty-five times what the termination message holds — and the inventory
// changes over weeks, so the second step should run hourly without the first
// running with it.
//
// # What it is not
//
// Not a source and not a target. `to.Files` names every object it writes
// `parte-<nanoseconds>` and never overwrites, on purpose: it is landing for
// data, and the timestamp is what stops one load clobbering another. Reading a
// key back through a glob would hand you every generation ever written,
// concatenated. This is a key and a value, read whole.
//
// # The backend
//
// `Use` registers it once, the way a fetcher passes `Store` to `from.Files`,
// and for the same reason: a consumer that keeps its context on disk compiles
// neither cloud SDK.
//
//	persist.Use(gcs.New(client))   // only when BREVIS_PERSIST_URL is gs://
package persist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// EnvURL is where the engine says the context lives. A step declares
// `persist_context:` in its YAML and the engine sets this; a step that did not
// declare it has nothing set, and every call here says so by name.
const EnvURL = "BREVIS_PERSIST_URL"

// EnvKeys is what the step declared it would touch. It is a DECLARATION and
// not a sandbox: the pod holds the store's credential either way, so this
// catches a typo and documents the dependency — it does not contain code that
// means harm. The bucket's own permissions are the security boundary, and they
// are the installation's to set.
const EnvKeys = "BREVIS_PERSIST_KEYS"

// MaxBytes is how much one key may hold.
//
// Inherited rather than chosen, the way sdk/context inherits 4096 from the
// kubelet. Two independent numbers in this system agree on it: every executor
// reads a step's stdout with a 1 MB line ceiling, and a Kubernetes ConfigMap
// stops at 1 MB. The case that asked for this feature is 99 KB.
const MaxBytes = 1 << 20

var (
	mu    sync.RWMutex
	store core.Store
)

// Use registers the backend. Call it once, before anything reads or writes.
//
// The local filesystem needs none: a BREVIS_PERSIST_URL that is a plain path
// works with nothing registered, which is what makes `brevis run` and a test
// work without a cloud account.
func Use(s core.Store) {
	mu.Lock()
	defer mu.Unlock()
	store = s
}

// Set writes one key, whole.
//
// Last writer wins. Object storage makes a single PUT atomic and a local write
// goes through a temporary file, so a concurrent write replaces the value and
// never interleaves with it: a reader sees the old value or the new one.
//
// That is a choice, and the alternative was available — the credential store
// next door uses a generation precondition and refuses a lost update. It is
// wrong here: this value is re-derived by the step that owns it, so a refused
// write would fail a run to protect a number the next run recomputes anyway.
func Set(ctx context.Context, key string, value any) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if err := declared(key); err != nil {
		return err
	}

	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("persist %q: the value does not serialise: %w", key, err)
	}
	if len(body) > MaxBytes {
		return fmt.Errorf("persist %q: %d bytes, and the ceiling is %d. "+
			"A value this size is data rather than context: write it where data "+
			"goes, with to.Files, and persist the path",
			key, len(body), MaxBytes)
	}

	loc, s, err := target(key)
	if err != nil {
		return err
	}
	if err := s.Create(ctx, loc.Bucket, loc.Prefix+loc.Pattern, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("persist %q: writing %s: %w", key, loc, err)
	}
	return nil
}

// Value reads one key.
//
// An absent key is not an error: ok is false and err is nil, the same contract
// sdk/context has. It matters more here than there, because the store cannot
// tell the two apart on its own — a read that returned an empty value for a key
// nobody wrote would build an empty batch query and fetch nothing, quietly.
func Value(ctx context.Context, key string) (any, bool, error) {
	raw, ok, err := Raw(ctx, key)
	if err != nil || !ok {
		return nil, false, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false, fmt.Errorf("persist %q: the stored value is not JSON: %w", key, err)
	}
	return v, true, nil
}

// Strings reads a list of text values, which is what a list of codes, of
// partitions or of accounts is.
//
// A number arrives from JSON as a float64 and is rendered without its exponent,
// because a station code that reads 2.613e+07 in a query is the kind of thing
// that is found in production.
func Strings(ctx context.Context, key string) ([]string, bool, error) {
	raw, ok, err := Raw(ctx, key)
	if err != nil || !ok {
		return nil, false, err
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, false, fmt.Errorf("persist %q: expected a list, got %s",
			key, firstBytes(raw))
	}
	out := make([]string, 0, len(list))
	for i, v := range list {
		s, err := text(v)
		if err != nil {
			return nil, false, fmt.Errorf("persist %q: item %d: %w", key, i, err)
		}
		out = append(out, s)
	}
	return out, true, nil
}

// text renders one item, and is deliberately NOT the sdk package's asText.
//
// That one is FROZEN: IngestionID computes a UUID over its output, so changing
// it rewrites every id ever written. This one is free to get stricter, and
// should -- it refuses what asText would quietly render with fmt.Sprint, which
// for a list of station codes means an object arriving as "map[...]" and a
// query asking for a station that does not exist.
//
// Sharing them would tie a thing that must never change to a thing that
// should.
func text(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case float64:
		// JSON has one number type, so a station code arrives as a float64.
		// fmt.Sprint renders 26130000 as 2.613e+07, and a query built from
		// that succeeds with no rows.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10), nil
		}
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case nil:
		return "", fmt.Errorf("null is not a value")
	}
	return "", fmt.Errorf("a %T is not text", v)
}

// Into decodes the key into a value of the caller's own shape.
func Into(ctx context.Context, key string, dst any) (bool, error) {
	raw, ok, err := Raw(ctx, key)
	if err != nil || !ok {
		return false, err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, fmt.Errorf("persist %q: decoding into %T: %w", key, dst, err)
	}
	return true, nil
}

// Raw reads the stored bytes.
func Raw(ctx context.Context, key string) ([]byte, bool, error) {
	if err := checkKey(key); err != nil {
		return nil, false, err
	}
	if err := declared(key); err != nil {
		return nil, false, err
	}

	loc, s, err := target(key)
	if err != nil {
		return nil, false, err
	}

	r, err := s.Open(ctx, loc.Bucket, loc.Prefix+loc.Pattern)
	if err != nil {
		if errors.Is(err, core.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("persist %q: reading %s: %w", key, loc, err)
	}
	defer func() { _ = r.Close() }()

	// One byte over the ceiling is read so that the refusal can be certain
	// rather than "exactly at the limit, probably fine".
	body, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("persist %q: reading %s: %w", key, loc, err)
	}
	if len(body) > MaxBytes {
		return nil, false, fmt.Errorf("persist %q: %s holds more than %d bytes",
			key, loc, MaxBytes)
	}
	return body, true, nil
}

// checkKey refuses what cannot be a path segment.
//
// A key becomes an object name, so a `/` would silently make a directory and a
// `..` would climb out of the prefix the installation chose. Both are refused
// here rather than sanitised: a key that does not mean what it says is worse
// than a key that is rejected.
func checkKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("persist: the key is empty")
	case strings.ContainsAny(key, `/\`):
		return fmt.Errorf("persist: key %q contains a path separator. "+
			"Use dots to namespace it: ana.station_codes", key)
	case key == "." || key == "..":
		return fmt.Errorf("persist: key %q is a path, not a name", key)
	case len(key) > 200:
		return fmt.Errorf("persist: key of %d characters; the ceiling is 200", len(key))
	}
	return nil
}

// declared refuses a key the step did not name in its YAML.
//
// A step that declares nothing gets an empty list and every key is refused --
// so the failure for a missing `persist_context:` names the flag rather than
// leaving somebody to find out that reads return nothing.
func declared(key string) error {
	raw, set := os.LookupEnv(EnvKeys)
	if !set {
		return nil // not under an engine that speaks this; target() decides
	}
	for _, k := range strings.Split(raw, ",") {
		if strings.TrimSpace(k) == key {
			return nil
		}
	}
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("persist: this step declared no persisted context, so %q "+
			"cannot be read or written. Add it to the step's persist_context: in the "+
			"workflow", key)
	}
	return fmt.Errorf("persist: %q is not in this step's persist_context (declared: %s)",
		key, raw)
}

// target resolves the key to a place and a backend.
func target(key string) (core.Location, core.Store, error) {
	base := os.Getenv(EnvURL)
	if base == "" {
		return core.Location{}, nil, fmt.Errorf(
			"persist: %s is not set, so there is nowhere to keep %q.\n"+
				"Under the engine, declare persist_context: [%s] on the step and the "+
				"engine sets it.\n"+
				"Outside it -- `brevis run`, or a test -- set it to a directory or to a "+
				"gs:// or s3:// prefix yourself",
			EnvURL, key, key)
	}

	loc, err := core.ParseLocation(strings.TrimSuffix(base, "/") + "/" + key + ".json")
	if err != nil {
		return core.Location{}, nil, fmt.Errorf("persist: %s is %q: %w", EnvURL, base, err)
	}

	mu.RLock()
	s := store
	mu.RUnlock()

	if s == nil {
		if loc.Scheme != "" {
			return core.Location{}, nil, fmt.Errorf(
				"persist: %s is %s and no %s backend is registered. "+
					"Call persist.Use(%s.New(client)) once, at start",
				EnvURL, base, loc.Scheme, loc.Scheme)
		}
		return loc, core.LocalStore{}, nil
	}
	if loc.Scheme != s.Scheme() {
		return core.Location{}, nil, fmt.Errorf(
			"persist: %s is %s but the registered backend serves %s://",
			EnvURL, base, s.Scheme())
	}
	return loc, s, nil
}

func firstBytes(b []byte) string {
	const n = 40
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}
