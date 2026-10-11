package sdk

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"unsafe"
)

// The SDK's own transformers say what they do on /data: `compute(provider)`,
// `ingestion_id`, `skip_without(id)` -- not `sdk.Compute` four times, which is
// all a function's runtime name can say, since every closure one factory makes
// shares it (decided with the owner, 2026-10-11: option (a)).
//
// A Transformer is a func, and a func carries nothing but its code, so the
// label is kept beside it, keyed by the func VALUE's identity: in the gc
// compiler a func value is one pointer to a closure record, and each closure a
// factory makes has its own. That is an implementation detail of the
// compiler, not of the language -- which is why a miss is never an error: a
// transformer with no label is named by its function, exactly as before.
//
// THE KEY HOLDS THE CLOSURE ALIVE, on purpose. A weak reference would let the
// address be reused by a later closure and hand it the wrong label -- and
// `weak.Make` cannot take one at all: a closure that captures nothing lives in
// static memory, and it is a fatal error, not a panic, which the probe for
// this found before the code did. The cost is bounded by labelCeiling.
var (
	labelsMu sync.Mutex
	labels   = map[unsafe.Pointer]string{}

	// labelCeiling bounds how many transformers keep a label. A pipeline
	// builds a handful, once; a program building them per record would
	// otherwise keep every one alive. Past it, transformers are named by
	// their function. A variable so a test can lower it.
	labelCeiling = 10_000
)

// funcIdentity is a func value's closure record.
func funcIdentity(fn Transformer) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&fn))
}

// labeled records what fn does and returns it unchanged. Each of the SDK's
// transformer factories returns through it.
func labeled(fn Transformer, label string) Transformer {
	if fn == nil {
		return nil
	}
	labelsMu.Lock()
	defer labelsMu.Unlock()
	key := funcIdentity(fn)
	if _, known := labels[key]; known || len(labels) < labelCeiling {
		labels[key] = label
	}
	return fn
}

// labelOf is what labeled recorded for fn, if anything.
func labelOf(fn Transformer) (string, bool) {
	labelsMu.Lock()
	defer labelsMu.Unlock()
	l, ok := labels[funcIdentity(fn)]
	return l, ok
}

// transformerLabels is how many labels are held, for the ceiling's test.
func transformerLabels() int {
	labelsMu.Lock()
	defer labelsMu.Unlock()
	return len(labels)
}

// call is `name(a, b)`, or `name` with no arguments.
func call(name string, args ...string) string {
	if len(args) == 0 {
		return name
	}
	return name + "(" + strings.Join(args, ", ") + ")"
}

// renames is a Rename's map as `a → b`, in a stable order.
func renames(names map[string]string) []string {
	out := make([]string, 0, len(names))
	for from, to := range names {
		out = append(out, fmt.Sprintf("%s → %s", from, to))
	}
	sort.Strings(out)
	return out
}
