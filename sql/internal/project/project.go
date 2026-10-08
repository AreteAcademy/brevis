// Package project reads a directory of models and works out the order.
//
// An edge is INFERRED from the SQL and then shown, which is the shape the
// spike's verdict asked for by name: the extractor reads 99.3% of real
// Postgres and nobody should find the other 0.7% at three in the morning.
// `brevis-sql graph` prints what was inferred, and `depends_on:` overrides it.
package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AreteAcademy/brevis/sql/internal/model"
	"github.com/AreteAcademy/brevis/sql/internal/refs"
)

// Project is every model under a root, with the edges between them.
type Project struct {
	Models map[string]model.Model // by Ref(), `<schema>.<name>`
	// Edges[a] are the models a reads. Only models: a reference to something
	// this project does not define is a SOURCE, and sources have no edges
	// because nothing here builds them.
	Edges   map[string][]string
	Sources map[string][]string // by model, the references that are not models
}

// Load reads every `.sql` under root/models.
func Load(root, dialect string) (*Project, error) {
	dir := filepath.Join(root, "models")
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".sql") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s has no .sql models", dir)
	}
	sort.Strings(paths)

	p := &Project{
		Models:  map[string]model.Model{},
		Edges:   map[string][]string{},
		Sources: map[string][]string{},
	}

	for _, path := range paths {
		raw, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return nil, err
		}
		// The path as the PROJECT sees it, so an error names what somebody
		// typed rather than an absolute path from this machine.
		rel, _ := filepath.Rel(root, path)
		m, err := model.Parse(rel, raw)
		if err != nil {
			return nil, err
		}
		if was, clash := p.Models[m.Ref()]; clash {
			return nil, fmt.Errorf("%s and %s are both %s: two files cannot build one relation",
				was.Schema+"/"+was.Name+".sql", rel, m.Ref())
		}
		p.Models[m.Ref()] = m
	}

	for ref, m := range p.Models {
		read, err := refs.Of(dialect, m.SQL)
		if err != nil {
			return nil, fmt.Errorf("%s: reading its references: %w", ref, err)
		}
		// depends_on is ADDED to what was inferred, never instead of it: it
		// exists for the edge the extractor cannot see, and a model that
		// declares one still reads everything else it reads.
		seen := map[string]bool{}
		for _, r := range append(read, m.DependsOn...) {
			if seen[r] || r == ref {
				continue
			}
			seen[r] = true
			if _, isModel := p.Models[r]; isModel {
				p.Edges[ref] = append(p.Edges[ref], r)
			} else {
				p.Sources[ref] = append(p.Sources[ref], r)
			}
		}
		sort.Strings(p.Edges[ref])
		sort.Strings(p.Sources[ref])
	}
	return p, nil
}

// Order is the models in an order where everything a model reads comes first.
//
// A cycle is refused NAMING THE RING. "there is a cycle" sends somebody to
// read every model; the ring sends them to two.
func (p *Project) Order() ([]string, error) {
	const (
		unvisited = 0
		active    = 1
		done      = 2
	)
	state := map[string]int{}
	var out []string
	var stack []string

	refsSorted := make([]string, 0, len(p.Models))
	for r := range p.Models {
		refsSorted = append(refsSorted, r)
	}
	sort.Strings(refsSorted)

	var visit func(string) error
	visit = func(r string) error {
		switch state[r] {
		case done:
			return nil
		case active:
			// The ring, from where it closes back to itself.
			ring := stack
			for i, s := range stack {
				if s == r {
					ring = stack[i:]
					break
				}
			}
			return fmt.Errorf("these models read each other in a ring: %s -> %s",
				strings.Join(ring, " -> "), r)
		}
		state[r] = active
		stack = append(stack, r)
		for _, dep := range p.Edges[r] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[r] = done
		out = append(out, r)
		return nil
	}

	for _, r := range refsSorted {
		if err := visit(r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Select narrows to the models a `--select` expression names.
//
// `name` is one model; `name+` is it and everything downstream of it, which
// is the question somebody actually asks: "I changed this, what else moves".
func (p *Project) Select(expr string) ([]string, error) {
	if expr == "" {
		return p.Order()
	}
	want := strings.TrimSuffix(expr, "+")
	downstream := strings.HasSuffix(expr, "+")
	if _, ok := p.Models[want]; !ok {
		return nil, fmt.Errorf("--select %s: this project has no model %q", expr, want)
	}

	keep := map[string]bool{want: true}
	if downstream {
		// Repeated until nothing new is added: a model is downstream if it
		// reads anything already kept, which ripples.
		for changed := true; changed; {
			changed = false
			for ref, deps := range p.Edges {
				if keep[ref] {
					continue
				}
				for _, d := range deps {
					if keep[d] {
						keep[ref], changed = true, true
						break
					}
				}
			}
		}
	}

	order, err := p.Order()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range order {
		if keep[r] {
			out = append(out, r)
		}
	}
	return out, nil
}
