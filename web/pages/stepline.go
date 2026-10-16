package pages

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// StepLine is a one-step run's step, drawn as a line instead of a canvas
// holding one box (screen-run-v2 RV3). Built by the API from the same graph
// build the island draws, so the two drawings say the same thing.
type StepLine struct {
	Name, Status string
	// Action is the command or the typed action, whole: the line truncates it
	// and keeps the full text in its title.
	Action string
	// Runtime is the chip ("go", "python"); RuntimeInferred says the command
	// suggested it rather than the YAML declaring it.
	Runtime         string
	RuntimeInferred bool
	// SDK is the version the step announced; empty for a step that is not an
	// SDK one.
	SDK        string
	DurationMs int64
	// Published is how many values the step told the steps below it.
	Published int
	Stages    []StageCell
}

// StageCell is one SDK stage on the line: its label as a person reads it
// (source and target, not the wire's extract and load), its state, the
// identity it worked on, its duration and up to two numbers.
type StageCell struct {
	Label, State, Detail string
	Ms                   *int64
	Numbers              []StageNumber
}

// StageNumber is one magnitude a stage produced: "3 in", "1 pages".
type StageNumber struct{ Key, Value string }

// StageHighlight is the order the numbers worth showing come in, and dag.js's
// HIGHLIGHT is the same list (TestTheStageRulesMatchDagJs). Two of them fit a
// line; the rest are the graph panel's.
var StageHighlight = []string{"in", "out", "groups", "rows", "pages", "objects"}

// StageStates maps a stage's recorded state to the run states' palette, as
// dag.js's STAGE_STATE_COLOURS does. Anything else is pending.
var StageStates = map[string]string{"done": "success", "running": "running", "failed": "failed", "aborted": "canceled"}

// StageNumbers picks a stage's two numbers, in StageHighlight's order. A
// number reads with thousands separators, as the island's toLocaleString
// writes it; a string is shown as it came.
func StageNumbers(numbers map[string]any) []StageNumber {
	var out []StageNumber
	for _, k := range StageHighlight {
		if len(out) == 2 {
			break
		}
		v, ok := numbers[k]
		if !ok || v == nil || v == "" {
			continue
		}
		out = append(out, StageNumber{Key: k, Value: numberText(v)})
	}
	return out
}

func numberText(v any) string {
	f, ok := v.(float64)
	if !ok {
		if i, isInt := v.(int); isInt {
			f, ok = float64(i), true
		}
	}
	if !ok {
		return fmt.Sprint(v)
	}
	if f != math.Trunc(f) {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatInt(int64(math.Abs(f)), 10)
	var b strings.Builder
	if f < 0 {
		b.WriteByte('-')
	}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// stageTone is the state a stage's dot is drawn in.
func stageTone(state string) string {
	if t, ok := StageStates[state]; ok {
		return t
	}
	return "pending"
}

func publishedText(n int) string {
	if n == 1 {
		return "1 value published"
	}
	return fmt.Sprintf("%d values published", n)
}

// RuntimeLabels is how a runtime's chip reads, as dag.js's LANG_LABEL writes
// it (pinned by TestTheStageRulesMatchDagJs). An id not here reads as itself.
var RuntimeLabels = map[string]string{
	"python": "Python", "go": "Go", "node": "Node.js", "java": "Java", "rust": "Rust",
	"php": "PHP", "ruby": "Ruby", "dotnet": ".NET", "sql": "SQL", "shell": "Shell",
	"dbt": "dbt", "spark": "Spark", "airbyte": "Airbyte", "soda": "Soda",
	"sqlmesh": "SQLMesh", "meltano": "Meltano", "dlt": "dlt", "duckdb": "DuckDB",
	"pandas": "pandas", "polars": "Polars", "airflow": "Airflow",
	"terraform": "Terraform",
}

func runtimeLabel(id string) string {
	if l, ok := RuntimeLabels[id]; ok {
		return l
	}
	return id
}
