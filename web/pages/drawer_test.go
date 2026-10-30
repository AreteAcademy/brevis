package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/web/i18n"
)

func everyType() wf.Workflow {
	return wf.Workflow{Slug: "daily", Schedule: "0 9 * * *", Params: []wf.Param{
		{Name: "since", Type: wf.ParamString, Pattern: `^\d{4}-\d{2}-\d{2}$`, Description: "the first day"},
		{Name: "max_queries", Type: wf.ParamInteger, Default: "300", Title: "Queries", Description: "How many it may run.", Group: "Cost", Uses: "bigquery"},
		{Name: "full", Type: wf.ParamBool, Default: "false", Title: "Full load", Group: "Mode", Warn: "rebuilds silver and gold"},
		{Name: "mode", Type: wf.ParamString, Enum: []string{"fast", "slow"}, Default: "fast", Group: "Mode"},
		{Name: "layers", Type: "list|string", Enum: []string{"bronze", "silver"}, Default: "bronze", Group: "Cost"},
		{Name: "tables", Type: "list|string", Default: "users"},
	}}
}

func drawerHTML(t *testing.T, w wf.Workflow, d drawerText) string {
	t.Helper()
	var b strings.Builder
	if err := runDrawer(w, d).Render(i18n.WithLang(context.Background(), "en"), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// fieldOf is a param's card in the drawer.
func fieldOf(t *testing.T, html, name string) string {
	t.Helper()
	at := strings.Index(html, `data-param="`+name+`"`)
	if at < 0 {
		t.Fatalf("no field for %s", name)
	}
	rest := html[at:]
	if end := strings.Index(rest[1:], `data-param="`); end > 0 {
		return rest[:end+1]
	}
	return rest
}

// THE DRAWER ASKS FOR EACH PARAMETER WITH ITS OWN CONTROL (screen-workflows
// T9), and every one of them works with no script: a boolean is a select a
// switch drives, an integer a number between − and +, a pattern a field that
// shows it, an enum a select, a list with an enum its boxes, a free list its
// chips.
func TestTheDrawerHasAControlForEachType(t *testing.T) {
	html := drawerHTML(t, everyType(), drawerText{Slot: &RunNow{Slug: "daily", Cron: "0 9 * * *", Now: time.Now()}})
	for name, wants := range map[string][]string{
		"full":        {`<select name="param.full"`, "data-no-js", `data-bool-for="param.full"`, `role="switch"`},
		"max_queries": {`type="number" name="param.max_queries"`, `data-step="-1"`, `data-step="1"`},
		"since":       {`name="param.since"`, `data-pattern="^\d{4}-\d{2}-\d{2}$"`, `^\d{4}-\d{2}-\d{2}$</code>`},
		"mode":        {`<select name="param.mode"`, `<option value="slow">`},
		"layers":      {`type="checkbox" name="param.layers" value="silver"`, `type="hidden" name="param.layers" value=""`},
		"tables":      {`data-lista="string"`},
	} {
		f := fieldOf(t, html, name)
		for _, want := range wants {
			if !strings.Contains(f, want) {
				t.Errorf("%s has no %s", name, want)
			}
		}
	}
}

// A FIELD SAYS WHAT IT IS before what it takes: the name, a mark that lights
// when it differs from the default, the connector it bills, "default X", the
// title and the description; under it, its error and its warning, which the
// script shows when they apply.
func TestADrawerFieldSaysWhatItIs(t *testing.T) {
	html := drawerHTML(t, everyType(), drawerText{})
	f := fieldOf(t, html, "max_queries")
	for _, want := range []string{
		`data-field="param.max_queries"`, `data-default="300"`, `data-type="integer"`,
		">max_queries<", "data-param-changed", `aria-label="Google BigQuery"`, "default 300",
		">Queries.<", "How many it may run.", "data-param-error",
	} {
		if !strings.Contains(f, want) {
			t.Errorf("max_queries has no %s", want)
		}
	}
	if full := fieldOf(t, html, "full"); !strings.Contains(full, "data-param-warn") || !strings.Contains(full, "rebuilds silver and gold") {
		t.Error("full's warning is not under it")
	}
	if strings.Contains(fieldOf(t, html, "tables"), "data-param-warn") {
		t.Error("a param with no warning has one")
	}
}

// THE PARAMETERS ARE GROUPED in the order each group first appears; the ones
// with no group come first, under no heading.
func TestTheDrawerGroupsTheParameters(t *testing.T) {
	html := drawerHTML(t, everyType(), drawerText{})
	order := []string{`data-param="since"`, `data-param="tables"`, `data-param-group="Cost"`, `data-param="max_queries"`,
		`data-param="layers"`, `data-param-group="Mode"`, `data-param="full"`, `data-param="mode"`}
	last := -1
	for _, mark := range order {
		at := strings.Index(html, mark)
		if at < 0 || at < last {
			t.Fatalf("%s is out of order (at %d, after %d)", mark, at, last)
		}
		last = at
	}
}

// THE DRAWER SAYS WHAT IT WILL SEND, and runs it: the payload and the same by
// the CLI, the slot when the workflow has a schedule, the count of what
// changed, Restore defaults and Run -- posting with its script so a refused
// value stays in the drawer.
func TestTheDrawerSaysWhatItWillSend(t *testing.T) {
	html := drawerHTML(t, everyType(), drawerText{Project: "zarv", Slot: &RunNow{Slug: "daily", Cron: "0 9 * * *", Now: time.Now()}})
	for _, want := range []string{
		"data-run-form", "data-run-now", `data-slug="daily"`, `data-project="zarv"`,
		`name="when" value="slot"`, "<details open", "data-run-preview", "data-run-cli",
		"data-run-status", "data-run-reset", "data-run-error", "⌘↵",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the drawer has no %s", want)
		}
	}
	if rerun := drawerHTML(t, everyType(), drawerText{Rerun: true}); strings.Contains(rerun, `name="when"`) {
		t.Error("a rerun offers a slot")
	}
}

// THE DRAWER SPEAKS THE READER'S LANGUAGE, the Rerun's too.
func TestTheDrawerHasNoEnglishLeft(t *testing.T) {
	var b strings.Builder
	ctx := i18n.WithLang(context.Background(), "pt-BR")
	slot := time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)
	for _, d := range []drawerText{
		{Project: "zarv", Slot: &RunNow{Slug: "daily", Cron: "0 9 * * *", Now: time.Now()}},
		{Rerun: true, NoteSlot: &slot, Dropped: []string{"gone"}},
	} {
		if err := runDrawer(everyType(), d).Render(ctx, &b); err != nil {
			t.Fatal(err)
		}
	}
	text := visibleText(b.String())
	for _, english := range []string{
		"Run with parameters", "Rerun with parameters", "Creates a manual run", "Scheduled runs",
		"Runs now", "not in the current definition", "Payload sent", "The same by the CLI",
		"Restore defaults", "default", "Has to match", "required", "Now", "Another slot", "one, two, three",
	} {
		if strings.Contains(text, english) {
			t.Errorf("the Portuguese drawer still says %q", english)
		}
	}
}
