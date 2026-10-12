package main

import (
	"encoding/json"
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
	topg "github.com/AreteAcademy/brevis/sdk/to/postgres"
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
	run := thisRun()

	sdk.Run(sdk.Pipeline{
		Name: "load " + partition,
		Source: sdk.Source{From: from.Files{
			Path:   filepath.Join(incoming, partition+".csv"),
			Format: sdk.FormatCSV,
		}},
		Transform: []sdk.Transformer{
			sdk.Compute("partition", func(map[string]any) (any, error) { return partition, nil }),
			// The run that landed the row. Staging is shared across runs,
			// and `check` and `report` read only their own run's rows.
			sdk.Compute(runColumn, func(map[string]any) (any, error) { return run, nil }),
		},
		Target: sdk.Target{
			// FLAT, not one directory per partition. The driver picks the file
			// name -- a batch is one object and a second load must not
			// overwrite the first -- so three instances writing here do not
			// collide, and the `partition` column above keeps the provenance
			// that the directory would have carried.
			//
			// And not one directory per RUN either, though a run reads only
			// its own rows: the directory is the target /data lists, and a
			// directory per run would be a new target every run.
			To:      to.Files{Path: landing + "/"},
			Columns: []string{"partition", "sku", "quantity", "price", runColumn},
		},
	})
	return nil // sdk.Run exits on failure; reaching here means it worked
}

// loadWarehouse lands the same partition in a REAL TABLE.
//
// It exists beside `load` rather than instead of it, and the pair is the
// point: a file destination and a table destination from one workflow, so
// `/data` shows both answers on one screen -- the table offers Preview and
// Query, and the directory correctly offers no tabs at all, because a bucket
// has no columns to draw.
//
// THE DSN COMES FROM THE ENVIRONMENT AND THE ENGINE CHOSE TO PASS IT.
// `BREVIS_TASK_ENV` is the only list a step's environment comes from -- a
// step gets PATH, HOME and what that names, and nothing else -- so a database
// credential reaching here is a deliberate act and never an inheritance.
func loadWarehouse() error {
	partition := os.Getenv("BREVIS_MAP_VALUE")
	if partition == "" {
		return fmt.Errorf("BREVIS_MAP_VALUE is empty: this step is meant to run " +
			"under `for_each:`, one instance per partition")
	}
	dsn := os.Getenv("WAREHOUSE_DSN")
	if dsn == "" {
		return fmt.Errorf("WAREHOUSE_DSN is not set. The scheduler passes it through " +
			"BREVIS_TASK_ENV, which is the only list a step's environment comes from")
	}

	sdk.Run(sdk.Pipeline{
		Name: "load_warehouse " + partition,
		Source: sdk.Source{From: from.Files{
			Path:   filepath.Join(incoming, partition+".csv"),
			Format: sdk.FormatCSV,
		}},
		Transform: []sdk.Transformer{
			sdk.Compute("partition", func(map[string]any) (any, error) { return partition, nil }),
		},
		Target: sdk.Target{
			// THE TABLE IS NOT CREATED HERE. `CreateTable` stays off, which
			// is the SDK's default: the schema is DDL in
			// `warehouse/001-sales.sql`, beside this file, where a column can
			// be read before it is relied on.
			To: topg.Table{DSN: dsn, Name: "sales.daily"},
			// Exactly what the table holds, in its order. The SDK refuses a
			// declared column the transform did not deliver AND a field the
			// declaration does not list -- "dropping data in silence is the
			// worst way to fail" -- so this list and that DDL have to agree,
			// and when they do not the run says which column.
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
	keep := ofRun(thisRun())
	var rows int
	for _, f := range found {
		n, err := rowsKept(f, keep)
		if err != nil {
			return err
		}
		rows += n
	}

	fmt.Printf("%d row(s) of this run, in %d staged file(s)\n", rows, len(found))

	// A boolean, decided HERE. The workflow reads one key and asks whether it
	// is empty -- no expression language, and the decision is testable with
	// this package's own test framework.
	return bctx.SetAll(map[string]any{"has_rows": rows > 0, "rows": rows})
}

// rowsKept counts the rows of a staged file that keep lets through. A line
// that is not a JSON object is no run's row.
func rowsKept(path string, keep sdk.Transformer) (int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a path this program wrote
	if err != nil {
		return 0, err
	}
	var n int
	for _, line := range strings.Split(string(raw), "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		if _, err := keep(row); err == nil {
			n++
		}
	}
	return n, nil
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
	day := reportDay(rc)
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
		// Only this run's rows: staging also holds the day's earlier runs,
		// and summing them would count the same orders once per run.
		Transform: []sdk.Transformer{ofRun(thisRun())},
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
//
// It counts the newest report of this run's day, the one this run wrote.
// Summing every report on disk made the number grow run after run, since
// `report` never overwrites and `cleanup` never removes one.
func qualityCount() error {
	newest, _, err := newestReport()
	if err != nil {
		return err
	}
	// No report is zero rows, not a failure: that zero is what the next
	// step's `unless_empty:` reads to skip itself.
	rows := 0
	if newest != "" {
		if rows, err = lines(newest); err != nil {
			return err
		}
	}
	fmt.Printf("the report has %d row(s)\n", rows)
	return bctx.Set("rows", rows)
}

// qualityFreshness refuses a report that is older than a day.
//
// It reads its own workflow's key, which is why it does not care whether it is
// running standalone or inlined: `unless_empty:` in the YAML resolves the
// prefix, and this code names nothing.
//
// It judges the same report qualityCount counted. Judging every report on
// disk failed every run once the directory was a day old -- on a report
// nobody was asking about.
func qualityFreshness() error {
	newest, at, err := newestReport()
	if err != nil {
		return err
	}
	if newest == "" {
		return fmt.Errorf("no report to check in %s",
			filepath.Join(reports, reportDay(sdk.RunContextFromEnv())))
	}
	day := filepath.Base(filepath.Dir(newest))
	if age := time.Since(at); age > 24*time.Hour {
		return fmt.Errorf("the newest report, for %s, is %s old", day, age.Round(time.Hour))
	}
	fmt.Printf("the newest report, for %s, is fresh\n", day)
	return nil
}

// newestReport is the report this run's `report` wrote, and when; "" when
// there is none.
//
// The data directory keeps every report a run ever wrote: `report` names each
// file itself so a second load never overwrites the first, and `cleanup` only
// tidies staging. So "the report" is the newest file in this run's day --
// never all of them, and never another day's, which a backfill running its
// days side by side may have written a minute later.
func newestReport() (string, time.Time, error) {
	day := reportDay(sdk.RunContextFromEnv())
	found, err := filepath.Glob(filepath.Join(reports, day, "*.ndjson"))
	if err != nil {
		return "", time.Time{}, err
	}
	var newest string
	var newestAt time.Time
	for _, f := range found {
		info, err := os.Stat(f)
		if err != nil {
			return "", time.Time{}, err
		}
		if info.ModTime().After(newestAt) {
			newest, newestAt = f, info.ModTime()
		}
	}
	return newest, newestAt, nil
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

// reportDay is the directory a run's report goes in: its slot's day, which
// does not move when the run is late or retried.
func reportDay(rc sdk.RunContext) string { return rc.Auto.Now().Format("2006-01-02") }

// thisRun is the run the rows of this execution belong to. By hand, with no
// engine, every execution is one run called "by-hand": the steps still agree
// with each other, which is all staging needs from the name.
func thisRun() string {
	if id := sdk.RunContextFromEnv().ID; id != "" {
		return id
	}
	return "by-hand"
}

// ofRun keeps the staged rows landed by run and drops the rest.
//
// Staging is shared and keeps a day of files ON PURPOSE -- a retry of this
// run needs what its earlier attempt landed -- so the second run of a day
// finds the first one's rows beside its own. A row written before rows
// carried their run belongs to no run and is dropped too.
func ofRun(run string) sdk.Transformer {
	return func(payload any) (any, error) {
		row, ok := payload.(map[string]any)
		if !ok || row[runColumn] != run {
			return nil, sdk.SkipRecord
		}
		return row, nil
	}
}

// runColumn is the column `load` stamps on every staged row: the run that
// landed it, as `partition` says which partition did.
const runColumn = "run_id"
