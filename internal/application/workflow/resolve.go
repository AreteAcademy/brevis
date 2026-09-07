package workflow

import (
	"fmt"
	"sort"
	"strings"

	dominio "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// Resolve expands every `uses:` across a set of workflows.
//
// It works on the WHOLE set and not on one file, because a `uses:` names a
// sibling. That is also why the child must be in the same publish, and not
// merely published already: expansion that reads the database would make
// `brevis validate` -- which touches no database, on purpose -- answer a
// different question from `brevis publish`, and the file that passed CI would
// be the file that failed the deploy.
//
// The child's steps take the parent step's place, prefixed with its id, and the
// parent node disappears. What arrives at the runner is one flat graph, which
// is the whole point: a step that TRIGGERED a child run and waited would hold a
// slot from the same per-process semaphore the child needs, and hang under
// load.
func Resolve(all []dominio.Workflow) ([]dominio.Workflow, error) {
	bySlug := make(map[string]dominio.Workflow, len(all))
	for _, w := range all {
		bySlug[w.Slug] = w
	}

	out := make([]dominio.Workflow, 0, len(all))
	for _, w := range all {
		flat, err := expand(w, bySlug, nil)
		if err != nil {
			return nil, err
		}
		// Checked AGAIN, on the expanded graph. The first Validate ran on a
		// file with a `uses:` node in it; this one is the graph that will
		// actually run, with the child's ids, edges and cycles in place.
		if err := flat.Validate(); err != nil {
			return nil, fmt.Errorf("%s: after expanding `uses`: %w", w.Slug, err)
		}
		out = append(out, flat)
	}
	return out, nil
}

// expand inlines every `uses:` in one workflow, depth first.
//
// `visiting` is the chain that led here, and it is what catches a cycle. A
// workflow that uses itself, directly or through three others, would otherwise
// expand until the process runs out of memory -- and the error would be a stack
// overflow rather than the name of the file somebody has to fix.
func expand(w dominio.Workflow, bySlug map[string]dominio.Workflow, visiting []string) (dominio.Workflow, error) {
	for _, seen := range visiting {
		if seen == w.Slug {
			return w, fmt.Errorf("`uses` goes in a circle: %s -> %s",
				strings.Join(visiting, " -> "), w.Slug)
		}
	}

	var nodes []dominio.Node
	var inner []dominio.Edge
	// Where each expanded step BEGINS and ENDS, kept here because it is only
	// knowable from the child's own edges -- the parent's say nothing about
	// what is inside a step it has not opened yet.
	roots := map[string][]string{}
	leaves := map[string][]string{}

	for _, n := range w.Nodes {
		if n.Uses == "" {
			nodes = append(nodes, n)
			continue
		}

		child, ok := bySlug[n.Uses]
		if !ok {
			return w, fmt.Errorf("workflow %q: step %q uses %q, which is not in this "+
				"publish (available: %s)", w.Slug, n.ID, n.Uses, known(bySlug))
		}
		// Depth first: a grandchild is already flat by the time it gets here,
		// so one pass is enough and the cycle check bounds the recursion.
		child, err := expand(child, bySlug, append(visiting, w.Slug))
		if err != nil {
			return w, err
		}
		if len(child.Nodes) == 0 {
			return w, fmt.Errorf("workflow %q: step %q uses %q, which has no steps",
				w.Slug, n.ID, n.Uses)
		}

		childNodes, childEdges := inline(n, child)
		nodes = append(nodes, childNodes...)
		inner = append(inner, childEdges...)
		roots[n.ID], leaves[n.ID] = boundaries(n.ID, child)
	}

	if len(roots) == 0 {
		return w, nil
	}

	w.Edges = append(inner, rewire(w.Edges, roots, leaves)...)
	w.Nodes = nodes
	return w, nil
}

// boundaries finds the child's steps with nothing above them and the ones with
// nothing below them, already prefixed.
//
// They are what makes the expansion mean what the file said: whatever depended
// on `mlops` depends on the WHOLE of it, and `mlops` starts when its own
// dependencies are done.
func boundaries(host string, child dominio.Workflow) (roots, leaves []string) {
	hasIncoming, hasOutgoing := map[string]bool{}, map[string]bool{}
	for _, e := range child.Edges {
		hasIncoming[e.To] = true
		hasOutgoing[e.From] = true
	}
	for _, n := range child.Nodes {
		if !hasIncoming[n.ID] {
			roots = append(roots, host+"."+n.ID)
		}
		if !hasOutgoing[n.ID] {
			leaves = append(leaves, host+"."+n.ID)
		}
	}
	sort.Strings(roots)
	sort.Strings(leaves)
	return roots, leaves
}

// rewire replaces the parent's edges that touched a `uses:` step.
//
// An arrow INTO the step becomes one into each of the child's roots; an arrow
// OUT of it becomes one out of each of its leaves. An edge between two expanded
// steps becomes every leaf of the first to every root of the second.
func rewire(edges []dominio.Edge, roots, leaves map[string][]string) []dominio.Edge {
	var out []dominio.Edge
	for _, e := range edges {
		fromLeaves, fromHost := leaves[e.From]
		toRoots, toHost := roots[e.To]
		switch {
		case !fromHost && !toHost:
			out = append(out, e)
		case fromHost && !toHost:
			for _, leaf := range fromLeaves {
				out = append(out, dominio.Edge{From: leaf, To: e.To, Label: e.Label})
			}
		case !fromHost && toHost:
			for _, root := range toRoots {
				out = append(out, dominio.Edge{From: e.From, To: root, Label: e.Label})
			}
		default:
			for _, leaf := range fromLeaves {
				for _, root := range toRoots {
					out = append(out, dominio.Edge{From: leaf, To: root, Label: e.Label})
				}
			}
		}
	}
	return out
}

// inline turns a child's steps into the parent's, and returns the child's own
// edges with both ends prefixed.
//
// Every node comes out SELF-CONTAINED: the child workflow's `image:`, `env:`,
// `secrets:` and `resources:` are materialised onto it. Without that the steps
// would land in the parent and start inheriting the PARENT's image -- the same
// YAML producing a different container depending on who used it.
func inline(host dominio.Node, child dominio.Workflow) ([]dominio.Node, []dominio.Edge) {
	nodes := make([]dominio.Node, 0, len(child.Nodes))
	for _, n := range child.Nodes {
		n.Image = child.ImageFor(n)
		n.Resources = child.ResourcesFor(n)
		n.Env = child.EnvDe(n)
		n.Secrets = child.SecretsDe(n)

		// The group is the host step's id, so the child arrives as one
		// collapsible box on the graph -- which is what somebody writing
		// `uses:` was drawing in their head.
		if n.Group == "" {
			n.Group = host.ID
		} else {
			n.Group = host.ID + "." + n.Group
		}

		// The keys a child's step reads name the child's OWN steps, so they
		// move with the prefix: a key that named `extract` has to keep naming
		// it after it becomes `mlops.extract`.
		n.UnlessEmpty = prefix(host.ID, n.UnlessEmpty)
		n.ForEach = prefix(host.ID, n.ForEach)

		n.ID = host.ID + "." + n.ID
		nodes = append(nodes, n)
	}

	edges := make([]dominio.Edge, 0, len(child.Edges))
	for _, e := range child.Edges {
		edges = append(edges, dominio.Edge{
			From:  host.ID + "." + e.From,
			To:    host.ID + "." + e.To,
			Label: e.Label,
		})
	}
	return nodes, edges
}

func prefix(host, value string) string {
	if value == "" {
		return ""
	}
	return host + "." + value
}

func known(bySlug map[string]dominio.Workflow) string {
	out := make([]string, 0, len(bySlug))
	for slug := range bySlug {
		out = append(out, slug)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
