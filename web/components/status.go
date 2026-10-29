package components

import (
	"context"

	"github.com/AreteAcademy/brevis/web/i18n"
)

// status is how one state looks: the shape its mark is drawn in (a class
// app.src.css draws with ::before), its colour as a text class -- the mark
// takes currentColor, so word and mark never disagree -- and its word.
type status struct {
	shape, colour, label string
}

// statuses is the console's one vocabulary of state, for runs and steps and
// for destinations against their schedule alike.
//
// THE SHAPE CARRIES THE MEANING THE COLOUR ALSO CARRIES. Filled is settled,
// a ring is waiting, a spinning ring is working, a diamond needs a person, a
// triangle is late, a square is a choice somebody made, a pulse is a stream
// that never ends. Two states in one colour that mean different things --
// failed and stale, queued and late, success and live -- differ in shape, and
// status_test.go holds that.
//
// The colours are the ones these states always had; the shapes are new.
//
// The class strings are written out whole, never built, because Tailwind
// only compiles a class it can read in the source.
var statuses = map[string]status{
	"success":   {"dot", "text-state-success", "success"},
	"failed":    {"diamond", "text-state-failed", "failed"},
	"running":   {"spin", "text-state-running", "running"},
	"queued":    {"ring", "text-state-queued", "queued"},
	"retrying":  {"spin-dashed", "text-state-retrying", "retrying"},
	"canceled":  {"square", "text-state-canceled", "canceled"},
	"pending":   {"ring", "text-state-pending", "pending"},
	"skipped":   {"square", "text-state-skipped", "skipped"},
	"never_ran": {"ring-dashed", "text-state-pending", "never ran"},

	"on_time":      {"dot", "text-state-success", "on time"},
	"late":         {"triangle", "text-state-queued", "late"},
	"stale":        {"diamond-hollow", "text-state-failed", "stale"},
	"paused":       {"square", "text-state-canceled", "paused"},
	"continuous":   {"pulse", "text-state-running", "continuous"},
	"no_schedule":  {"ring", "text-muted", "no schedule"},
	"unidentified": {"ring-dashed", "text-muted", "unidentified"},
	"live":         {"pulse", "text-state-success", "live"},
}

// statusOrder is the vocabulary in the order it is taught: a run's states,
// then a destination's.
var statusOrder = []string{
	"success", "failed", "running", "queued", "retrying", "canceled", "pending", "skipped", "never_ran",
	"on_time", "late", "stale", "paused", "continuous", "no_schedule", "unidentified", "live",
}

// StatusKinds is every kind Status draws, for the gallery.
func StatusKinds() []string {
	return append([]string(nil), statusOrder...)
}

// lookStatus is a kind's look; one nobody declared is drawn as itself, in the
// neutral ring, rather than as some other state.
func lookStatus(kind string) status {
	if s, ok := statuses[kind]; ok {
		return s
	}
	return status{"ring", "text-muted", kind}
}

// statusWord is a kind's word in the page's language. A kind nobody
// declared is its own name, which no catalog translates.
func statusWord(ctx context.Context, kind string, s status) string {
	if _, ok := statuses[kind]; !ok {
		return s.label
	}
	return i18n.T(ctx, "state."+kind)
}

// StateWord is a run's or a step's status in the reader's language, as the
// State mark says it -- for a line of text that names a state without the
// mark beside it.
func StateWord(ctx context.Context, status string) string {
	kind := runKind(status)
	return statusWord(ctx, kind, lookStatus(kind))
}

// runKind is a run's or a step's status as a Status kind: the engine says ""
// for a node that never ran.
func runKind(s string) string {
	if s == "" {
		return "never_ran"
	}
	return s
}

// freshnessKind is a catalog status as a Status kind: anything the catalog
// does not name is a destination with no schedule to be late against.
func freshnessKind(s string) string {
	switch s {
	case "on_time", "late", "stale", "paused", "continuous", "unidentified":
		return s
	}
	return "no_schedule"
}
