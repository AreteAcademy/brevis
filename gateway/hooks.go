package gateway

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Hook is what runs on every event, and it is a plain Go function.
//
// Returning nil DROPS the event, and that is a feature rather than an accident:
// filtering at the edge is most of what these do. It is counted, because an
// event dropped silently is indistinguishable from one lost.
//
// An error sends that one event to the dead letter path and never fails the
// batch beside it. So does a panic: it is recovered per event, for the same
// reason -- one malformed record must not take down a process serving every
// other stream.
type Hook func(event map[string]any) (map[string]any, error)

// Hooks is what this binary knows how to run.
//
// Compiled in, and named by the YAML rather than pointed at. Measured before
// choosing: a Go function is 93 ns/op against Starlark's 959 and yaegi's 1,281,
// for nothing added to the binary -- and `plugin.Open` is not an option at all,
// because under CGO_ENABLED=0, which is the build every artifact here ships
// with, it returns "plugin: not implemented".
//
// What it costs is worth saying where somebody will read it: adding a hook is a
// rebuild and a deploy, not a config change. That is the right trade while the
// hooks are written by the same people who ship the binary, and the wrong one
// the day a customer has to change one without a release.
type Hooks struct {
	mu sync.RWMutex
	by map[string]Hook
}

func NewHooks() *Hooks { return &Hooks{by: map[string]Hook{}} }

// Register adds one.
//
// A duplicate name is REFUSED rather than overwritten, which is the rule the
// engine's task registry already follows: a silently replaced registration is a
// bug that only shows up in production, when the wrong hook runs.
func (h *Hooks) Register(name string, fn Hook) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("a hook needs a name")
	}
	if fn == nil {
		return fmt.Errorf("hook %q is nil", name)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, taken := h.by[name]; taken {
		return fmt.Errorf("a hook called %q is already registered", name)
	}
	h.by[name] = fn
	return nil
}

// MustRegister is Register for a main that has nowhere to return an error.
func (h *Hooks) MustRegister(name string, fn Hook) {
	if err := h.Register(name, fn); err != nil {
		panic(err)
	}
}

// get resolves a name, and a miss names what exists.
//
// It is resolved at LOAD and not per event, so a stream naming a hook nobody
// registered stops the gateway from starting -- instead of starting and
// dropping every event of that stream for a reason visible only in a log.
func (h *Hooks) get(name string) (Hook, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	fn, ok := h.by[name]
	if ok {
		return fn, nil
	}
	if len(h.by) == 0 {
		return nil, fmt.Errorf("no hook called %q is registered, and neither is any "+
			"other. Hooks are Go, compiled into the binary: register them and pass "+
			"them to gateway.Main. The published image has none, so a stream with a "+
			"`hook:` needs a binary of its own -- see gateway/example", name)
	}
	names := make([]string, 0, len(h.by))
	for n := range h.by {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("no hook called %q is registered (this binary has: %s)",
		name, strings.Join(names, ", "))
}

// run applies a hook, turning a panic into an error for this event alone.
func run(fn Hook, event map[string]any) (out map[string]any, err error) {
	defer func() {
		if r := recover(); r != nil {
			// The event is lost to the dead letter path and the process lives.
			// A hook is ordinary Go, so the failure modes are ordinary too --
			// a nil map, a bad type assertion -- and none of them is a reason
			// to stop serving the other streams.
			out, err = nil, fmt.Errorf("the hook panicked: %v", r)
		}
	}()
	return fn(event)
}
