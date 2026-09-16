package sdk

import (
	"os"
	"reflect"
	"testing"
)

// The case this exists for: a value needed to BUILD the pipeline.
//
// A consumer was carrying a hand-rolled takeFlag that pulled `-ufs SP,RJ` out
// of os.Args and rewrote os.Args so sdk.Run would not refuse it -- because
// Pipeline.Flags is parsed inside Execute, which is too late for a value that
// decides what the pipeline IS.
func TestParamListIsReadableBeforeRun(t *testing.T) {
	t.Setenv("BREVIS_RUN_PARAMS", `{"ufs":"SP,RJ,MG"}`)
	if got := ParamList("ufs"); !reflect.DeepEqual(got, []string{"SP", "RJ", "MG"}) {
		t.Errorf("ParamList = %#v", got)
	}
	if got := Param("ufs"); got != "SP,RJ,MG" {
		t.Errorf("Param = %q", got)
	}
}

func TestParamFromTheCommandLine(t *testing.T) {
	casos := []struct {
		why  string
		args []string
		want string
	}{
		{"separated", []string{"-param", "ufs=SP,RJ"}, "SP,RJ"},
		{"joined", []string{"-param=ufs=SP,RJ"}, "SP,RJ"},
		{"two dashes", []string{"--param", "ufs=SP"}, "SP"},
		{"two dashes, joined", []string{"--param=ufs=SP"}, "SP"},
		{"among other flags", []string{"-v", "-param", "ufs=SP", "-dry-run"}, "SP"},
		{"another param's value is not this one's", []string{"-param", "since=2026-01-01"}, ""},
		{"the last one wins", []string{"-param", "ufs=SP", "-param", "ufs=RJ"}, "RJ"},
		{"a trailing -param with no value does not panic", []string{"-param"}, ""},
		{"absent", []string{"-v"}, ""},
	}
	for _, c := range casos {
		if got := paramFromArgs(c.args, "ufs"); got != c.want {
			t.Errorf("%s: paramFromArgs(%v) = %q, want %q", c.why, c.args, got, c.want)
		}
	}
}

// Under the engine the environment IS the value: a `-param` left in a manifest
// must not quietly override what the operator typed in the trigger form.
func TestTheEnvironmentWinsOverTheFlag(t *testing.T) {
	t.Setenv("BREVIS_RUN_PARAMS", `{"ufs":"SP"}`)
	original := os.Args
	defer func() { os.Args = original }()
	os.Args = []string{"fetcher", "-param", "ufs=RJ"}

	if got := Param("ufs"); got != "SP" {
		t.Errorf("Param = %q, want the engine's value", got)
	}
}

// And with no engine, the flag is what there is -- which is the laptop half of
// a fetcher's life.
func TestTheFlagServesWhenThereIsNoEngine(t *testing.T) {
	t.Setenv("BREVIS_RUN_PARAMS", "")
	original := os.Args
	defer func() { os.Args = original }()
	os.Args = []string{"fetcher", "-param", "ufs=SP,RJ"}

	if got := ParamList("ufs"); !reflect.DeepEqual(got, []string{"SP", "RJ"}) {
		t.Errorf("ParamList = %#v", got)
	}
}

func TestParamsAreEmptyOutsideEverything(t *testing.T) {
	t.Setenv("BREVIS_RUN_PARAMS", "")
	original := os.Args
	defer func() { os.Args = original }()
	os.Args = []string{"fetcher"}

	if got := Param("ufs"); got != "" {
		t.Errorf("Param = %q, want empty", got)
	}
	if got := ParamList("ufs"); got != nil {
		t.Errorf("ParamList = %#v, want nil", got)
	}
}

// -param is a real flag too, so `-h` lists it and Execute does not refuse it as
// unknown -- and what it collects reaches p.Run.Params, so the value reads the
// same inside the pipeline as outside it.
func TestTheParamFlagReachesRunParams(t *testing.T) {
	p := paramFlags{}
	if err := p.Set("ufs=SP,RJ"); err != nil {
		t.Fatal(err)
	}
	if p["ufs"] != "SP,RJ" {
		t.Errorf("Set left %#v", p)
	}
	// Forgetting the value is an error, not a param named after the whole token.
	if err := p.Set("ufs"); err == nil {
		t.Error("-param ufs was accepted with no value")
	}
	if err := p.Set("=SP"); err == nil {
		t.Error("-param =SP was accepted with no name")
	}
}
