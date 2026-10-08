package run_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
	"github.com/AreteAcademy/brevis/sql/internal/project"
	"github.com/AreteAcademy/brevis/sql/internal/run"
)

// FIFTY MODELS ON A REAL POSTGRES, UNDER TEN SECONDS. #62's acceptance
// criterion, and it is about THIS TOOL's overhead rather than the
// warehouse's: fifty trivial models are nothing for Postgres, so what the
// budget measures is how many round trips brevis-sql makes per model and
// whether anything in the loop is accidentally quadratic.
//
// The shape is a project rather than fifty unrelated SELECTs: ten sources,
// then four layers of ten reading the one below. That is what makes the
// graph, the topological order and `Select` do real work, and it is the
// shape a dbt project actually has.
func TestFiftyModelsBuildUnderTenSeconds(t *testing.T) {
	ctx, d, conn := live(t)

	const schema = "bvs_scale_it"
	t.Cleanup(func() {
		_ = conn.Exec(t.Context(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	root := fiftyModels(t, schema)
	p, err := project.Load(root, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != 50 {
		t.Fatalf("the fixture made %d models", len(p.Models))
	}
	order, err := p.Select("")
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	res, err := run.Build(ctx, d, conn, p, order, run.Options{})
	took := time.Since(started)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if len(res.Built) != 50 {
		t.Fatalf("built %d", len(res.Built))
	}

	t.Logf("fifty models in %s (%s each)", took.Round(time.Millisecond),
		(took / 50).Round(time.Millisecond))
	if took > 10*time.Second {
		t.Errorf("fifty models took %s, and #62's budget is 10s.\n"+
			"    This measures brevis-sql's overhead, not Postgres's: look for a\n"+
			"    round trip added per model, or something quadratic in the loop.", took)
	}
}

// fiftyModels writes ten sources and four layers of ten over them.
func fiftyModels(t *testing.T, schema string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "models", schema)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".sql"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for i := range 10 {
		write(fmt.Sprintf("src_%02d", i), fmt.Sprintf("SELECT %d AS n", i))
	}
	for layer := 1; layer <= 4; layer++ {
		for i := range 10 {
			below := fmt.Sprintf("src_%02d", i)
			if layer > 1 {
				below = fmt.Sprintf("l%d_%02d", layer-1, i)
			}
			// Every fourth one is a table, so the row count -- an extra
			// query per table -- is in the measurement rather than
			// outside it.
			header := ""
			if i%4 == 0 {
				header = "/* brevis\nmaterialized: table\n*/\n"
			}
			write(fmt.Sprintf("l%d_%02d", layer, i), fmt.Sprintf(
				"%sSELECT n + %d AS n FROM %s.%s", header, layer, schema, below))
		}
	}
	return root
}

// THE ROUND TRIPS, which is the half of the budget that can actually fail.
//
// Fifty models take 81 milliseconds against a local Postgres -- so the ten
// second budget above has a hundredfold of headroom, and a check with a
// hundredfold of headroom is decoration. What WOULD regress is the number
// of times this loop talks to the warehouse per model, and that is exactly
// what the budget is a proxy for: against a warehouse across a network, or
// BigQuery where a trivial query is two seconds, one extra round trip per
// model is the difference between a minute and three.
//
// Counted rather than timed, so it fails deterministically and on a laptop.
//
// Today, per model: one KindOf, one or two build statements, and for a
// table one COUNT for the `landed` line. Plus one CREATE SCHEMA for the
// whole run. Anything that adds another is a decision, and this is where
// it gets made.
func TestFiftyModelsCostAtMostThreeRoundTripsEach(t *testing.T) {
	const schema = "bvs_scale_spy"
	p, err := project.Load(fiftyModels(t, schema), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	order, err := p.Select("")
	if err != nil {
		t.Fatal(err)
	}

	spy := &spyConn{}
	if _, err := run.Build(t.Context(), postgres.Dialect{}, spy, p, order, run.Options{}); err != nil {
		t.Fatal(err)
	}

	// One per schema, then at most three per model.
	ceiling := 1 + 3*len(p.Models)
	if spy.trips > ceiling {
		t.Errorf("fifty models cost %d round trips, over %d (1 + 3 per model).\n"+
			"    Something in the loop started asking the warehouse one more\n"+
			"    question per model. On BigQuery that is two seconds each.",
			spy.trips, ceiling)
	}
	t.Logf("%d round trips for %d models (%.1f each)",
		spy.trips, len(p.Models), float64(spy.trips)/float64(len(p.Models)))
}
