package pages

import (
	"strings"
	"testing"
	"time"
)

// tablesStream is an auto_table stream: its events name their table, which
// must start with an accepted prefix, in the dataset `landing`.
func tablesStream() StreamView {
	s := StreamsOf(printFixtures(time.Now()))[0].Streams[0]
	s.Config = sourceConfig(field("name", "tables"), field("path", "/v1/tables"), field("format", "json"),
		field("sink.type", "auto_table"), field("sink.into.type", "bigquery"), field("sink.into.project", "acme"),
		field("sink.into.dataset", "landing"), field("sink.naming.allow", []any{"app_", "svc_"}),
		field("dead_letter.type", "files"), field("dead_letter.path", "./dead-letter/tables/"))
	return BuildStream(s, nil)
}

// THE TEST EVENT KNOWS WHERE AN EVENT LANDS (screen-streams T5): an
// auto_table's example names its table with an accepted prefix, and the
// drawer carries the rules the browser checks it by -- the field that names
// the table, the prefixes, the dataset -- so the two cannot disagree.
func TestTheTestEventKnowsWhereAnEventLands(t *testing.T) {
	v := tablesStream()
	if body := v.TestBody(); !strings.Contains(body, `"table_name": "app_test"`) {
		t.Errorf("an auto_table's example names no table: %s", body)
	}
	page := drawIn(t, "pt-BR", StreamPage(v))
	d := page[strings.Index(page, `<dialog id="test-event"`):]
	d = d[:strings.Index(d, "</dialog>")]
	for _, want := range []string{`data-test-table="table_name"`, `data-test-allow="app_,svc_"`, `data-test-dataset="landing"`,
		`data-msg-invalid="JSON inválido: {e}"`, `data-msg-no-table=`, `data-msg-refused=`, `data-msg-lands="Vai cair em {to}."`} {
		if !strings.Contains(d, want) {
			t.Errorf("the drawer has no %s", want)
		}
	}
	clicks := drawIn(t, "en", StreamPage(BuildStream(StreamsOf(printFixtures(time.Now()))[0].Streams[0], nil)))
	if !strings.Contains(clicks, `data-test-lands="pubsub://brevis-local/clicks"`) || strings.Contains(clicks, `data-test-table="`) {
		t.Error("a stream that writes one place does not say where, or asks for a table")
	}
}

// THE DEAD LETTER AND THE TEST EVENT SPEAK THE READER'S LANGUAGE
// (screen-streams T5); commands, paths and metrics are the data's own.
func TestTheDeadLetterAndTheTestEventHaveNoEnglishLeft(t *testing.T) {
	page := drawIn(t, "pt-BR", StreamPage(tablesStream()))
	dl := visibleText(page[strings.Index(page, `id="deadletter"`):])
	for _, english := range []string{
		"Dead letter\n", "Where refused batches go", "How many records", "Batches the destination refuses",
		"To list them", "This stream", "This destination", "reads back only", "declares no dead letter",
		"The commands", "Send a test event", "Body", "Send it with", "The console sends nothing", "the gateway publishes",
	} {
		if at := strings.Index(dl, english); at >= 0 {
			t.Errorf("the Portuguese dead letter still says %q: …%s…", english, dl[max(0, at-60):min(len(dl), at+40)])
		}
	}
	for _, data := range []string{"gateway deadletter ls", "--dry-run", "brevis_gateway_dead_letter_records_total"} {
		if !strings.Contains(page, data) {
			t.Errorf("the dead letter lost %q", data)
		}
	}
}
