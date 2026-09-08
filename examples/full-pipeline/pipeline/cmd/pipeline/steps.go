package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	bctx "github.com/AreteAcademy/brevis/sdk/context"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to"
)

// discover lists what there is to load and publishes it.
//
// It is the step that makes `for_each:` possible: nothing in the YAML knows how
// many partitions exist, and this is where that number comes from. The list
// travels in the context, which is capped at 4 KB -- so the fan-out below is
// bounded without the workflow inventing a limit.
func discover() error {
	found, err := filepath.Glob(filepath.Join(incoming, "*.csv"))
	if err != nil {
		return err
	}
	sort.Strings(found) // the same input has to produce the same run

	partitions := make([]string, 0, len(found))
	for _, p := range found {
		partitions = append(partitions, strings.TrimSuffix(filepath.Base(p), ".csv"))
	}

	fmt.Printf("found %d partition(s) in %s: %s\n",
		len(partitions), incoming, strings.Join(partitions, ", "))

	// Published for the steps below. `load` maps over this key.
	return bctx.SetAll(map[string]any{
		"partitions": partitions,
		"source_dir": incoming,
	})
}

// load is ONE instance per partition.
//
// The engine sets BREVIS_MAP_VALUE to the element and BREVIS_MAP_INDEX to its
// position, and gives each instance a row, a retry and an exit code of its own.
// On the graph it is one node with [3] on it.
//
// It uses sdk.Run rather than the Extract/Transform/Load trio, so the phases
// this pipeline goes through appear on the screen WHILE it runs -- a forty
// minute step that only says "running" is a step nobody can help.
func load() error {
	partition := os.Getenv("BREVIS_MAP_VALUE")
	if partition == "" {
		return fmt.Errorf("BREVIS_MAP_VALUE is empty: this step is meant to run " +
			"under `for_each:`, one instance per partition")
	}
	if err := os.MkdirAll(landing, 0o750); err != nil {
		return err
	}

	sdk.Run(sdk.Pipeline{
		Name: "load " + partition,
		Source: sdk.Source{From: from.Files{
			Path:   filepath.Join(incoming, partition+".csv"),
			Format: sdk.FormatCSV,
		}},
		Transform: []sdk.Transformer{
			sdk.Compute("partition", func(map[string]any) (any, error) { return partition, nil }),
		},
		Target: sdk.Target{
			// FLAT, not one directory per partition. The driver picks the file
			// name -- a batch is one object and a second load must not
			// overwrite the first -- so three instances writing here do not
			// collide, and the `partition` column above keeps the provenance
			// that the directory would have carried.
			To:      to.Files{Path: landing + "/"},
			Columns: []string{"partition", "sku", "quantity", "price"},
		},
	})
	return nil // sdk.Run exits on failure; reaching here means it worked
}

// check counts what landed and publishes a boolean.
//
// It exists BECAUSE `load` is mapped: four instances cannot share one context
// key, so a mapped step publishes nothing downstream. Somebody has to look at
// the result, and that somebody is a step.
func check() error {
	found, err := filepath.Glob(filepath.Join(landing, "*.ndjson"))
	if err != nil {
		return err
	}
	var rows int
	for _, f := range found {
		n, err := lines(f)
		if err != nil {
			return err
		}
		rows += n
	}

	fmt.Printf("%d row(s) across %d file(s)\n", rows, len(found))

	// A boolean, decided HERE. The workflow reads one key and asks whether it
	// is empty -- no expression language, and the decision is testable with
	// this package's own test framework.
	return bctx.SetAll(map[string]any{"has_rows": rows > 0, "rows": rows})
}

// report folds everything that landed into one row per SKU.
//
// It is `skipped` when check published has_rows=false, and so is everything
// below it. Delete the CSVs in data/incoming and run again to watch that
// happen.
func report() error {
	if err := os.MkdirAll(reports, 0o750); err != nil {
		return err
	}

	// The AUTO PARAMS, and the reason this step reads them instead of calling
	// time.Now().
	//
	// This report is named after the day it covers. With now(), a run the queue
	// delayed past midnight writes yesterday's data into today's file -- and a
	// retry at nine the next morning writes it into a third one. With
	// Auto.Now() the name is the SLOT: it does not move when the run is late
	// and does not move when the run is retried.
	//
	// Nothing sets this up -- no `params:` block, nothing in the YAML. Every
	// run has them. Run this program by hand and Auto.Now() is simply the wall
	// clock, which is why there is no branch here for local development.
	rc := sdk.RunContextFromEnv()
	day := rc.Auto.Now().Format("2006-01-02")
	if start, end, ok := rc.Auto.Window(); ok {
		fmt.Printf("reporting on the window [%s, %s)\n",
			start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	if rc.Auto.PreviousError {
		fmt.Println("the previous run of this workflow did not succeed; " +
			"a real pipeline would widen its window to catch up")
	}

	// Reduce folds the stream into groups BEFORE it reaches the destination,
	// and memory stays proportional to the number of groups rather than to the
	// number of rows. A million orders across two hundred SKUs is two hundred
	// rows in memory.
	sdk.Run(sdk.Pipeline{
		Name:   "daily report",
		Source: sdk.Source{From: from.Files{Path: filepath.Join(landing, "*.ndjson")}},
		Reduce: &sdk.Reduce{
			By: sdk.GroupBy("sku"),
			Agg: map[string]sdk.Aggregator{
				"orders": sdk.Count(),
				"units":  sdk.Sum("quantity"),
			},
		},
		Target: sdk.Target{
			// The day is a DIRECTORY, not a file name: to.Files names the
			// object itself, on purpose, so that a second load never
			// overwrites the first. `reports/2026-09-08/parte-....ndjson`.
			To:      to.Files{Path: filepath.Join(reports, day) + "/"},
			Columns: []string{"sku", "orders", "units"},
		},
	})
	return nil
}

// qualityCount is the reusable workflow's first step.
//
// Standalone it publishes `count_rows.rows`; expanded into daily_sales it
// publishes `quality.count_rows.rows`, and the `unless_empty:` in the next step
// moves with it. The same file works both ways without being written twice.
func qualityCount() error {
	found, err := filepath.Glob(filepath.Join(reports, "*", "*.ndjson"))
	if err != nil {
		return err
	}
	var rows int
	for _, f := range found {
		n, err := lines(f)
		if err != nil {
			return err
		}
		rows += n
	}
	fmt.Printf("the report has %d row(s)\n", rows)
	return bctx.Set("rows", rows)
}

// qualityFreshness refuses a report that is older than a day.
//
// It reads its own workflow's key, which is why it does not care whether it is
// running standalone or inlined: `unless_empty:` in the YAML resolves the
// prefix, and this code names nothing.
func qualityFreshness() error {
	found, err := filepath.Glob(filepath.Join(reports, "*", "*.ndjson"))
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return fmt.Errorf("no report to check in %s", reports)
	}
	for _, f := range found {
		info, err := os.Stat(f)
		if err != nil {
			return err
		}
		if age := time.Since(info.ModTime()); age > 24*time.Hour {
			return fmt.Errorf("the report for %s is %s old",
				filepath.Base(filepath.Dir(f)), age.Round(time.Hour))
		}
	}
	fmt.Printf("%d report file(s), all fresh\n", len(found))
	return nil
}

// notify runs precisely when something above it failed.
//
// On a good night it never runs and the graph shows it `skipped` -- a state of
// its own, drawn in its own colour, which is not a failure and not a success.
func notify() error {
	fmt.Println("something above this step failed; this is where a page would go out")
	return nil
}

// cleanup runs either way -- the case that is impossible without trigger rules,
// because the staging directory has to be tidied whether or not the report
// worked.
//
// It removes what PREVIOUS runs staged, and never what this one did. That is
// not fussiness: a run-level retry re-runs the steps that failed, so a cleanup
// that deleted this run's landing files would delete exactly what the retry of
// `report` needs, and the run would fail forever on "no such file or
// directory". A destructive `when: all_done` beside a retry is a trap, and this
// is what stepping around it looks like.
func cleanup() error {
	found, err := filepath.Glob(filepath.Join(landing, "*.ndjson"))
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-24 * time.Hour)

	var removed int
	for _, f := range found {
		info, err := os.Stat(f)
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			continue // this run's, or one recent enough to still be retried
		}
		if err := os.Remove(f); err != nil {
			return err
		}
		removed++
	}
	fmt.Printf("%d of %d staged file(s) were older than a day and were removed\n",
		removed, len(found))
	return nil
}

func lines(path string) (int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a path this program wrote
	if err != nil {
		return 0, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return 0, nil
	}
	return len(strings.Split(trimmed, "\n")), nil
}

func removeAll(dir string) (int, error) {
	found, err := filepath.Glob(filepath.Join(dir, "*", "*.ndjson"))
	if err != nil {
		return 0, err
	}
	for _, f := range found {
		if err := os.Remove(f); err != nil {
			return 0, err
		}
	}
	return len(found), os.RemoveAll(dir)
}
