package pages

import (
	"context"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

// A COLUMN WITH NOTHING IN IT IS MARKED (screen-load-v2 LV4): null in every
// row of the preview, and only then -- a preview with no rows proves
// nothing about any column.
func TestNullColumnsAreTheOnesNullInEveryRow(t *testing.T) {
	res := &sqlserve.Result{Columns: []string{"a", "b", "c"}, Rows: [][]any{{1.0, nil, nil}, {2.0, "", nil}}}
	if got := NullColumns(res); !slices.Equal(got, []bool{false, false, true}) {
		t.Errorf("null columns = %v (an empty string is a value)", got)
	}
	if got := NullColumns(&sqlserve.Result{Columns: []string{"a"}}); !slices.Equal(got, []bool{false}) {
		t.Errorf("no rows marked %v", got)
	}
}

// THE SDK'S OWN COLUMNS ARE THE METADATA: the names are read out of the
// SDK, so a rename there fails here instead of silently ungrouping them.
func TestTheMetadataColumnsAreTheSDKs(t *testing.T) {
	raw, err := os.ReadFile("../../sdk/internal/core/metadata.go")
	if err != nil {
		t.Fatal(err)
	}
	var sdk []string
	for _, m := range regexp.MustCompile(`Metadata\w+\s*=\s*"([^"]+)"`).FindAllStringSubmatch(string(raw), -1) {
		sdk = append(sdk, m[1])
	}
	if !slices.Equal(sdk, MetadataColumns) {
		t.Errorf("the SDK's metadata columns are %v, the console groups %v", sdk, MetadataColumns)
	}
}

// THE LOAD'S PREVIEW HIDES WHAT IS EMPTY, AND SAYS SO: the null-only columns
// are marked for the switch (shown only with a script), the note names
// them, and the SDK's columns come last under "metadata".
func TestTheLoadsPreviewMarksItsEmptyColumns(t *testing.T) {
	v := LoadView{TargetView: TargetView{Tabs: true, Tab: "preview", Preview: &sqlserve.Result{
		Columns: []string{"ingestion_id", "sku", "notes", "qty"},
		Rows:    [][]any{{"x1", "A", nil, 3.0}, {"x2", "B", nil, 4.0}},
	}}}
	var b strings.Builder
	if err := loadPreview(v).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if strings.Count(got, "data-null-col") != 3 {
		t.Errorf("the empty column is not marked in its header and both rows: %d", strings.Count(got, "data-null-col"))
	}
	if !strings.Contains(got, "data-null-toggle") || !strings.Contains(got, "data-js") {
		t.Error("no switch, or a switch shown with no script")
	}
	if !strings.Contains(got, "1 column hidden: notes") {
		t.Error("the note does not name the hidden column")
	}
	if strings.Index(got, "ingestion_id") < strings.Index(got, "qty") || !strings.Contains(got, ">metadata<") {
		t.Error("the SDK's column is not last under metadata")
	}
}

// THE SWITCH ONLY WORKS WITH A SCRIPT, AND HIDING ONLY HAPPENS UNDER ONE: the
// sheet hides marked columns under `.js`, and ui.js flips data-hide-null.
func TestTheNullSwitchIsScriptOnly(t *testing.T) {
	css, _ := os.ReadFile("../assets/app.src.css")
	if !strings.Contains(string(css), ".js [data-hide-null] [data-null-col]") {
		t.Error("the sheet does not hide the marked columns, or hides them with no script")
	}
	js, _ := os.ReadFile("../assets/ui.js")
	for _, want := range []string{"[data-null-toggle]", `toggleAttribute("data-hide-null"`, "aria-checked"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("ui.js has no %s", want)
		}
	}
}
