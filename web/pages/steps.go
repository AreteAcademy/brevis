package pages

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/domain/runcontext"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/graph"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// StepOutput is one step of the "Step output" section: one per NODE, as the
// flow above it draws one per node. A mapped step's instances are its rows,
// not three entries called "load" that nobody can tell apart.
type StepOutput struct {
	Node   string
	Mapped bool
	// Status is the worst of its instances' last attempts, the colour the
	// node has in the flow.
	Status string
	// Ok and Total count a mapped step's instances, the flow's "2/3".
	Ok, Total int
	// Command is what the step runs, as the flow's node subtitles it.
	Command string
	Rows    []StepRow
}

// StepRow is one attempt of one instance.
type StepRow struct {
	postgres.StepLog
	// Item is what a mapped instance ran for -- the element of the list it
	// maps over, as the step received it in BREVIS_MAP_VALUE.
	Item    string
	Command string
}

// StepOutputs groups a run's logs by step, in the graph's order.
//
// The logs come in the order the steps STARTED, which is not the order a
// reader follows: the instances of a mapped step start in any order, and a
// step on a branch can start after `end`. The definition gives the order the
// flow uses. When it cannot be read every log is still shown, in the order
// it came -- this section is where somebody looks when everything else broke.
func StepOutputs(r run.Run, logs []postgres.StepLog) []StepOutput {
	var def wf.Workflow
	_ = json.Unmarshal(r.Definition, &def)

	position := map[string]int{}
	if levels, err := graph.Levels(def); err == nil {
		for _, level := range levels {
			for _, id := range level {
				position[id] = len(position)
			}
		}
	}

	var out []StepOutput
	at := map[string]int{}
	for _, l := range logs {
		i, seen := at[l.NodeID]
		if !seen {
			i = len(out)
			at[l.NodeID] = i
			out = append(out, StepOutput{Node: l.NodeID})
		}
		out[i].Rows = append(out[i].Rows, StepRow{StepLog: l})
		if l.MapIndex != run.Unmapped {
			out[i].Mapped = true
		}
	}

	// A node the definition does not know (a run older than its workflow's
	// last edit) keeps its place after the known ones.
	sort.SliceStable(out, func(a, b int) bool {
		pa, okA := position[out[a].Node]
		pb, okB := position[out[b].Node]
		if okA != okB {
			return okA
		}
		return okA && pa < pb
	})

	items := mappedItems(def, logs)
	commands := map[string]string{}
	for _, n := range def.Nodes {
		commands[n.ID] = commandOf(n)
	}
	for i := range out {
		g := &out[i]
		g.Command = commands[g.Node]
		for j := range g.Rows {
			g.Rows[j].Command = g.Command
		}
		sort.SliceStable(g.Rows, func(a, b int) bool {
			if g.Rows[a].MapIndex != g.Rows[b].MapIndex {
				return g.Rows[a].MapIndex < g.Rows[b].MapIndex
			}
			return g.Rows[a].Attempt < g.Rows[b].Attempt
		})
		last := map[int]string{}
		for j := range g.Rows {
			row := &g.Rows[j]
			last[row.MapIndex] = row.Status
			if g.Mapped {
				row.Item = itemLabel(items[g.Node], row.MapIndex)
			}
		}
		g.Status = worst(last)
		if g.Mapped {
			g.Total = len(last)
			for _, s := range last {
				if s == string(run.StatusSuccess) {
					g.Ok++
				}
			}
		}
	}
	return out
}

// mappedItems resolves, for each mapped step, the list it mapped over: the
// key its `for_each` names, in what the step above it published. The same
// resolution the runner does, read back from what was recorded.
func mappedItems(def wf.Workflow, logs []postgres.StepLog) map[string][]json.RawMessage {
	deps := map[string][]string{}
	for _, e := range def.Edges {
		deps[e.To] = append(deps[e.To], e.From)
	}
	published := map[string]json.RawMessage{}
	for _, l := range logs {
		if l.MapIndex == run.Unmapped && len(l.Output) > 0 {
			published[l.NodeID] = l.Output
		}
	}

	out := map[string][]json.RawMessage{}
	for _, n := range def.Nodes {
		if n.ForEach == "" {
			continue
		}
		step, key, ok := runcontext.SplitKey(n.ForEach, runcontext.Visible(deps, n.ID))
		if !ok {
			continue
		}
		var values map[string]json.RawMessage
		if json.Unmarshal(published[step], &values) != nil {
			continue
		}
		var list []json.RawMessage
		if json.Unmarshal(values[key], &list) == nil {
			out[n.ID] = list
		}
	}
	return out
}

// itemLabel is an instance's element as the step received it -- a string
// without its quotes, anything else as its JSON -- or its position when the
// list cannot be read back.
func itemLabel(list []json.RawMessage, i int) string {
	if i < 0 || i >= len(list) {
		return "#" + strconv.Itoa(i)
	}
	return plainValue(list[i])
}

func plainValue(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

// worst is the status a node shows for its instances' last attempts: one
// failure makes the node failed, as it does in the flow.
//
// Success ranks below everything, a status this list does not know included:
// a node is green only when every instance is, never because a new status
// happened to sort after "success".
func worst(last map[int]string) string {
	rank := map[string]int{
		string(run.StatusSuccess):  0,
		string(run.StatusSkipped):  2,
		string(run.StatusCanceled): 3,
		string(run.StatusCreated):  4,
		string(run.StatusPending):  5,
		string(run.StatusQueued):   6,
		string(run.StatusRetrying): 7,
		string(run.StatusRunning):  8,
		string(run.StatusFailed):   9,
	}
	status, best := "", -1
	for _, s := range last {
		r, known := rank[s]
		if !known {
			r = 1
		}
		if r > best || (r == best && s < status) {
			status, best = s, r
		}
	}
	return status
}

// commandOf is what a step runs, as its node in the flow says it: the action,
// the command line, or "marker" for a step that runs nothing.
func commandOf(n wf.Node) string {
	switch {
	case n.Marker:
		return "marker"
	case n.Action != "":
		return n.Action
	}
	return n.Run
}

// ContextEntry is what one step -- one instance of a mapped one -- published
// for the steps below it, as its last attempt left it.
type ContextEntry struct {
	Step string
	// JSON is the published value, indented; empty with nothing published.
	JSON  string
	Bytes int
	// State says why there is no JSON: "running", "failed" or "none".
	State string
}

// ContextOf is the run's published context, step by step in the flow's
// order: for each instance, the latest attempt that published -- what the
// run's context holds -- and, when none did, why, from its last attempt.
func ContextOf(r run.Run, logs []postgres.StepLog) []ContextEntry {
	var out []ContextEntry
	for _, g := range StepOutputs(r, logs) {
		last := map[int]postgres.StepLog{}
		published := map[int]postgres.StepLog{}
		var order []int
		for _, row := range g.Rows {
			if _, seen := last[row.MapIndex]; !seen {
				order = append(order, row.MapIndex)
			}
			last[row.MapIndex] = row.StepLog
			if len(row.Output) > 0 {
				published[row.MapIndex] = row.StepLog
			}
		}
		for _, i := range order {
			l, ok := published[i]
			if !ok {
				l = last[i]
			}
			e := ContextEntry{Step: StepName(l.NodeID, l.MapIndex), Bytes: len(l.Output)}
			switch {
			case len(l.Output) > 0:
				var b bytes.Buffer
				if json.Indent(&b, l.Output, "", "  ") == nil {
					e.JSON = b.String()
				} else {
					e.JSON = string(l.Output)
				}
			case l.Status == string(run.StatusRunning) || l.Status == string(run.StatusQueued) || l.Status == "pending":
				e.State = "running"
			case l.Status == string(run.StatusFailed):
				e.State = "failed"
			default:
				e.State = "none"
			}
			out = append(out, e)
		}
	}
	return out
}
