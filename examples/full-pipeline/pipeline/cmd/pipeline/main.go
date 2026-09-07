// Command pipeline is every step of the daily_sales workflow, in one binary.
//
// One binary and not eight, because that is what a real deployment looks like:
// one image with several entrypoints. The workflow's `run:` lines pick the
// subcommand.
//
//	pipeline discover           lists the partitions and publishes them
//	pipeline load               one instance per partition (for_each)
//	pipeline check              counts what landed and publishes a boolean
//	pipeline report             the summary, skipped when there is nothing
//	pipeline quality-count      the reusable workflow's first step
//	pipeline quality-freshness  and its second
//	pipeline notify             when: any_failed
//	pipeline cleanup            when: all_done
package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Where the data lives. One directory per stage, so what each step did is
// visible on disk after the run rather than only in a log.
var (
	dataDir  = env("DATA_DIR", "/data")
	incoming = filepath.Join(dataDir, "incoming") // the CSVs to load
	landing  = filepath.Join(dataDir, "landing")  // what `load` wrote
	reports  = filepath.Join(dataDir, "reports")  // what `report` wrote
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pipeline <discover|load|check|report|"+
			"quality-count|quality-freshness|notify|cleanup>")
		os.Exit(2)
	}

	commands := map[string]func() error{
		"discover":          discover,
		"load":              load,
		"check":             check,
		"report":            report,
		"quality-count":     qualityCount,
		"quality-freshness": qualityFreshness,
		"notify":            notify,
		"cleanup":           cleanup,
	}

	run, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown step %q; valid: %s\n", os.Args[1], names(commands))
		os.Exit(2)
	}
	if err := run(); err != nil {
		// Non-zero and a message on stderr. The engine records both: the exit
		// code lands on the step's row and the last lines of this stream are
		// what the failure alert carries.
		slog.Error(os.Args[1]+" failed", "error", err)
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func names(m map[string]func() error) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
