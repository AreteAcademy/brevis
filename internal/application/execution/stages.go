package execution

import (
	"encoding/json"
	"sort"
	"strings"
)

// sdkMarker is the prefix the SDK uses to speak to the engine through stdout.
//
// The pipe already existed: the executor follows the pod's log while the
// container lives, and this loop sees every line, one by one. Recognising it
// here -- and not in the executor -- makes the LOCAL executor get the same for
// free, because this code does not know which of them produced the event.
const sdkMarker = "@brevis:"

// stageCeiling caps how many transitions one step may record.
//
// The log stream becomes a database write here. Without a cap, a pipeline in a
// loop would take Postgres down through the log path -- and the log is precisely
// what must not stop working when something is wrong. The SDK caps itself; this
// is the cap of somebody who does not trust what came down the pipe.
const stageCeiling = 60

// Etapa is one phase of an SDK step, as it stands now.
//
// Indice is what identifies it, and not Nome: a pipeline with two Map stages
// announces `map` twice, and keying by name would make the second overwrite the
// first -- three declared stages collapsing into two boxes on the screen, with
// no warning.
//
// RecordedStage is the same type under the name it travels through the database
// with: it is what a test outside this package needs to check what was
// recorded.
type Stage struct {
	Index    int            `json:"indice"`
	TaskName string         `json:"nome"`
	State    string         `json:"estado"`
	Ms       *int64         `json:"ms,omitempty"`
	At       string         `json:"em"`
	Numbers  map[string]any `json:"numeros,omitempty"`
}

// knownStages is a closed list on purpose: a phase this engine does not
// know is ignored, rather than becoming a meaningless box on the screen.
// RecordedStage is the shape an Etapa takes in the JSONB column.
type RecordedStage = Stage

// knownStages is a closed list on purpose: a phase this engine does not
// know is ignored, rather than becoming a meaningless box on the screen.
//
// `map` and `aggregate` are the STAGE kinds, and there can be several of each
// in one pipeline. `transform` is the single collapsed box the SDK announced up
// to v0.50.0, kept so an older fetcher still draws something.
var knownStages = map[string]bool{
	"check": true, "extract": true, "load": true,
	"map": true, "aggregate": true,
	"transform": true,
}

// stageCollector builds the phases' state out of the marked lines.
//
// A phase is ONE entry that changes state, not two lines of history: the screen
// shows four boxes, not a diary.
type stageCollector struct {
	Version string
	Stages  []Stage
	seen    int

	// Metrics the step reported since the last drain. They accumulate here and
	// the runner takes them: this type parses the pipe and knows nothing about
	// where a number goes, which is what keeps it testable without a meter.
	Metrics []StepMetric
}

// StepMetric is one value a step reported through the @brevis: protocol.
//
// Kind is "gauge" (last value wins) or "counter" (adds up). The engine supplies
// the labels; there is deliberately no field for the step's own.
type StepMetric struct {
	Name  string
	Kind  string
	Value float64
}

// drainMetrics returns what has arrived and empties the buffer.
//
// Taking rather than reading is what makes a counter correct: a metric read
// twice would be ADDED twice, and this loop runs on every marked line.
func (c *stageCollector) drainMetrics() []StepMetric {
	if len(c.Metrics) == 0 {
		return nil
	}
	out := c.Metrics
	c.Metrics = nil
	return out
}

// line consumes one log line. It returns true when the line was a marker -- and
// in that case it must NOT enter the step's log: whoever looks at the screen
// wants to see phases, not JSON in a console.
func (c *stageCollector) line(msg string) bool {
	body, ok := strings.CutPrefix(msg, sdkMarker)
	if !ok {
		return false
	}

	// TWO formats, and the second is a bridge with an expiry date.
	//
	// The SDK up to v0.47.0 spoke Portuguese: {"tipo":"etapa","nome","estado",
	// "versao","em"}. From v0.48.0 on it speaks English: {"type":"stage","name",
	// "state","version","at"}. An engine that only understood the new one would
	// make an older fetcher's phases vanish from the screen -- no error, no log,
	// just the grey box back.
	//
	// The bridge goes away once no fetcher below v0.48.0 remains in
	// production.
	var ev struct {
		Type     string `json:"type"`
		Version  string `json:"version"`
		TaskName string `json:"name"`
		State    string `json:"state"`
		Ms       *int64 `json:"ms"`
		At       string `json:"at"`
		Index    *int   `json:"index"`

		// A metric a step reported. See the "metric" case below.
		Kind  string   `json:"kind"`
		Value *float64 `json:"value"`

		TypeOld    string `json:"tipo"`
		VersionOld string `json:"versao"`
		NameOld    string `json:"nome"`
		StateOld   string `json:"estado"`
		AtOld      string `json:"em"`
	}
	if err := json.Unmarshal([]byte(body), &ev); err != nil {
		// An unreadable marker goes back to being a log line: hiding it would
		// remove from the screen the only clue that something is writing
		// rubbish in the wrong place.
		return false
	}

	// The old format fills in the new fields, and the rest of the code sees only
	// one format.
	if ev.Type == "" {
		ev.Type, ev.Version = translateOldType(ev.TypeOld), ev.VersionOld
		ev.TaskName, ev.State, ev.At = ev.NameOld, ev.StateOld, ev.AtOld
	}

	if c.seen >= stageCeiling {
		return true // consumed, but not recorded
	}
	c.seen++

	switch ev.Type {
	case "metric":
		// A number only the pipeline knows -- rows rejected by a vendor rule,
		// a watermark's age -- reported through the pipe that already exists.
		//
		// It is COLLECTED here and recorded by the runner, which is what knows
		// the workflow and the step. A metric labelled by the step itself would
		// be labelled wrongly, and asking every language's library to know its
		// own workflow slug is asking for the one thing they cannot see.
		if ev.TaskName != "" && ev.Value != nil {
			c.Metrics = append(c.Metrics, StepMetric{
				Name: ev.TaskName, Kind: ev.Kind, Value: *ev.Value,
			})
		}
		return true
	case "sdk":
		c.Version = ev.Version
		return true
	case "stage":
		if !knownStages[ev.TaskName] {
			return true
		}
		// No index means a fetcher up to v0.50.0, which announced one box per
		// NAME -- there could be only one `transform`, so the name was the
		// identity. Reusing that box's position keeps those drawing exactly as
		// they did, instead of turning `running` and `done` into two boxes.
		indice := c.positionFor(ev.TaskName)
		if ev.Index != nil {
			indice = *ev.Index
		}
		c.apply(Stage{
			Index: indice, TaskName: ev.TaskName, State: ev.State, Ms: ev.Ms, At: ev.At,
			Numbers: numbersOf(body),
		})
		return true
	}
	return true
}

// positionFor is the position an unindexed phase belongs at: the one it already
// occupies, or the next free one.
func (c *stageCollector) positionFor(name string) int {
	maior := -1
	for _, e := range c.Stages {
		if e.TaskName == name {
			return e.Index
		}
		if e.Index > maior {
			maior = e.Index
		}
	}
	return maior + 1
}

// aplicar replaces the phase at the same POSITION, keeping the pipeline's order.
func (c *stageCollector) apply(e Stage) {
	for i := range c.Stages {
		if c.Stages[i].Index == e.Index {
			c.Stages[i] = e
			return
		}
	}
	c.Stages = append(c.Stages, e)
	sort.Slice(c.Stages, func(i, j int) bool { return c.Stages[i].Index < c.Stages[j].Index })
}

// reservedFields are the ones that become the Etapa's own columns; the rest of
// the object is numbers the phase produced.
var reservedFields = map[string]bool{
	"type": true, "name": true, "state": true, "ms": true, "at": true, "version": true,
	"index": true,
	// The old format's; see stageCollector.line.
	"tipo": true, "nome": true, "estado": true, "em": true, "versao": true,
}

func numbersOf(body string) map[string]any {
	var tudo map[string]any
	if err := json.Unmarshal([]byte(body), &tudo); err != nil {
		return nil
	}
	for k := range tudo {
		if reservedFields[k] {
			delete(tudo, k)
		}
	}
	if len(tudo) == 0 {
		return nil
	}
	return tudo
}

// translateOldType maps the old format's type. See stageCollector.line.
func translateOldType(pt string) string {
	if pt == "etapa" {
		return "stage"
	}
	return pt // "sdk" is the same in both
}
