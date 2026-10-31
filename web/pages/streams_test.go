package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/web/i18n"
)

// streamsFixture is the prints' streams, the clicks stream with its note: the
// comment above its `- name:` in the gateway's file.
func streamsFixture() StreamsView {
	entries := printFixtures(time.Now())
	entries[0].Writers[0].Gateway.Config.Notes = map[string]string{"": "Clicks from the web app.\nOne row per click."}
	return BuildStreams(entries, StreamFilter{})
}

func streamKeys(v StreamsView) string {
	var out []string
	for _, s := range v.Rows {
		out = append(out, s.Gateway+s.Path)
	}
	return strings.Join(out, " ")
}

// THE STREAMS ARE ONE ROW EACH (screen-streams T1), by gateway and path, and
// a dead letter is never a row of its own: it is its stream's.
func TestTheStreamsAreOneRowEach(t *testing.T) {
	v := streamsFixture()
	if got, want := streamKeys(v), "events_gateway/v1/clicks events_gateway/v1/orders zarv_ingestion_dev/v1/ingestion"; got != want {
		t.Errorf("rows = %s, want %s", got, want)
	}
	if v.Total != 3 || v.Gateways != 2 || v.WithDeadLetter != 2 {
		t.Errorf("total %d, gateways %d, with a dead letter %d; want 3, 2, 2", v.Total, v.Gateways, v.WithDeadLetter)
	}
}

// A ROW SAYS WHERE EVENTS COME IN, WHERE THEY GO AND HOW: POST and the path,
// the stream's note, the gateway and the destination with their logos, the
// batch in words, continuous, and the dead letter; it opens the stream.
func TestAStreamRowSaysWhereAndHow(t *testing.T) {
	page := drawIn(t, "en", StreamsList(streamsFixture()))
	clicks := page[strings.Index(page, `data-href="/ingest/streams/events_gateway/v1/clicks"`):]
	clicks = clicks[:strings.Index(clicks, "</tr>")]
	for _, want := range []string{"POST", "/v1/clicks", "Clicks from the web app.", "events_gateway", `aria-label="Brevis gateway"`,
		`aria-label="Google Pub/Sub"`, "pubsub://brevis-local/clicks", "1 second or 500", "continuous", "file:///var/dead-letter/clicks/"} {
		if !strings.Contains(clicks, want) {
			t.Errorf("the clicks row has no %q", want)
		}
	}
	if strings.Contains(clicks, "One row per click") {
		t.Error("the row shows more than the note's first line")
	}
	ingestion := page[strings.Index(page, `data-href="/ingest/streams/zarv_ingestion_dev/v1/ingestion"`):]
	ingestion = ingestion[:strings.Index(ingestion, "</tr>")]
	if !strings.Contains(ingestion, "—") || !strings.Contains(ingestion, "bigquery://zarv-development-94b6/bronze/*") {
		t.Error("a stream with no configuration and no dead letter does not say so")
	}
}

// A BATCH IS SAID IN WORDS, in each language: every, up to, or both.
func TestAStreamsBatchIsInWords(t *testing.T) {
	for lang, want := range map[string][3]string{
		"en":    {"2 minutes or 2,000", "30 seconds", "up to 500"},
		"pt-BR": {"2 minutos ou 2.000", "30 segundos", "até 500"},
		"es":    {"2 minutos o 2.000", "30 segundos", "hasta 500"},
	} {
		ctx := i18n.WithLang(context.Background(), lang)
		for i, s := range []Stream{
			{Config: sourceConfig(field("buffer.flush.every", "2m"), field("buffer.flush.records", 2000.0))},
			{Config: sourceConfig(field("buffer.flush.every", "30s"))},
			{Config: sourceConfig(field("buffer.flush.records", 500.0))},
		} {
			if got := s.BatchIn(ctx); got != want[i] {
				t.Errorf("%s batch %d = %q, want %q", lang, i, got, want[i])
			}
		}
	}
	if got := (Stream{}).BatchIn(context.Background()); got != "" {
		t.Errorf("no buffer declared says %q", got)
	}
}

// THE SUMMARY AND THE FOOTER: how many streams, gateways and dead letters,
// and how a stream works, in three steps.
func TestTheStreamsHeadAndFoot(t *testing.T) {
	page := drawIn(t, "en", Streams(streamsFixture()))
	summary := page[strings.Index(page, "data-streams-summary"):]
	summary = summary[:strings.Index(summary, "</div>")]
	for _, want := range []string{"3 streams", "2 gateways", "2 with a dead letter"} {
		if !strings.Contains(summary, want) {
			t.Errorf("the summary has no %q", want)
		}
	}
	for _, want := range []string{"data-how-it-works", "POST to the gateway", "ingestion_id", "Batched before writing", "Written, or kept"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %q", want)
		}
	}
}

// TEMPO REAL SPEAKS THE READER'S LANGUAGE (screen-streams T1): the title, the
// lead, the summary, the table, the footer and the empty states. Paths,
// gateways and URIs are the data's own.
func TestTheStreamsHaveNoEnglishLeft(t *testing.T) {
	text := visibleText(drawIn(t, "pt-BR", Streams(streamsFixture()))) + " " +
		visibleText(drawIn(t, "pt-BR", StreamsList(BuildStreams(nil, StreamFilter{})))) + " " +
		visibleText(drawIn(t, "pt-BR", StreamsList(BuildStreams(printFixtures(time.Now()), StreamFilter{Q: "zzz"}))))
	for _, english := range []string{
		// "stream", "gateway" and "dead letter" are the product's words in
		// every language, as the handoff writes them.
		"Real time", "Streams write", "continuously", "with a dead letter",
		"Origin", "destination", "Batch", "Status", "continuous", "second",
		"How it works", "POST to the gateway", "Batched", "Written", "No stream yet", "Nothing matches",
	} {
		if at := strings.Index(text, english); at >= 0 {
			t.Errorf("the Portuguese streams still say %q: …%s…", english, text[max(0, at-60):min(len(text), at+40)])
		}
	}
}

// A DEAD LETTER ITS MANIFEST COULD NOT NAME -- a path relative to the
// gateway's working directory -- is still shown as declared, its kind and
// its path, with why it has no URI on hover.
func TestAnUnnamedDeadLetterIsShownAsDeclared(t *testing.T) {
	s := Stream{Gateway: "edge", Path: "/v1/clicks", Sink: Place{URI: "pubsub://acme/clicks", Kind: "pubsub"},
		DeadLetter: &Place{Note: `"./dead-letter/" is relative to the gateway's working directory`},
		Config:     sourceConfig(field("dead_letter.type", "files"), field("dead_letter.path", "./dead-letter/clicks/"))}
	page := drawIn(t, "en", StreamsList(StreamsView{Rows: []Stream{s}, Total: 1}))
	for _, want := range []string{"./dead-letter/", "clicks/", `aria-label="Local files"`, "relative to the gateway"} {
		if !strings.Contains(page, want) {
			t.Errorf("the unnamed dead letter has no %q", want)
		}
	}
	if strings.Contains(page, "not named by the manifest") {
		t.Error("a dead letter whose path is declared is called unnamed")
	}
}
