package pages

import (
	"context"
	"strings"
	"testing"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

func fieldHTML(t *testing.T, p wf.Param) string {
	t.Helper()
	var b strings.Builder
	if err := paramField(p).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// A TITLE IS THE LABEL, and the name stays beside it: the name is what the CLI
// and the template know the param by.
func TestAParamsTitleIsItsLabel(t *testing.T) {
	got := fieldHTML(t, wf.Param{Name: "load_full", Type: wf.ParamBool, Title: "Full load"})
	label := got[:strings.Index(got, "<select")]
	if !strings.Contains(label, ">Full load<") || !strings.Contains(label, ">load_full<") {
		t.Errorf("the label does not carry the title and the name:\n%s", label)
	}
	if strings.Index(label, "Full load") > strings.Index(label, "load_full<") {
		t.Error("the name comes before the title")
	}
}

// WITHOUT A TITLE THE NAME IS THE LABEL, as it always was.
func TestAParamWithNoTitleIsLabelledByItsName(t *testing.T) {
	got := fieldHTML(t, wf.Param{Name: "load_full", Type: wf.ParamBool})
	if strings.Count(got, "load_full<") != 1 {
		t.Errorf("the name is not the one label:\n%s", got)
	}
}

// A WARNING IS A HINT UNDER THE FIELD, and a param with none has no hint.
func TestAParamsWarningIsAHintUnderTheField(t *testing.T) {
	got := fieldHTML(t, wf.Param{Name: "load_full", Type: wf.ParamBool, Warn: "reprocesses everything; it costs"})
	at := strings.Index(got, "data-param-warn")
	if at < 0 || at < strings.Index(got, "</select>") || !strings.Contains(got[at:], "reprocesses everything; it costs") {
		t.Errorf("no warning under the field:\n%s", got)
	}
	if strings.Contains(fieldHTML(t, wf.Param{Name: "load_full", Type: wf.ParamBool}), "data-param-warn") {
		t.Error("a param with no warning has a hint")
	}
}
