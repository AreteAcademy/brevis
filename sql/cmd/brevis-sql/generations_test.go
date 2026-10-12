package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/generation"
)

// A GENERATION THAT CANNOT BE WRITTEN IS A WARNING, NEVER A FAILED BUILD.
// The models are built; a cache that goes stale until its ceiling is a
// smaller harm than a pipeline that fails because Redis blinked -- and the
// warning is on the same stream as the build's report, where it is read.
func TestAGenerationThatCannotBeWrittenWarnsAndTheBuildStands(t *testing.T) {
	for name, value := range map[string]string{
		"an unset variable":  "",
		"not an address":     "http://x",
		"an unreachable one": "127.0.0.1:1",
	} {
		t.Setenv("TEST_GENERATIONS", value)
		var out bytes.Buffer
		writeGenerations(&out, "TEST_GENERATIONS", []string{"marts.a"})
		if !strings.Contains(out.String(), "warning") || !strings.Contains(out.String(), "TEST_GENERATIONS") {
			t.Errorf("%s: said %q", name, out.String())
		}
	}
}

func TestGenerationBumpNeedsAModelAndTheFlag(t *testing.T) {
	for _, args := range [][]string{
		{"generation"}, {"generation", "bump"}, {"generation", "bump", "marts.a"},
		{"generation", "frob", "--generations-from", "X", "marts.a"},
		{"generation", "bump", "--generations-from", "X", "not-a-ref"},
	} {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%v ran", args)
		}
	}
}

// AGAINST A REAL REDIS: a bump is read back as the generation it wrote, and
// a second bump is a different one.
func TestIntegrationGenerationBumpWritesANewGeneration(t *testing.T) {
	addr := os.Getenv("BREVIS_SQL_IT_REDIS")
	if addr == "" {
		t.Skip("BREVIS_SQL_IT_REDIS not set")
	}
	t.Setenv("TEST_GENERATIONS", addr)
	model := "it_" + strings.ReplaceAll(t.Name(), "/", "_") + ".m"
	read := func() string {
		c, _ := generation.Open(addr)
		defer c.Close()
		got, err := generation.Read(context.Background(), c, []string{model})
		if err != nil {
			t.Fatal(err)
		}
		return got[0]
	}
	if err := run([]string{"generation", "bump", "--generations-from", "TEST_GENERATIONS", model}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	first := read()
	if err := run([]string{"generation", "bump", "--generations-from", "TEST_GENERATIONS", model}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if second := read(); first == "" || second == first {
		t.Errorf("generations %q then %q", first, second)
	}
}
