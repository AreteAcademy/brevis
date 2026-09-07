// Package graph resolves the execution order from the workflow's graph.
package graph

import (
	"fmt"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// Niveis returns the nodes grouped by topological level: everything at level N
// can run in parallel, and level N+1 only starts when N finishes.
//
// Grouping by level, rather than returning a linear list, is what preserves the
// declared parallelism. A plain topological sort would serialize gold_metrics
// and gold_users, which are independent.
func Niveis(w wf.Workflow) ([][]string, error) {
	entrada := make(map[string]int, len(w.Nodes))
	saida := make(map[string][]string, len(w.Nodes))
	for _, n := range w.Nodes {
		entrada[n.ID] = 0
	}
	for _, e := range w.Edges {
		entrada[e.To]++
		saida[e.From] = append(saida[e.From], e.To)
	}

	// the first level: everything with no dependency, in the file's order so the
	// output is deterministic
	var atual []string
	for _, n := range w.Nodes {
		if entrada[n.ID] == 0 {
			atual = append(atual, n.ID)
		}
	}

	var niveis [][]string
	vistos := 0
	for len(atual) > 0 {
		niveis = append(niveis, atual)
		vistos += len(atual)

		var proximo []string
		for _, n := range w.Nodes { // iterate the nodes, not the map: determinism
			if !contem(atual, n.ID) {
				continue
			}
			for _, dest := range saida[n.ID] {
				entrada[dest]--
				if entrada[dest] == 0 {
					proximo = append(proximo, dest)
				}
			}
		}
		atual = proximo
	}

	// Workflow.Validate already refuses cycles; this guard protects against a
	// graph assembled in code without going through the validation.
	if vistos != len(w.Nodes) {
		return nil, fmt.Errorf("the graph has a cycle or an unreachable node (%d of %d nodes ordered)", vistos, len(w.Nodes))
	}
	return niveis, nil
}

func contem(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
