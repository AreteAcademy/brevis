package api_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// THE SCRIPT SPEAKS THE PAGE'S LANGUAGE TOO. ui.js writes a few sentences
// itself -- the theme switch's new label, "Copied", the palette's empty and
// loading states -- and it takes them from a JSON block the server writes in
// the page's language, never from English literals in the file.
func TestThePageHandsTheScriptItsSentences(t *testing.T) {
	body := pageIn(t, dataUI(catalogFake{}), "/ingest/loads", "pt-BR", "", false)
	open := `<script type="application/json" id="i18n">`
	at := strings.Index(body, open)
	if at < 0 {
		t.Fatal("the page has no i18n block for the script")
	}
	raw := body[at+len(open) : at+strings.Index(body[at:], "</script>")]
	var msgs map[string]string
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		t.Fatalf("the block is not JSON: %v\n%s", err, raw)
	}
	for key, want := range map[string]string{
		"copy.done":          "Copiado",
		"palette.empty":      "Nada encontrado.",
		"palette.open_run":   "Abrir a execução {id}…",
		"palette.group.runs": "Execuções",
	} {
		if msgs[key] != want {
			t.Errorf("%s = %q, want %q", key, msgs[key], want)
		}
	}
}

func TestTheScriptHasNoSentenceOfItsOwn(t *testing.T) {
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/ui.js")
	js := strip(raw)
	for _, old := range []string{
		"Switch to the dark theme", "Switch to the light theme", `"Copied"`,
		"Nothing matches.", "Loading…", "Search is unavailable", `"Open run "`,
	} {
		if strings.Contains(js, old) {
			t.Errorf("ui.js still writes %q", old)
		}
	}
	if !strings.Contains(js, `getElementById("i18n")`) {
		t.Error("ui.js does not read the page's i18n block")
	}
}

// THE PALETTE'S GROUPS ARE IDS WITH LABELS: the script orders and filters by
// the id, which is the same in every language, and shows the label.
func TestThePaletteGroupsCarryATranslatedLabel(t *testing.T) {
	_, _, items := palette(t, dataUI(catalogFake{}))
	for _, it := range items {
		if it.Group == "" || it.GroupLabel == "" {
			t.Errorf("an item without a group id or label: %+v", it)
		}
	}
	_, ui := browsing(t)
	_, raw := get(t, ui, "/assets/ui.js")
	if !strings.Contains(strip(raw), "groupLabel") {
		t.Error("ui.js does not show the group's label")
	}
}
