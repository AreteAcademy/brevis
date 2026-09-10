package api

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/google/uuid"

	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/domain/runcontext"
	"github.com/AreteAcademy/brevis/internal/domain/runtimes"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/graph"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// This file implements the flow from §20 of the plan:
//
//	definition in the database -> API -> React Flow JSON -> UI
//
// React Flow is a VIEW layer, never the source of truth. The layout comes from
// here, from the server, reusing the same `graph.Niveis` the executor uses to
// decide what runs in parallel. That way the drawing matches the real execution:
// nodes in the same column are the ones that actually run together, rather than
// a guess by a layout algorithm in the browser.

type flowNode struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Position position       `json:"position"`
	Data     map[string]any `json:"data"`

	// Nesting: an SDK step becomes a group, and its phases become child nodes
	// INSIDE it. The DAG's edges stay between steps, so the group occupies a
	// single column -- which is what preserves "same column means runs in
	// parallel".
	ParentID   string         `json:"parentId,omitempty"`
	Extent     string         `json:"extent,omitempty"`
	Style      map[string]any `json:"style,omitempty"`
	Selectable *bool          `json:"selectable,omitempty"`
	Draggable  *bool          `json:"draggable,omitempty"`
}

type position struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type flowEdge struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Animated bool   `json:"animated"`

	// Label is what the dependency means, and it is omitted when there is
	// none -- which is most of them. An unlabelled edge's payload stays byte
	// for byte what it was.
	Label string `json:"label,omitempty"`
}

type graphResponse struct {
	Slug     string     `json:"slug"`
	RunID    string     `json:"run_id,omitempty"`
	Status   string     `json:"status,omitempty"`
	Terminal bool       `json:"terminal"`
	Nodes    []flowNode `json:"nodes"`
	Edges    []flowEdge `json:"edges"`
}

// Layout spacing.
//
// The numbers below MEASURE what the island draws. They are not a preference:
// React Flow positions a step's phases as absolute children of its card, so a
// height that is too small puts the first phase on top of the card's own last
// line -- which is what happened when the card grew a chip row and a context
// count and these constants did not.
//
// Every one of them is a row in web/assets/dag.js. If a row is added there, one
// of these has to move, and TestTheCardIsMeasuredNotGuessed is what says so.
const (
	// Horizontal. levelWidth has to leave room for an edge LABEL between two
	// cards, or the label lands on the card it points at.
	levelWidth = 360
	nodeWidth  = 230

	// The card's own rows, as the island lays them out.
	cardPadding = 11 // padding: "11px 13px", top and bottom
	headerRow   = 20 // the dot, the name, the [3] and the SDK badge
	commandRow  = 19 // marginTop 4 + a monospace line
	chipsRow    = 27 // marginTop 7 + a row of chips
	contextRow  = 22 // marginTop 6 + one small line
	statusRow   = 22 // marginTop 7 + the state and the duration

	// A step's phases, drawn under its own content and INSIDE its card.
	stageHeight = 30 // the pitch from one phase to the next
	stageRow    = 24 // the pill itself; the remainder is the gap between them
	stageInset  = 10 // left and right, so it does not touch the card's border
	stagesFoot  = 12 // so the last phase does not touch the card's bottom

	// Between two cards in the same column, and around a group's contents.
	folgaVertical = 34
	groupPadding  = 22
	groupHeader   = 34
	// Between two lanes. Bigger than the gap between cards, because it is what
	// separates a group's box from whatever is under it.
	laneGap = 56
)

// cardHeight is the height of the card the island will draw for this data.
//
// Measured from the SAME map that is sent to the browser, so the layout and the
// drawing cannot disagree about how tall a step is. Guessing one number for
// every card is what put a phase on top of a status line.
func cardHeight(data map[string]any) int {
	h := 2*cardPadding + headerRow + statusRow
	if v, ok := data["acao"].(string); ok && v != "" {
		h += commandRow
	}
	if _, ok := data["runtime"]; ok {
		h += chipsRow
	} else if _, ok := data["tools"]; ok {
		h += chipsRow
	}
	if _, ok := data["context"]; ok {
		h += contextRow
	}
	return h
}

// nodeHeight is the card plus the phases it announces.
func nodeHeight(data map[string]any, stages int) int {
	if stages == 0 {
		return cardHeight(data)
	}
	return cardHeight(data) + stages*stageHeight + stagesFoot
}

// workflowGraph draws the PUBLISHED definition, with no execution state. It is
// the "what this workflow looks like" screen, which has to work for a workflow
// that never ran.
func (u *UI) workflowGraph(w http.ResponseWriter, r *http.Request) {
	def, err := u.defs.Definition(r.Context(), r.PathValue("slug"))
	if err != nil {
		http.Error(w, "workflow not found", http.StatusNotFound)
		return
	}
	u.respondGraph(w, def, nil, "", "")
}

// runGraph draws the SNAPSHOT stored on the Run, not the current definition:
// if the workflow was edited afterwards, a past run's screen has to keep showing
// the graph that actually ran (§22).
func (u *UI) runGraph(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	run, err := u.execs.Get(ctx, id)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	var def wf.Workflow
	if err := json.Unmarshal(run.Definition, &def); err != nil {
		u.failure(w, r, err)
		return
	}

	// A missing state is not an error: a freshly queued run has no step started
	// yet, and the screen should show the whole graph in grey.
	states, err := u.execs.NodeStates(ctx, id)
	if err != nil {
		u.log.Warn("node state unavailable", "run", id, "error", err)
		states = nil
	}
	u.respondGraph(w, def, states, id.String(), string(run.Status))
}

func (u *UI) respondGraph(w http.ResponseWriter, def wf.Workflow,
	states map[string]postgres.NodeState, runID, status string) {

	levels, err := graph.Levels(def)
	if err != nil {
		// Getting here means a cyclic graph stored in the database. Not a 500:
		// it is invalid data, and the message has to say so on the screen.
		http.Error(w, "invalid graph: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	// The two inputs the availability needs, built once for the whole graph
	// rather than per node.
	publishedByNode := map[string]json.RawMessage{}
	for id, st := range states {
		if len(st.Published) > 0 {
			publishedByNode[id] = st.Published
		}
	}
	upstream := map[string][]string{}
	for _, e := range def.Edges {
		upstream[e.To] = append(upstream[e.To], e.From)
	}

	resp := graphResponse{
		Slug: def.Slug, RunID: runID, Status: status,
		// `failed` does NOT count: §7's state machine allows failed -> retrying,
		// so the client still has to poll (more slowly). Marking failed as
		// terminal would freeze the screen in the middle of a retry.
		Terminal: status == "success" || status == "canceled",
		Nodes:    []flowNode{}, Edges: []flowEdge{},
	}

	off := false
	boxes := newGrouping()

	// Two passes. The card's height depends on what is IN it -- a chip row, a
	// context count, a command -- so the payload has to exist before anything
	// can be positioned. One pass guessed a single height for every card, and
	// the guess is what put a step's first phase on top of its own status line.
	built := map[string]map[string]any{}
	for _, ids := range levels {
		for _, id := range ids {
			no := findNode(def.Nodes, id)
			data := map[string]any{
				"label": id,
				"acao":  actionLabel(no),
				// From the domain, not a literal. The UI owned this string for
				// as long as it existed -- and a state the screen knows and the
				// engine does not is how the two drift.
				"status": string(dom.StatusPending),
			}
			// A mapped step's [4], from the rows rather than from a column: a
			// count that is stored has to be kept in sync, and a wrong one
			// outlives the fix. Omitted entirely when the step is not mapped.
			if st := states[id]; st.Instances > 0 {
				data["instancias"] = st.Instances
				data["instancias_ok"] = st.Done
			}
			// What the step runs in. Computed HERE, on the definition already
			// loaded, and not stored in a column: the inference rules will be
			// wrong at first, and a stored value freezes a wrong guess into
			// every workflow published before the fix, needing a backfill.
			// Computed on read, fixing the rule fixes history.
			//
			// Omitted entirely when there is nothing to say, so an unknown
			// step's payload is byte-identical to the one before this feature.
			if tc := runtimes.Resolve(no.Runtime, no.Tools,
				runtimes.Detect(no.Run, def.ImageFor(no), no.Action)); !tc.Empty() {
				if tc.Runtime != "" {
					data["runtime"] = tc.Runtime
				}
				if len(tc.Tools) > 0 {
					data["tools"] = tc.Tools
				}
				// The source is what lets the screen draw an inferred chip
				// differently from a declared one. "This step runs Python" and
				// "this command starts with python" are different claims, and
				// a chip that hides which one it is borrows the credibility of
				// the SDK badge beside it -- which cannot lie.
				data["runtime_source"] = tc.Source
			}
			e, hasState := states[id]
			if hasState {
				data["status"] = e.Status
				data["duracao_ms"] = e.DurationMs
				data["tentativa"] = e.Attempt
				if e.Err != "" {
					data["erro"] = e.Err
				}
				if e.ExitCode != nil {
					data["exit_code"] = *e.ExitCode
				}
				// The badge. It exists because it was OBSERVED: the step announced
				// itself. Nothing in the YAML produces it, so it has no way to
				// lie.
				if e.SdkVersion != "" {
					data["sdk"] = e.SdkVersion
				}
				// What this step told the steps below it.
				//
				// The whole object, because the panel shows it: the card only
				// gets a count, and a count with no way to see what it counts
				// is a number nobody can act on. Absent when the step published
				// nothing, which is most steps -- so their card is byte for
				// byte the one they had before this feature.
				if len(e.Published) > 0 {
					data["context"] = e.Published
				}
			}

			// What the runner handed this step, keyed by the step that wrote
			// each value.
			//
			// Computed with the SAME two functions the runner calls, and that
			// is the whole reason it is computed here rather than walked from
			// the edges by the island: two answers to "what did this step
			// receive" is one answer too many, and the one on the screen would
			// be the one somebody trusts.
			//
			// It is deliberately NOT called "read". The engine does not observe
			// get() calls -- it knows what was available, not what was used,
			// and a step can be handed a value it never asks for. Labelling it
			// read would be a claim the data does not support.
			//
			// The cost is duplication: a step deep in a chain carries its
			// ancestors' context as well as its own. Bounded by 4 KB per step
			// and small in practice; if it ever shows up on the 2s poll, the
			// answer is a separate endpoint fetched on selection, not a second
			// implementation of the visibility rule.
			if in, err := runcontext.Assemble(publishedByNode, runcontext.Visible(upstream, id)); err == nil && in != "" {
				data["available"] = json.RawMessage(in)
			}

			if no.Group != "" {
				data["grupo"] = no.Group
			}

			built[id] = data
		}
	}

	// Where each step sits. See layout: a group gets a vertical LANE of its
	// own, so its box can never contain a step that is not in it.
	at := layout(levels, built, states)

	for _, ids := range levels {
		for _, id := range ids {
			data := built[id]
			e := states[id]
			spot := at[id]

			step := flowNode{
				ID: id, Type: "brevis",
				Position: position{X: spot.x, Y: spot.y},
				Data:     data,
			}
			// EVERY card gets the declared width, not just the ones with
			// phases. Letting the others size themselves made a column of
			// different-width cards with ragged edges, and the layout above
			// reserves nodeWidth for all of them anyway -- so the drawing and
			// the arithmetic disagreed about how wide a step is.
			//
			// The height is declared only when there are phases: React Flow
			// positions those as children, and without a height they spill out
			// of the card.
			step.Style = map[string]any{"width": nodeWidth}
			if len(e.Stages) > 0 {
				step.Style["height"] = spot.h
			}
			if g, ok := data["grupo"].(string); ok {
				boxes.add(g, spot.x, spot.y, spot.h)
			}
			resp.Nodes = append(resp.Nodes, step)

			// The children come AFTER the parent in the array: React Flow requires it.
			for j, et := range e.Stages {
				resp.Nodes = append(resp.Nodes, flowNode{
					ID: id + "::" + et.Name, Type: "etapa",
					ParentID: id, Extent: "parent",
					// Under the card's own content, measured rather than
					// guessed. A fixed offset put the first phase on top of the
					// status line the moment a card grew a row.
					Position: position{X: stageInset, Y: cardHeight(data) + j*stageHeight},
					// Declared here, like the card's. It was 210 hard-coded in
					// the island, plus 16 of padding and 2 of border -- 228
					// inside a 230 card placed 10 from the left, so every pill
					// hung 8px over the right edge.
					Style: map[string]any{
						"width": nodeWidth - 2*stageInset, "height": stageRow,
					},
					// Clicking a phase selects the STEP: the details panel belongs
					// to the step, and a selectable phase would open an empty
					// one.
					Selectable: &off, Draggable: &off,
					Data: map[string]any{
						"nome": et.Name, "estado": et.State,
						"ms": et.Ms, "numeros": et.Numbers,
						// The label the screen shows. `extract` and `load` are
						// the wire's names, kept for compatibility; what a
						// person reads is what they mean.
						"rotulo": stageLabel(et.Name),
					},
				})
			}
		}
	}

	// The group boxes go FIRST in the array, and that is the whole trick:
	// React Flow draws in order, so a node placed before the steps renders
	// behind them. They are not parents -- a `parentId` would make every
	// member's position relative and collide with the SDK phases, which
	// already use that mechanism.
	resp.Nodes = append(boxes.nodes(), resp.Nodes...)

	for _, e := range def.Edges {
		resp.Edges = append(resp.Edges, flowEdge{
			ID: e.From + "->" + e.To, Source: e.From, Target: e.To, Label: e.Label,
			// Only the edge arriving at what is running now is animated:
			// animating everything turns into noise and buries the
			// information.
			Animated: states[e.To].Status == "running",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		u.log.Error("serializing the graph", "slug", def.Slug, "error", err)
	}
}

// spot is where one step ends up, and how tall it is.
type spot struct{ x, y, h int }

// layout places every step, and it exists for one rule: a GROUP gets a vertical
// lane of its own, so its box can never contain a step that is not in it.
//
// The bounding box of a group's members was the first design, and it lies. In
// the example workflow, `notify_failure` belongs to no group and sits in the
// same column as `quality.count_rows`, between it and the next member -- so it
// fell inside QUALITY's rectangle and the screen said it was part of a group it
// has nothing to do with. A box that claims the wrong membership is worse than
// no box.
//
// Groups whose COLUMNS do not overlap share a lane, which is what keeps a
// linear pipeline looking linear: ingestion, reporting and quality follow each
// other left to right on one line rather than descending a staircase.
func layout(levels [][]string, built map[string]map[string]any,
	states map[string]postgres.NodeState,
) map[string]spot {
	height := func(id string) int {
		return nodeHeight(built[id], len(states[id].Stages))
	}
	groupOf := func(id string) string {
		g, _ := built[id]["grupo"].(string)
		return g
	}

	// Which columns each group touches, and the tallest stack it needs in any
	// one of them.
	type span struct {
		first, last int
		order       int
		tallest     int
	}
	spans := map[string]*span{}
	var groupOrder []string
	for level, ids := range levels {
		perGroup := map[string]int{}
		for _, id := range ids {
			g := groupOf(id)
			if g == "" {
				continue
			}
			if _, ok := spans[g]; !ok {
				spans[g] = &span{first: level, last: level, order: len(groupOrder)}
				groupOrder = append(groupOrder, g)
			}
			spans[g].last = level
			perGroup[g] += height(id) + folgaVertical
		}
		for g, stack := range perGroup {
			if stack-folgaVertical > spans[g].tallest {
				spans[g].tallest = stack - folgaVertical
			}
		}
	}

	// Lanes, greedily: a group goes in the first lane whose last column ends
	// before this one begins. Disjoint groups share a lane; overlapping ones
	// are stacked, which is the only way their boxes can stay apart.
	sort.Slice(groupOrder, func(i, j int) bool {
		a, b := spans[groupOrder[i]], spans[groupOrder[j]]
		if a.first != b.first {
			return a.first < b.first
		}
		return a.order < b.order
	})
	var laneEnd []int
	laneOf := map[string]int{}
	for _, g := range groupOrder {
		placed := false
		for lane, end := range laneEnd {
			if spans[g].first > end {
				laneEnd[lane] = spans[g].last
				laneOf[g] = lane
				placed = true
				break
			}
		}
		if !placed {
			laneOf[g] = len(laneEnd)
			laneEnd = append(laneEnd, spans[g].last)
		}
	}

	// An ungrouped step keeps its place on the main line UNLESS a box would
	// reach it. Sending every ungrouped step to a lane of its own was the first
	// fix and it made the spine zigzag: `start` and `end` touch no group and
	// have no reason to leave the line the rest of the flow is on.
	//
	// A box spans every column between its first member and its last, including
	// ones where it has none -- so the test is the SPAN, not the membership.
	covered := map[int]bool{}
	for _, sp := range spans {
		for c := sp.first; c <= sp.last; c++ {
			covered[c] = true
		}
	}
	laneFor := func(level int, id string) int {
		if g := groupOf(id); g != "" {
			return laneOf[g]
		}
		if covered[level] {
			return len(laneEnd) // the loose lane, under every box
		}
		return 0
	}

	laneHeight := make([]int, len(laneEnd))
	for g, lane := range laneOf {
		if spans[g].tallest > laneHeight[lane] {
			laneHeight[lane] = spans[g].tallest
		}
	}
	loose := 0
	for level, ids := range levels {
		stack := 0
		for _, id := range ids {
			if laneFor(level, id) == len(laneEnd) {
				stack += height(id) + folgaVertical
			}
		}
		if stack-folgaVertical > loose {
			loose = stack - folgaVertical
		}
	}
	if loose > 0 {
		laneHeight = append(laneHeight, loose)
	}
	// A step on the main line in a column with no group still has to fit lane
	// zero, which was sized for the tallest group's stack.
	for level, ids := range levels {
		stack := 0
		for _, id := range ids {
			if laneFor(level, id) == 0 {
				stack += height(id) + folgaVertical
			}
		}
		if stack-folgaVertical > laneHeight[0] {
			laneHeight[0] = stack - folgaVertical
		}
	}

	laneTop := make([]int, len(laneHeight))
	total := 0
	for i, h := range laneHeight {
		laneTop[i] = total
		total += h + laneGap
	}
	if total > 0 {
		total -= laneGap
	}
	// Centred on zero, so a graph with one lane looks exactly as it did.
	for i := range laneTop {
		laneTop[i] -= total / 2
	}
	out := map[string]spot{}
	for level, ids := range levels {
		// Where the next card goes in each lane of this column. A lane's stack
		// is centred inside it, so a group with one member in this column does
		// not hug its box's top edge.
		next := map[int]int{}
		stacks := map[int]int{}
		for _, id := range ids {
			stacks[laneFor(level, id)] += height(id) + folgaVertical
		}
		for lane, stack := range stacks {
			if lane >= len(laneHeight) {
				continue // a loose lane that ended up with nothing in it
			}
			next[lane] = laneTop[lane] + (laneHeight[lane]-(stack-folgaVertical))/2
		}

		for _, id := range ids {
			lane := laneFor(level, id)
			h := height(id)
			out[id] = spot{x: level * levelWidth, y: next[lane], h: h}
			next[lane] += h + folgaVertical
		}
	}
	return out
}

// grouping collects the bounding box of each named group as the layout is
// computed, because the box cannot be drawn before its members are placed.
type grouping struct {
	order []string
	by    map[string]*box
}

type box struct{ minX, minY, maxX, maxY int }

func newGrouping() *grouping { return &grouping{by: map[string]*box{}} }

func (g *grouping) add(name string, x, y, height int) {
	b, ok := g.by[name]
	if !ok {
		b = &box{minX: x, minY: y, maxX: x + nodeWidth, maxY: y + height}
		g.by[name] = b
		g.order = append(g.order, name)
		return
	}
	b.minX = min(b.minX, x)
	b.minY = min(b.minY, y)
	b.maxX = max(b.maxX, x+nodeWidth)
	b.maxY = max(b.maxY, y+height)
}

func (g *grouping) nodes() []flowNode {
	off := false
	out := make([]flowNode, 0, len(g.order))
	for _, name := range g.order {
		b := g.by[name]
		out = append(out, flowNode{
			ID: "grupo::" + name, Type: "grupo",
			Position: position{X: b.minX - groupPadding, Y: b.minY - groupPadding - groupHeader},
			// Not selectable and not draggable: clicking the box behind a step
			// must not open a details panel for something that is not a step.
			Selectable: &off, Draggable: &off,
			Style: map[string]any{
				"width":  b.maxX - b.minX + 2*groupPadding,
				"height": b.maxY - b.minY + 2*groupPadding + groupHeader,
			},
			Data: map[string]any{"label": name},
		})
	}
	return out
}

// stageLabel is what a person reads on the box.
//
// The wire keeps `extract` and `load` because renaming them would make an
// already-published engine stop drawing an older fetcher's phases. The screen is
// free to say what they mean.
func stageLabel(name string) string {
	switch name {
	case "extract":
		return "source"
	case "load":
		return "target"
	}
	return name
}

func findNode(nodes []wf.Node, id string) wf.Node {
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
	}
	return wf.Node{}
}

// actionLabel is the card's second line: what the node does, not what it is called.
func actionLabel(n wf.Node) string {
	// A marker runs nothing, and an empty subtitle would make it look like a
	// step whose command failed to load. Saying what it is costs one line and
	// stops somebody from opening the YAML to find out why it is blank.
	if n.Marker {
		return "marker"
	}
	if n.Action != "" {
		return n.Action
	}
	if len(n.Run) > 42 {
		return n.Run[:39] + "..."
	}
	return n.Run
}
