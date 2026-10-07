package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const comTypo = `name: warn_demo
type: dag
steps:
  - id: one
    run: echo hi
    hosts: vendor-box
`

// A helper called from nowhere looks exactly like a helper that works.
//
// UnknownFields has seven tests of its own and every one of them still passes
// with the call deleted from validate, from publish and from run. This
// repository has shipped that twice already -- the Kubernetes return path and
// Runner.ContextDir, both green and both inert -- so the wiring is asserted
// where the wiring is.
func TestEveryCommandThatReadsAWorkflowSaysWhatItIgnored(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "w.yaml")
	if err := os.WriteFile(file, []byte(comTypo), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{"validate", func(t *testing.T) {
			cmd := cmdValidate()
			cmd.SetArgs([]string{dir})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("validate refused a file that only has a stray key: %v", err)
			}
		}},
		{"publish", func(t *testing.T) {
			// What `publish` reads every file through.
			if _, err := readAll([]string{file}); err != nil {
				t.Fatal(err)
			}
		}},
		{"run", func(t *testing.T) {
			cmd := cmdRun()
			cmd.SetArgs([]string{file})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("run refused it: %v", err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := capturingStderr(t, func() { c.run(t) })
			if !strings.Contains(got, "`hosts` is not a field of a step") {
				t.Errorf("%s read the file and said nothing about the line it dropped.\nstderr: %q", c.name, got)
			}
		})
	}
}

func capturingStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	f()
	_ = w.Close()
	return <-done
}
