package pages

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

// ranFixture is the workbench after a query: two rows, 91.9 MB read, 523 ms.
func ranFixture() SQLView {
	v := sqlFixture()
	v.Statement = "SELECT sku, n FROM bronze.orders"
	v.Result = &sqlserve.Result{Columns: []string{"sku", "n"}, Rows: [][]any{{"A", "1"}, {"B", "2"}},
		Bytes: 96_363_724, Millis: 523}
	return v
}

// THE ANSWER IS THREE TABS (screen-sql T3): Results, JSON, and History with
// how many it holds -- what was the Recent rail beside the grid, now a tab,
// still only in this browser. The tabs sit above the answer and outlive it:
// a run swaps the answer and not them.
func TestTheAnswerIsThreeTabs(t *testing.T) {
	page := drawIn(t, "en", SQL(ranFixture()))
	results := page[strings.Index(page, `data-pane="results"`):]
	tabs := results[:strings.Index(results, `data-answer`)]
	for _, want := range []string{`data-tab="results"`, ">Results<", `data-tab="json"`, ">JSON<", `data-tab="history"`, "History", "data-history-count"} {
		if !strings.Contains(tabs, want) {
			t.Errorf("the tabs above the answer lack %s", want)
		}
	}
	history := results[strings.Index(results, `data-panel="history"`):]
	if !strings.Contains(history, "data-recent-keep") || !strings.Contains(history, "in this browser") {
		t.Error("History is not the list the rail was")
	}
	if strings.Contains(page, "data-recent-toggle") || strings.Contains(page, "workbench-recent") {
		t.Error("the Recent rail is still drawn")
	}
}

// WHAT CAME BACK IS SAID IN THE READER'S FORMAT: rows, bytes read and time,
// with "there are more" when a limit cut it.
func TestTheAnswersFiguresAreInTheReadersFormat(t *testing.T) {
	v := ranFixture()
	for lang, want := range map[string]string{"en": "2 rows · 91.9 MB read · 523 ms", "pt-BR": "2 linhas · 91,9 MB lidos · 523 ms"} {
		if got := drawIn(t, lang, SQLAnswer(v)); !strings.Contains(got, want) {
			t.Errorf("%s: %s", lang, visibleText(got))
		}
	}
	v.Result.Truncated = true
	if got := drawIn(t, "pt-BR", SQLAnswer(v)); !strings.Contains(got, "há mais") {
		t.Errorf("a cut answer: %s", visibleText(got))
	}
}

// A REFUSAL AT RUN HAS THE CONSOLE'S TITLE, the service's sentence and the
// way to where writes run; a destination the console does not know, or one
// with no connection declared, is said in the reader's words.
func TestARefusalIsTitledAndLeadsToWorkflows(t *testing.T) {
	v := sqlFixture()
	v.Refused = "this service only reads, and this is a DELETE statement"
	got := drawIn(t, "pt-BR", SQLAnswer(v))
	for _, want := range []string{"Recusada pelo serviço SQL", "this is a DELETE statement", `href="/workflows"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal lacks %s: %s", want, visibleText(got))
		}
	}
	v.Refused, v.ErrKey, v.ErrArg = "", "sql.err.no_connection", "warehouse"
	if got := drawIn(t, "pt-BR", SQLAnswer(v)); !strings.Contains(got, "warehouse") || !strings.Contains(got, "brevis.yaml") {
		t.Errorf("no connection: %s", visibleText(got))
	}
}

// BEFORE ANYTHING RUNS, HOW TO: the handoff's sentence.
func TestTheEmptyAnswerSaysHow(t *testing.T) {
	got := visibleText(drawIn(t, "pt-BR", SQLAnswer(sqlFixture())))
	if !strings.Contains(got, "Nenhuma consulta executada") || !strings.Contains(got, "Clique numa tabela à esquerda") {
		t.Errorf("the empty answer: %s", got)
	}
}

// THE WHOLE SCREEN SPEAKS THE READER'S LANGUAGE (screen-sql T3): empty, with
// an answer and a cut one, refused, failed, a write on an unlocked
// connection, and a console with nothing to query. The data, the SQL and the
// service's own sentence are what they are.
func TestTheSQLScreenHasNoEnglishLeft(t *testing.T) {
	var text string
	draw := func(v SQLView) {
		text += " " + visibleText(regexpPre.ReplaceAllString(drawIn(t, "pt-BR", SQL(v)), " "))
	}
	draw(sqlFixture())
	v := ranFixture()
	v.Result.Truncated = true
	draw(v)
	v = sqlFixture()
	v.Refused = "the service's own words"
	draw(v)
	v = sqlFixture()
	v.ErrKey = "sql.err.unknown"
	draw(v)
	v = sqlFixture()
	v.Err = "the SQL service could not be reached"
	draw(v)
	one := int64(1)
	v = ranFixture()
	v.Writable, v.Result.Wrote, v.Result.Changed, v.Result.Columns, v.Result.Rows = true, true, &one, nil, nil
	draw(v)
	draw(SQLView{})
	for _, english := range []string{"Results", "History", "Recent", "Rows per page", "Previous page", "Next page",
		// " Cancel " with its spaces: "Cancelar" is the Portuguese.
		"Running", " Cancel ", " rows", "scanned", " read ", "there are more", "row changed", "no rows to count",
		"Refused", "Nothing has run", "Run a query", "Nothing yet", "That is not a destination", "Nothing here can be queried",
		"Resize the editor", "failed", "Could not run", "How to read the answer"} {
		if at := strings.Index(text, english); at >= 0 {
			t.Errorf("the Portuguese screen still says %q: …%s…", english, text[max(0, at-60):min(len(text), at+40)])
		}
	}
}
