package sdk

import (
	"encoding/json"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

// The SDK tells the engine which phase it is in, through a marked line on
// stdout.
//
// The pipe already exists and is already running: the executor follows the
// pod's log while the container lives, and the runner sees every line, one by
// one. No callback, no new port, no new authentication and no new RBAC -- and
// the local executor gets the same for free, because what recognises the marker
// is the runner, which does not know which executor produced the event.
//
// One line per transition. The executor's copier breaks on lines, so an event
// that did not fit on one line simply would not arrive.
const phaseMarker = "@brevis:"

// The phases a pipeline announces. A closed list on purpose: an unknown phase
// is ignored by the engine rather than inventing a box on the screen.
const (
	PhaseCheck     = "check"
	PhaseExtract   = "extract"
	PhaseTransform = "transform"
	PhaseLoad      = "load"
)

// States of a phase. `aborted` is not emitted here: the engine decides it, on
// seeing the step finish with a phase still running -- because a process that
// died emits nothing.
const (
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)

// phaseCap limits how many transitions one process may announce.
//
// The log stream becomes a database write on the other side. Without a cap, a
// pipeline in a loop would take Postgres down through the log path -- and the
// log is precisely what must not stop working when something is wrong.
const phaseCap = 50

// phaseOutput is stdout, and is swappable so a test can read what was
// announced. The same seam as the slog.SetDefault the other tests use.
var phaseOutput io.Writer = os.Stdout

type reporter struct {
	mu        sync.Mutex
	emitted   int
	on        bool
	startedAt map[string]time.Time
}

// newReporter returns a reporter that is on only under the engine.
//
// Run by hand, the lines serve nobody and would only clutter the terminal of
// whoever is debugging a fetcher.
func newReporter(run RunContext) *reporter {
	return &reporter{
		on:        run.FromEngine(),
		startedAt: map[string]time.Time{},
	}
}

// announce says this step is an SDK pipeline, and with which version.
//
// The version comes from the binary itself: nobody types it, nobody keeps it in
// sync, and the badge has no way to lie. A wrong badge would be worse than no
// badge, because it is the thing people look at to rule hypotheses out.
func (r *reporter) announce(pipeline string) {
	r.emit(map[string]any{
		"type":     "sdk",
		"version":  SDKVersion(),
		"pipeline": pipeline,
	})
}

func (r *reporter) started(phase string) {
	r.mu.Lock()
	r.startedAt[phase] = time.Now()
	r.mu.Unlock()
	r.emit(map[string]any{"type": "stage", "name": phase, "state": StateRunning})
}

// finished closes the phase. The numbers go with it because a state without a
// number says nothing: "extract done" is less useful than "extract done, 300
// pages, 48,213 rows".
// withoutClock are the phases that report no duration.
//
// Transform runs per record, interleaved with the read, so any number coming
// out of it would be the time of something else -- in practice the extraction's,
// which is what sets the pace of the stream. A missing number beats a wrong
// one, and a `transform: 40min` next to an `extract: 40min` would send someone
// looking for the bottleneck in the wrong place.
var withoutClock = map[string]bool{PhaseTransform: true}

func (r *reporter) finished(phase, state string, numbers map[string]any) {
	r.mu.Lock()
	since, had := r.startedAt[phase]
	r.mu.Unlock()

	ev := map[string]any{"type": "stage", "name": phase, "state": state}
	if had && !withoutClock[phase] {
		ev["ms"] = time.Since(since).Milliseconds()
	}
	for k, v := range numbers {
		ev[k] = v
	}
	r.emit(ev)
}

func (r *reporter) emit(ev map[string]any) {
	if r == nil || !r.on {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.emitted >= phaseCap {
		return
	}
	r.emitted++

	ev["at"] = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(ev)
	if err != nil {
		return // um evento que nao serializa nao vale derrubar o pipeline
	}
	// Ignored on purpose: if stdout no longer accepts writes the pipeline has
	// a bigger problem, and failing because of telemetry would trade an
	// incomplete screen for a lost run.
	_, _ = io.WriteString(phaseOutput, phaseMarker+string(line)+"\n")
}

// SDKVersion is this module's version, read from the binary itself.
//
// Returns "devel" when the fetcher was built from a checkout or through a
// replace -- which is the truth, and beats inventing a number.
func SDKVersion() string {
	version := func() string {
		info, ok := debug.ReadBuildInfo()
		if !ok {
			return ""
		}
		for _, d := range info.Deps {
			if d.Path != modulePath {
				continue
			}
			// A REPLACED module reports the version go.mod asked for, and that
			// is fiction: the code actually running came from a directory. A
			// badge saying "v0.0.0" -- or worse, a plausible version that is
			// not the one sitting there -- is worse than no badge, because it
			// is precisely what people look at to rule hypotheses out.
			if d.Replace != nil {
				return ""
			}
			return d.Version
		}
		// O proprio modulo, quando os testes do SDK rodam dentro dele.
		if info.Main.Path == modulePath {
			return info.Main.Version
		}
		return ""
	}()

	switch version {
	case "", "(devel)":
		return "devel"
	default:
		return version
	}
}

const modulePath = "github.com/AreteAcademy/brevis/sdk"

// extractNumbers is what the extraction phase produced.
func extractNumbers(d *Data) map[string]any {
	if d == nil {
		return nil
	}
	st := d.Stats()
	n := map[string]any{}
	if st.Pages > 0 {
		n["paginas"] = st.Pages
	}
	if st.Attempts > 0 {
		n["tentativas_http"] = st.Attempts
	}
	return n
}

// loadNumbers is what the load produced.
func loadNumbers(res *Result) map[string]any {
	if res == nil {
		return nil
	}
	n := map[string]any{"linhas": res.Rows, "registros": res.Records}
	if res.Strategy != "" {
		n["estrategia"] = res.Strategy
	}
	if res.CheckpointReused {
		n["checkpoint"] = "reaproveitado"
	}
	if len(res.Objects) > 0 {
		n["objetos"] = strconv.Itoa(len(res.Objects))
	}
	return n
}
