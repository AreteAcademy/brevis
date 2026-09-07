package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

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
}

type graphResponse struct {
	Slug     string     `json:"slug"`
	RunID    string     `json:"run_id,omitempty"`
	Status   string     `json:"status,omitempty"`
	Terminal bool       `json:"terminal"`
	Nodes    []flowNode `json:"nodes"`
	Edges    []flowEdge `json:"edges"`
}

// Layout spacing. Constants and not configuration: the node's size is fixed in
// the CSS, and making this adjustable would only create two sources of truth.
//
// The height stopped being a single value. An SDK step grows with the phases it
// announces, and the `-(len(ids)-1) * alturaNo / 2` that centred the column
// assumed a fixed height: an expanded group ran over its neighbour. The column
// is now the SUM of its heights.
const (
	levelWidth    = 300
	nodeWidth     = 230
	cardHeight    = 84
	stageHeight   = 30
	stagesTop     = 74
	rodapeDoGrupo = 10
	folgaVertical = 26
)

// nodeHeight is what this step occupies vertically.
func nodeHeight(stages int) int {
	if stages == 0 {
		return cardHeight
	}
	return stagesTop + stages*stageHeight + rodapeDoGrupo
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
		http.Error(w, "id invalido", http.StatusBadRequest)
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
		http.Error(w, "grafo invalido: "+err.Error(), http.StatusUnprocessableEntity)
		return
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
	for level, ids := range levels {
		// The column is measured before it is drawn: the heights vary, so
		// centring requires knowing the total.
		heights := make([]int, len(ids))
		total := (len(ids) - 1) * folgaVertical
		for i, id := range ids {
			heights[i] = nodeHeight(len(states[id].Stages))
			total += heights[i]
		}

		y := -total / 2
		for i, id := range ids {
			no := findNode(def.Nodes, id)
			data := map[string]any{
				"label":  id,
				"acao":   actionLabel(no),
				"status": "pending",
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
			}

			step := flowNode{
				ID: id, Type: "brevis",
				Position: position{X: level * levelWidth, Y: y},
				Data:     data,
			}
			if len(e.Stages) > 0 {
				// The group needs a declared size: React Flow positions the
				// children relative to it, and without a size they spill out.
				step.Style = map[string]any{"width": nodeWidth, "height": heights[i]}
			}
			resp.Nodes = append(resp.Nodes, step)

			// The children come AFTER the parent in the array: React Flow requires it.
			for j, et := range e.Stages {
				resp.Nodes = append(resp.Nodes, flowNode{
					ID: id + "::" + et.Name, Type: "etapa",
					ParentID: id, Extent: "parent",
					Position: position{X: 10, Y: stagesTop + j*stageHeight},
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
			y += heights[i] + folgaVertical
		}
	}

	for _, e := range def.Edges {
		resp.Edges = append(resp.Edges, flowEdge{
			ID: e.From + "->" + e.To, Source: e.From, Target: e.To,
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
	if n.Action != "" {
		return n.Action
	}
	if len(n.Run) > 42 {
		return n.Run[:39] + "..."
	}
	return n.Run
}
