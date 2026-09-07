package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// The version is READ from the binary, not typed here.
//
// It was the literal "0.1.0" while VERSION said 0.7.0, and nothing set it
// through -ldflags -- so `brevis-sdk version` had been wrong since the split,
// and would go wrong again the first time somebody forgot to bump it.
//
// runtime/debug is what `go install ...@v0.7.0` fills in, and it is the same
// mechanism sdk.SDKVersion uses for the badge on the graph. Nobody types it, so
// it cannot be stale.
func buildVersion() (string, string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "devel", ""
	}
	version := info.Main.Version
	if version == "" || version == "(devel)" {
		// A checkout or a `go build`. "devel" is the truth, and telling it
		// apart from a release artifact is exactly what somebody reporting odd
		// behaviour needs.
		version = "devel"
	}
	var commit string
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			commit = s.Value
			if len(commit) > 12 {
				commit = commit[:12]
			}
		}
	}
	return version, commit
}

var version, commit = buildVersion()

var rootCmd = &cobra.Command{
	Use:     "brevis-sdk",
	Short:   "Extract and load data to BigQuery",
	Long:    "Brevis CLI: High-performance data extraction and loading. No schema opinions.",
	Version: fmt.Sprintf("%s (%s)", version, commit),
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(
		extractCmd,
		loadCmd,
		runCmd,
		versionCmd,
	)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
