package execution_test

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution"
)

func TestRenderingSubstitutesTheParams(t *testing.T) {
	out, err := execution.Render(
		`dbt build --vars '{"load_full":"{{ .load_full }}"}' --select bronze_x+`,
		map[string]string{"load_full": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"load_full":"true"`) {
		t.Errorf("it did not substitute: %s", out)
	}
}

// A param with a typo in the YAML becomes an empty string without
// `missingkey=error`, and the command comes out quietly wrong: `--select ` with
// no target, `--date` with no date. Failing here, naming it, is what prevents
// that.
func TestAParamMissingFromTheTemplateFails(t *testing.T) {
	_, err := execution.Render("echo {{ .lod_full }}", map[string]string{"load_full": "true"})
	if err == nil {
		t.Fatal("a template with the wrong name got through")
	}
	if !strings.Contains(err.Error(), "load_full") {
		t.Errorf("the error does not say what exists: %v", err)
	}
}

func TestTheConvertersConditional(t *testing.T) {
	cmd := `dbt build{{ if eq .full_refresh "true" }} --full-refresh{{ end }} --select x+`

	com, err := execution.Render(cmd, map[string]string{"full_refresh": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(com, "--full-refresh") {
		t.Errorf("the flag did not go in: %s", com)
	}

	sem, err := execution.Render(cmd, map[string]string{"full_refresh": "false"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sem, "--full-refresh") {
		t.Errorf("the flag went in with false: %s", sem)
	}
}

// A command with no template does not go through the engine: that is most of
// them, and skipping the parse saves work on every step of every run.
func TestACommandWithNoTemplatePassesThrough(t *testing.T) {
	cmd := `sh -c 'echo {oi} && ls | grep x'`
	out, err := execution.Render(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != cmd {
		t.Errorf("it touched a command with no template:\n%s\n%s", cmd, out)
	}
}

// A multi-line script has to come out whole: collapsing it into one line would
// make the
// primeiro `#` comentar o resto.
func TestAMultiLineScriptSurvives(t *testing.T) {
	cmd := "set -e\n# comentario\npython3 -m x --date {{ .data }}\necho fim"
	out, err := execution.Render(cmd, map[string]string{"data": "2026-09-01"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "\n") != 3 {
		t.Errorf("it lost line breaks:\n%s", out)
	}
	if !strings.HasSuffix(out, "echo fim") {
		t.Errorf("o fim do script sumiu:\n%s", out)
	}
}
