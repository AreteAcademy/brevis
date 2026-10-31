package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// publishedStream is a stream as a gateway publishes it, with its notes and one
// setting this console has no words for.
func publishedStream() *postgres.WriterConfig {
	f := func(p string, v any) catalog.ManifestField { return catalog.ManifestField{Path: p, Value: v} }
	return &postgres.WriterConfig{
		Fields: []catalog.ManifestField{
			f("name", "clicks"), f("path", "/v1/clicks"), f("format", "json"),
			f("identity.provider", "acme"), f("identity.entity", "click"),
			{Path: "buffer.durability", Value: "memory", Default: true},
			f("buffer.flush.every", "1s"), f("buffer.flush.records", 500.0),
			f("sink.type", "auto_table"), f("sink.naming.allow", []any{"app_", "svc_"}),
			f("retry.attempts", 4.0), f("retry.backoff", "500ms"),
			f("dead_letter.type", "files"), f("dead_letter.path", "./dead-letter/clicks/"),
			f("sink.brand_new", "*all*"),
		},
		Notes: map[string]string{"": "Clicks from the web app.", "buffer.flush": "How often a batch is written.\nNever more than this.", "retry.attempts": "Then the dead letter."},
	}
}

// THE CONFIGURATION, EXPLAINED (screen-streams T4): the settings in cards --
// what comes in, when it is written, where, retries, the event's identity,
// the dead letter -- each with a label in the reader's language, its key
// under it, its value and whether the gateway filled it in; a setting with
// no label lands in Others by its key, never dropped.
func TestAStreamsConfigurationIsExplained(t *testing.T) {
	cards := ExplainConfig(publishedStream())
	var got []string
	for _, c := range cards {
		got = append(got, c.Key)
	}
	if strings.Join(got, " ") != "intake buffer destination retry identity dead_letter other" {
		t.Fatalf("cards = %v", got)
	}
	ctx := i18n.WithLang(context.Background(), "pt-BR")
	buffer := cards[1]
	if r := buffer.Rows[1]; r.Path != "buffer.flush.every" || r.LabelIn(ctx) != "Grava a cada" || r.Value != "1s" {
		t.Errorf("buffer's second row = %+v (%s)", r, r.LabelIn(ctx))
	}
	if !buffer.Rows[0].Default {
		t.Error("a value the gateway filled in is not marked")
	}
	if other := cards[6]; len(other.Rows) != 1 || other.Rows[0].Path != "sink.brand_new" || other.Rows[0].LabelIn(ctx) != "sink.brand_new" {
		t.Errorf("others = %+v", other.Rows)
	}
	if cards[3].Rows[0].Note != "Then the dead letter." {
		t.Errorf("a setting's note is lost: %+v", cards[3].Rows[0])
	}
	if dest := cards[2]; dest.Rows[1].Value != "app_, svc_" {
		t.Errorf("a list's value = %q", dest.Rows[1].Value)
	}
}

// THE YAML IS MADE FROM WHAT WAS PUBLISHED: the settings nested by their
// keys, lists as lists, a value that would read as something else quoted,
// and the notes as comments above what they are about. The same fields
// always give the same text.
func TestAStreamsYAMLIsMadeFromItsFields(t *testing.T) {
	want := `# Clicks from the web app.
name: clicks
path: /v1/clicks
format: json
identity:
  provider: acme
  entity: click
buffer:
  durability: memory
  # How often a batch is written.
  # Never more than this.
  flush:
    every: 1s
    records: 500
sink:
  type: auto_table
  naming:
    allow:
      - app_
      - svc_
  brand_new: "*all*"
retry:
  # Then the dead letter.
  attempts: 4
  backoff: 500ms
dead_letter:
  type: files
  path: ./dead-letter/clicks/
`
	if got := ConfigYAML(publishedStream()); got != want {
		t.Errorf("yaml =\n%s\nwant\n%s", got, want)
	}
	first := ConfigYAML(publishedStream())
	for range 20 {
		if again := ConfigYAML(publishedStream()); again != first {
			t.Fatal("the same fields give different text")
		}
	}
	if got := ConfigYAML(&postgres.WriterConfig{Fields: []catalog.ManifestField{{Path: "a", Value: "true"}, {Path: "b", Value: ""}, {Path: "c", Value: "x: y"}}}); got != "a: \"true\"\nb: \"\"\nc: \"x: y\"\n" {
		t.Errorf("quoting = %q", got)
	}
}

// THE CONFIGURATION TAB, ON THE PAGE: Explained and YAML, both drawn -- one
// at a time with a script -- and the history of publishes in one line.
func TestAStreamsConfigurationTab(t *testing.T) {
	s := StreamsOf(printFixtures(time.Now()))[0].Streams[0]
	s.Config = publishedStream()
	page := drawIn(t, "en", StreamPage(BuildStream(s, nil)))
	cfg := page[strings.Index(page, `id="config"`):strings.Index(page, `id="deadletter"`)]
	for _, want := range []string{
		`data-config-view="explained"`, `data-view-set="explained"`, `data-view-set="yaml"`,
		"Writes every", ">buffer.flush.every<", "Retries", "Event identity", "Others",
		`id="stream-yaml"`, "brand_new: &#34;*all*&#34;", `data-copy="#stream-yaml"`, "made from what was published",
		"default",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("the configuration tab has no %s", want)
		}
	}
}

// THE CONFIGURATION SPEAKS THE READER'S LANGUAGE (screen-streams T4): the
// cards, the segment, the labels and the notes about the YAML; keys, values
// and the file's own comments are the data's.
func TestAStreamsConfigurationHasNoEnglishLeft(t *testing.T) {
	s := StreamsOf(printFixtures(time.Now()))[0].Streams[0]
	s.Config = publishedStream()
	page := drawIn(t, "pt-BR", StreamPage(BuildStream(s, nil)))
	cfg := visibleText(page[strings.Index(page, `id="config"`):strings.Index(page, `id="deadletter"`)])
	for _, english := range []string{"Explained", "Writes every", "Retries", "Event identity", "Others", "Intake",
		"made from what was published", "default", "History", "first published", "Attempts", "Provider"} {
		if at := strings.Index(cfg, english); at >= 0 {
			t.Errorf("the Portuguese configuration still says %q: …%s…", english, cfg[max(0, at-60):min(len(cfg), at+40)])
		}
	}
	v1 := StreamsOf(printFixtures(time.Now()))[1].Streams[0] // zarv_ingestion_dev, no configuration
	if text := visibleText(drawIn(t, "pt-BR", StreamPage(BuildStream(v1, nil)))); strings.Contains(text, "published without its configuration") {
		t.Error("the version-1 note is still in English")
	}
}
