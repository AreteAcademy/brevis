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
func Levels(w wf.Workflow) ([][]string, error) {
	inDegree := make(map[string]int, len(w.Nodes))
	outgoing := make(map[string][]string, len(w.Nodes))
	for _, n := range w.Nodes {
		inDegree[n.ID] = 0
	}
	for _, e := range w.Edges {
		inDegree[e.To]++
		outgoing[e.From] = append(outgoing[e.From], e.To)
	}

	// the first level: everything with no dependency, in the file's order so the
	// output is deterministic
	var current []string
	for _, n := range w.Nodes {
		if inDegree[n.ID] == 0 {
			current = append(current, n.ID)
		}
	}

	var levels [][]string
	vistos := 0
	for len(current) > 0 {
		levels = append(levels, current)
		vistos += len(current)

		var next []string
		for _, n := range w.Nodes { // iterate the nodes, not the map: determinism
			if !contains(current, n.ID) {
				continue
			}
			for _, dest := range outgoing[n.ID] {
				inDegree[dest]--
				if inDegree[dest] == 0 {
					next = append(next, dest)
				}
			}
		}
		current = next
	}

	// Workflow.Validate already refuses cycles; this guard protects against a
	// graph assembled in code without going through the validation.
	if vistos != len(w.Nodes) {
		return nil, fmt.Errorf("the graph has a cycle or an unreachable node (%d of %d nodes ordered)", vistos, len(w.Nodes))
	}
	return levels, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
