// Package ci holds tests about this repository's own automation.
//
// IT HAS NO SOURCE AND IS NOT IMPORTED. A gate written in bash is checked by
// nothing, and the one failure this package exists for is not visible from
// inside any of them: whether the workflow that PUBLISHES remembers to run
// the gate that would have objected.
//
// That is not hypothetical here. `v0.17.0` was tagged on a commit whose CI
// was red, shipping manifests that deployed the previous engine --
// `image-pins-check.sh` had caught it, `Test & Lint` had gone red, and the
// release workflow never asked. The cure was one line in `release.yml`, and
// this is the test that would have asked for it.
package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const workflows = "../../.github/workflows"

// publishers are the workflows that put something where a stranger can get
// it, and the gates each must run BEFORE it does.
//
// `cli-docs-check.sh` is on every one of them, and that is the finding this
// map was written for: it reads the subcommands out of all three binaries and
// fails when a document has stopped mentioning one, it was repaired twice
// after `brevis gateway` shipped undocumented -- and NO RELEASE RAN IT. It
// was green on the push and never consulted at the moment a command actually
// reached somebody.
//
// `hub-overview-check.sh` belongs to whoever pushes a Docker Hub page: it
// fails when the page does not name the version in `VERSION`, so the page
// cannot announce something nobody can pull. The gateway's release ran it and
// the sql module's did not, which is the same hole one module over.
var publishers = map[string][]string{
	"release.yml": {
		"image-pins-check.sh",
		"generated-check.sh",
		"cli-docs-check.sh",
	},
	"release-sql.yml": {
		"sql-weight.sh",
		"cli-docs-check.sh",
		"hub-overview-check.sh",
	},
	"release-gateway.yml": {
		"hub-overview-check.sh",
		"gateway-consumer-check.sh",
		"cli-docs-check.sh",
	},
	"publish-sdk.yml": {
		"pruning-check.sh",
		"consumer-check.sh",
		"cli-docs-check.sh",
	},
	// The Python context library, which has no Go binary and therefore no
	// subcommands for `cli-docs-check.sh` to read. Its own gate runs twice,
	// on the declared minimum and on a current Python, and that is the whole
	// of what it owes.
	//
	// It is in this map because the DERIVED test found it: it fires on a tag
	// and nothing here had considered it, which is exactly the question that
	// test exists to ask.
	"publish-python.yml": {
		"python-check.sh",
	},
}

// A RELEASE RUNS THE GATES THAT WOULD HAVE OBJECTED TO IT.
func TestEveryPublisherRunsItsGates(t *testing.T) {
	for file, gates := range publishers {
		body, err := os.ReadFile(filepath.Join(workflows, file))
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		for _, gate := range gates {
			if !strings.Contains(string(body), gate) {
				t.Errorf("%s publishes without running %s", file, gate)
			}
		}
	}
}

// AND EVERY GATE NAMED HERE IS A SCRIPT THAT EXISTS.
//
// A typo in the map above would make the test pass by never matching
// anything real -- the same shape as a check that greps for a file it cannot
// find, which this repository has already paid for twice.
func TestEveryGateNamedIsAScriptThatExists(t *testing.T) {
	for file, gates := range publishers {
		for _, gate := range gates {
			if _, err := os.Stat(filepath.Join("../../.github/scripts", gate)); err != nil {
				t.Errorf("%s names %s, which is not a script here", file, gate)
			}
		}
	}
}

// AND EVERY WORKFLOW THAT PUBLISHES IS IN THE MAP.
//
// DERIVED, so a release added tomorrow has to be considered rather than
// quietly skipped. A workflow that fires on a tag is one that puts something
// where a stranger can get it; if a new one appears, this fails until
// somebody says which gates it owes.
func TestEveryWorkflowThatFiresOnATagIsAccountedFor(t *testing.T) {
	entries, err := os.ReadDir(workflows)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(workflows, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// `tags:` under `on:` is what makes a workflow a publisher. It is a
		// crude read of YAML and deliberately so: a parser here would be a
		// dependency this package does not want, and the string is exactly
		// what somebody writes when they add a release.
		head, _, _ := strings.Cut(string(body), "\njobs:")
		if !strings.Contains(head, "tags:") {
			continue
		}
		if _, known := publishers[e.Name()]; !known {
			t.Errorf("%s fires on a tag and no gates are declared for it", e.Name())
		}
	}
}

// THE SECURITY SCAN LOOKS AT EVERY MODULE, AND AT A PINNED VERSION.
//
// It ran `./sdk/...` and nothing else: the engine, the gateway and
// `brevis-sql` were never scanned. That is not an abstract gap -- pointed at
// the engine for the first time, it found an open redirect in `?next=`,
// which `internal/auth` now refuses and has a test for.
//
// `@master` is the other half. This repository pins templ and Tailwind
// because `releases/latest` made two developers generate different CSS; a
// scanner that changes on its own is the same problem with a worse failure
// mode, because its findings are what somebody decides to act on.
func TestTheSecurityScanCoversEveryModule(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(workflows, "test.yml"))
	if err != nil {
		t.Fatal(err)
	}
	scan := string(body)
	for _, module := range []string{"engine:.", "sdk:sdk", "gateway:gateway", "sql:sql"} {
		if !strings.Contains(scan, module) {
			t.Errorf("the security scan never enters %s", module)
		}
	}
	// AND NOT BY NAMING THEM IN ONE ARGUMENT LIST, which was measured and
	// does not work: from the root, `gosec ./... ./sdk/...` reports the
	// engine's findings and skips the other modules in silence. It has to
	// `cd` into each one.
	if strings.Contains(scan, "gosec -quiet -no-fail -fmt sarif ./sdk/...") {
		t.Error("the scan names other modules as paths, which reads none of them")
	}
	if strings.Contains(scan, "securego/gosec@master") {
		t.Error("the security scanner is pinned to @master, so its findings change on their own")
	}
}
