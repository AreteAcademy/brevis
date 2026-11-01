package pages

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

// editorOf is the editor's zone of a drawn workbench: the tabs, the bar, the
// box and the lines under it -- not the tree, not the answer.
func editorOf(t *testing.T, lang string, v SQLView) string {
	t.Helper()
	page := drawIn(t, lang, SQL(v))
	at, end := strings.Index(page, `data-pane="editor"`), strings.Index(page, `data-pane="results"`)
	if at < 0 || end < at {
		t.Fatal("no editor zone")
	}
	return page[at:end]
}

// THE ESTIMATE HAS THREE SHAPES (screen-sql T2): a figure, in the reader's
// number format; a refusal, under one title of the console's own, with the
// service's sentence as it came -- the service does not say why it refused,
// so the console does not guess; and a warehouse that does not price, said in
// the reader's words.
func TestTheEstimateHasThreeShapes(t *testing.T) {
	priced := sqlserve.Price{State: "priced", Bytes: 287_214_387}
	for lang, want := range map[string]string{"en": "Will process <strong class=\"font-medium text-ink\">273.9 MB</strong>",
		"pt-BR": "Vai processar <strong class=\"font-medium text-ink\">273,9 MB</strong>"} {
		if got := drawIn(t, lang, Cost(priced)); !strings.Contains(got, want) {
			t.Errorf("%s: %s", lang, got)
		}
	}
	refused := drawIn(t, "pt-BR", Cost(sqlserve.Price{State: "refused", Why: "only a single SELECT may run here"}))
	if !strings.Contains(refused, "Vai ser recusada") || !strings.Contains(refused, "only a single SELECT may run here") {
		t.Errorf("a refusal: %s", visibleText(refused))
	}
	unpriced := drawIn(t, "pt-BR", Cost(sqlserve.Price{State: "unpriced", Why: "this warehouse does not price a query"}))
	if strings.Contains(unpriced, "does not price") || !strings.Contains(unpriced, "Este warehouse não estima") {
		t.Errorf("a warehouse that does not price: %s", visibleText(unpriced))
	}
}

// THE BAR SAYS WHAT RUNS AND WHERE: Run with its key, the connection with its
// logo and a way to the connections, and Copy. The tabs are named, and Copy
// says it copied, in the reader's language -- the island reads the words
// from the page, it writes none of its own.
func TestTheBarSaysWhatRunsAndWhere(t *testing.T) {
	v := sqlFixture()
	editor := editorOf(t, "pt-BR", v)
	for _, want := range []string{"Executar", "⌘↵", `aria-label="Google BigQuery"`, "bigquery://acme", `href="/data"`, ">conexões<",
		"Copiar", `data-copied-label="Copiado"`, `data-tab-name="Consulta {n}"`, `href="/workflows"`} {
		if !strings.Contains(editor, want) {
			t.Errorf("the editor lacks %s", want)
		}
	}
}

// THE EDITOR SPEAKS THE READER'S LANGUAGE, on a connection that reads and on
// one somebody unlocked for writes; the SQL in it is SQL.
func TestTheEditorHasNoEnglishLeft(t *testing.T) {
	v := sqlFixture()
	text := visibleText(editorOf(t, "pt-BR", v))
	v.Writable = true
	text += " " + visibleText(editorOf(t, "pt-BR", v)) + " " + visibleText(drawIn(t, "pt-BR", writesAllowed()))
	for _, english := range []string{"Run", "Copy", "Open statements", "New statement", "Close this statement",
		// "Query" alone would find Google BigQuery.
		"Reads only", "completes names", "SQL to run", "Writes allowed", "change data", "connections", "Query 1", "New query"} {
		if at := strings.Index(text, english); at >= 0 {
			t.Errorf("the Portuguese editor still says %q: …%s…", english, text[max(0, at-60):min(len(text), at+40)])
		}
	}
}
