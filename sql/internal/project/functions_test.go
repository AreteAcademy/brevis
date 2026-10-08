package project_test

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/project"
)

// A FUNCTION IS NOT A MODEL. `functions/<schema>/<name>.sql` holds a
// statement run before the models, verbatim: no header, no
// materialisation, no tests and no place in the graph.
//
// The spike's verdict put them in M2 by name. With `trim_make_empty_string_null`
// and `generate_surrogate_key` as SQL functions, 53% of a real dbt project's
// models need no template at all -- which is the whole premise of this tool
// holding for half of a project nobody wrote for it.
func TestAProjectReadsItsFunctions(t *testing.T) {
	p, err := project.Load("testdata/withfunctions", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Functions) != 1 {
		t.Fatalf("read %d functions", len(p.Functions))
	}
	f := p.Functions[0]
	if f.Schema != "util" || f.Name != "cents_to_dollars" {
		t.Errorf("read %s.%s", f.Schema, f.Name)
	}
	if !strings.Contains(f.SQL, "create or replace function") {
		t.Errorf("the body did not travel:\n%s", f.SQL)
	}
}

// AND IT IS NOT IN THE GRAPH. A call in a SELECT list is not a relation,
// so the model that uses it has no edge and no source for it -- which is
// the extractor's own behaviour and worth pinning, because the day it
// starts reporting one, every model using a function gains a dependency on
// something that is not a model.
func TestAFunctionIsNeitherAnEdgeNorASource(t *testing.T) {
	p, err := project.Load("testdata/withfunctions", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	const ref = "staging.prices"
	if _, isModel := p.Models["util.cents_to_dollars"]; isModel {
		t.Error("a function was read as a model")
	}
	if n := len(p.Edges[ref]) + len(p.Sources[ref]); n != 0 {
		t.Errorf("%s has %d dependencies: %v %v", ref, n, p.Edges[ref], p.Sources[ref])
	}
}

// A project with no `functions/` is not an error: it is the common case.
func TestAProjectWithoutFunctionsIsFine(t *testing.T) {
	p, err := project.Load("../run/testdata/project", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Functions) != 0 {
		t.Errorf("found %d functions where there is no directory", len(p.Functions))
	}
}
