package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

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

type noFlow struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Position posicao        `json:"position"`
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

type posicao struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type arestaFlow struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Animated bool   `json:"animated"`
}

type respostaGrafo struct {
	Slug     string       `json:"slug"`
	RunID    string       `json:"run_id,omitempty"`
	Status   string       `json:"status,omitempty"`
	Terminal bool         `json:"terminal"`
	Nodes    []noFlow     `json:"nodes"`
	Edges    []arestaFlow `json:"edges"`
}

// Layout spacing. Constants and not configuration: the node's size is fixed in
// the CSS, and making this adjustable would only create two sources of truth.
//
// The height stopped being a single value. An SDK step grows with the phases it
// announces, and the `-(len(ids)-1) * alturaNo / 2` that centred the column
// assumed a fixed height: an expanded group ran over its neighbour. The column
// is now the SUM of its heights.
const (
	larguraNivel  = 300
	larguraNo     = 230
	alturaCartao  = 84
	alturaEtapa   = 26
	topoDasEtapas = 74
	rodapeDoGrupo = 10
	folgaVertical = 26
)

// alturaDoNo is what this step occupies vertically.
func alturaDoNo(etapas int) int {
	if etapas == 0 {
		return alturaCartao
	}
	return topoDasEtapas + etapas*alturaEtapa + rodapeDoGrupo
}

// grafoDoWorkflow draws the PUBLISHED definition, with no execution state. It is
// the "what this workflow looks like" screen, which has to work for a workflow
// that never ran.
func (u *UI) grafoDoWorkflow(w http.ResponseWriter, r *http.Request) {
	def, err := u.defs.Definicao(r.Context(), r.PathValue("slug"))
	if err != nil {
		http.Error(w, "workflow nao encontrado", http.StatusNotFound)
		return
	}
	u.responderGrafo(w, def, nil, "", "")
}

// grafoDaRun draws the SNAPSHOT stored on the Run, not the current definition:
// if the workflow was edited afterwards, a past run's screen has to keep showing
// the graph that actually ran (§22).
func (u *UI) grafoDaRun(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "id invalido", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	execucao, err := u.execs.Buscar(ctx, id)
	if err != nil {
		http.Error(w, "run nao encontrada", http.StatusNotFound)
		return
	}
	var def wf.Workflow
	if err := json.Unmarshal(execucao.Definicao, &def); err != nil {
		u.erro(w, r, err)
		return
	}

	// A missing state is not an error: a freshly queued run has no step started
	// yet, and the screen should show the whole graph in grey.
	estados, err := u.execs.EstadoDosNos(ctx, id)
	if err != nil {
		u.log.Warn("estado dos nos indisponivel", "run", id, "erro", err)
		estados = nil
	}
	u.responderGrafo(w, def, estados, id.String(), string(execucao.Status))
}

func (u *UI) responderGrafo(w http.ResponseWriter, def wf.Workflow,
	estados map[string]postgres.EstadoNo, runID, status string) {

	niveis, err := graph.Niveis(def)
	if err != nil {
		// Getting here means a cyclic graph stored in the database. Not a 500:
		// it is invalid data, and the message has to say so on the screen.
		http.Error(w, "grafo invalido: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	resp := respostaGrafo{
		Slug: def.Slug, RunID: runID, Status: status,
		// `failed` does NOT count: §7's state machine allows failed -> retrying,
		// so the client still has to poll (more slowly). Marking failed as
		// terminal would freeze the screen in the middle of a retry.
		Terminal: status == "success" || status == "canceled",
		Nodes:    []noFlow{}, Edges: []arestaFlow{},
	}

	nao := false
	for nivel, ids := range niveis {
		// The column is measured before it is drawn: the heights vary, so
		// centring requires knowing the total.
		alturas := make([]int, len(ids))
		total := (len(ids) - 1) * folgaVertical
		for i, id := range ids {
			alturas[i] = alturaDoNo(len(estados[id].Etapas))
			total += alturas[i]
		}

		y := -total / 2
		for i, id := range ids {
			no := acharNo(def.Nodes, id)
			dados := map[string]any{
				"label":  id,
				"acao":   rotuloDaAcao(no),
				"status": "pending",
			}
			e, temEstado := estados[id]
			if temEstado {
				dados["status"] = e.Status
				dados["duracao_ms"] = e.DuracaoMs
				dados["tentativa"] = e.Tentativa
				if e.Erro != "" {
					dados["erro"] = e.Erro
				}
				if e.ExitCode != nil {
					dados["exit_code"] = *e.ExitCode
				}
				// The badge. It exists because it was OBSERVED: the step announced
				// itself. Nothing in the YAML produces it, so it has no way to
				// lie.
				if e.SdkVersao != "" {
					dados["sdk"] = e.SdkVersao
				}
			}

			passo := noFlow{
				ID: id, Type: "brevis",
				Position: posicao{X: nivel * larguraNivel, Y: y},
				Data:     dados,
			}
			if len(e.Etapas) > 0 {
				// The group needs a declared size: React Flow positions the
				// children relative to it, and without a size they spill out.
				passo.Style = map[string]any{"width": larguraNo, "height": alturas[i]}
			}
			resp.Nodes = append(resp.Nodes, passo)

			// The children come AFTER the parent in the array: React Flow requires it.
			for j, et := range e.Etapas {
				resp.Nodes = append(resp.Nodes, noFlow{
					ID: id + "::" + et.Nome, Type: "etapa",
					ParentID: id, Extent: "parent",
					Position: posicao{X: 10, Y: topoDasEtapas + j*alturaEtapa},
					// Clicking a phase selects the STEP: the details panel belongs
					// to the step, and a selectable phase would open an empty
					// one.
					Selectable: &nao, Draggable: &nao,
					Data: map[string]any{
						"nome": et.Nome, "estado": et.Estado,
						"ms": et.Ms, "numeros": et.Numeros,
					},
				})
			}
			y += alturas[i] + folgaVertical
		}
	}

	for _, e := range def.Edges {
		resp.Edges = append(resp.Edges, arestaFlow{
			ID: e.From + "->" + e.To, Source: e.From, Target: e.To,
			// Only the edge arriving at what is running now is animated:
			// animating everything turns into noise and buries the
			// information.
			Animated: estados[e.To].Status == "running",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		u.log.Error("serializando grafo", "slug", def.Slug, "erro", err)
	}
}

func acharNo(nodes []wf.Node, id string) wf.Node {
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
	}
	return wf.Node{}
}

// rotuloDaAcao is the card's second line: what the node does, not what it is called.
func rotuloDaAcao(n wf.Node) string {
	if n.Action != "" {
		return n.Action
	}
	if len(n.Run) > 42 {
		return n.Run[:39] + "..."
	}
	return n.Run
}
