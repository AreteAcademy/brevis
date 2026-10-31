package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func tablesConfig() *postgres.WriterConfig {
	return &postgres.WriterConfig{
		Hash: "sha256:abc",
		Fields: []catalog.ManifestField{
			{Path: "name", Value: "tables"},
			{Path: "format", Value: "json"},
			{Path: "buffer.flush.every", Value: "1m"},
			{Path: "buffer.flush.claim", Value: true},
			{Path: "buffer.workers", Value: float64(4), Default: true},
			{Path: "sink.type", Value: "auto_table"},
			{Path: "sink.naming.allow", Value: []any{"app_", "svc_"}},
			{Path: "sink.metastore.type", Value: "redis"},
			{Path: "sink.metastore.ttl", Value: "1m"},
			{Path: "sink.into.write", Value: "append"},
			{Path: "dead_letter.path", Value: "./dead-letter/tables/"},
		},
		Notes: map[string]string{
			"":                "One route, N tables, nothing declared.",
			"buffer":          "Whichever trigger is crossed first sends the batch.",
			"sink.metastore":  "The shared state this stream keeps.",
			"sink.into.write": "APPEND: the landing table is history.\nDeduplication happens on read.",
		},
	}
}

// Sections follow the file: the stream's own settings, then each block in the
// order it first appears. A row is labelled by its path inside its section.
func TestTheConfigurationIsLaidOutInTheFilesOrder(t *testing.T) {
	v := BuildConfig(tablesConfig())

	var titles []string
	for _, s := range v.Sections {
		titles = append(titles, s.TitleIn(context.Background()))
	}
	if got := strings.Join(titles, ","); got != "Stream,Buffer,Sink,Dead letter" {
		t.Fatalf("sections = %s", got)
	}
	buffer := v.Sections[1]
	if buffer.Note != "Whichever trigger is crossed first sends the batch." {
		t.Fatalf("the section's own note = %q", buffer.Note)
	}
	if r := buffer.Rows[0]; r.Label != "flush.every" || r.Value != "1m" || r.Path != "buffer.flush.every" {
		t.Fatalf("first buffer row = %+v", r)
	}
	if r := buffer.Rows[1]; r.Value != "true" {
		t.Fatalf("a bool = %q", r.Value)
	}
	if r := buffer.Rows[2]; r.Value != "4" || !r.Default {
		t.Fatalf("a default number = %+v", r)
	}
	if v.Note != "One route, N tables, nothing declared." {
		t.Fatalf("the stream's note = %q", v.Note)
	}
	// Once, at the top -- not again as the Stream section's own.
	if v.Sections[0].Note != "" {
		t.Fatalf("the stream's note is repeated on its section: %q", v.Sections[0].Note)
	}
}

// A block inside a section -- `sink.metastore` -- has its comment shown once,
// before its first setting.
func TestABlocksNoteComesBeforeItsFirstSetting(t *testing.T) {
	sink := BuildConfig(tablesConfig()).Sections[2]
	var shown int
	for _, r := range sink.Rows {
		for _, g := range r.Before {
			if g.Label == "metastore" {
				shown++
				if r.Path != "sink.metastore.type" || g.Text != "The shared state this stream keeps." {
					t.Fatalf("metastore's note sits on %s: %+v", r.Path, g)
				}
			}
		}
	}
	if shown != 1 {
		t.Fatalf("metastore's note shown %d times, want once", shown)
	}
	if r := sink.Rows[1]; r.Value != "app_, svc_" {
		t.Fatalf("a list = %q", r.Value)
	}
}

// The page: every setting, its value, the default marked, the comment beside
// it. Server-rendered, so this is what a browser receives.
func TestTheDestinationPageShowsTheGatewaysConfiguration(t *testing.T) {
	const tables = "bigquery://acme/landing/*"
	d := postgres.TargetDetail{CatalogEntry: postgres.CatalogEntry{
		Target: tables, Kind: "bigquery",
		Writers: []postgres.CatalogWriter{{Gateway: &postgres.GatewayWriter{
			Name: "edge", Stream: "/v1/tables", Role: "sink", Kind: "auto_table", Routes: true,
			PublishedAt: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC),
			Config:      tablesConfig(),
		}}},
	}}
	var b bytes.Buffer
	if err := configurations(BuildTarget(d, time.Now())).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	html := b.String()
	for _, want := range []string{
		`data-path="sink.into.write"`,
		`>append<`,
		`data-defaulted="true"`,
		"Deduplication happens on read.",
		"One route, N tables, nothing declared.",
		"Configuration",
		// The sentence, above everything else in the section.
		"JSON events become app_* or svc_* tables, appended so a redelivery is a second row, in batches every 1 minute.",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if n := strings.Count(html, `data-defaulted="true"`); n != 1 {
		t.Errorf("%d settings marked as defaults, want 1", n)
	}
}

// A writer with nothing to show adds nothing to the page.
func TestAWriterWithNoConfigurationHasNoSection(t *testing.T) {
	d := postgres.TargetDetail{CatalogEntry: postgres.CatalogEntry{
		Target: "pubsub://acme/clicks", Kind: "pubsub",
		Writers: []postgres.CatalogWriter{{Gateway: &postgres.GatewayWriter{
			Name: "edge", Stream: "/v1/clicks", Role: "sink", Kind: "pubsub"}}},
	}}
	var b bytes.Buffer
	if err := configurations(BuildTarget(d, time.Now())).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "data-path=") {
		t.Fatal("a writer with no configuration drew settings")
	}
}

// Each version says what changed from the one before it, newest first; the
// oldest says it is where the record begins.
func TestTheConfigurationsChangesAreListedNewestFirst(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	c := tablesConfig()
	c.History = []postgres.ConfigVersion{
		{Hash: "c", At: at.Add(2 * time.Hour), Fields: []catalog.ManifestField{
			{Path: "buffer.flush.every", Value: "2m"}, {Path: "sink.into.write", Value: "append"}}},
		{Hash: "b", At: at.Add(time.Hour), Fields: []catalog.ManifestField{
			{Path: "buffer.flush.every", Value: "1m"}, {Path: "sink.into.write", Value: "append"},
			{Path: "retry.attempts", Value: float64(2)}}},
		{Hash: "a", At: at, Fields: []catalog.ManifestField{
			{Path: "buffer.flush.every", Value: "1m"}, {Path: "sink.into.write", Value: "merge"}}},
	}
	v := BuildConfig(c)
	if len(v.Changes) != 3 {
		t.Fatalf("changes = %+v", v.Changes)
	}
	newest := v.Changes[0]
	if !newest.At.Equal(at.Add(2*time.Hour)) || len(newest.Lines) != 2 {
		t.Fatalf("newest = %+v", newest)
	}
	if l := newest.Lines[0]; l.Path != "buffer.flush.every" || l.TextIn(context.Background()) != "1m → 2m" {
		t.Fatalf("a changed value = %+v", l)
	}
	if l := newest.Lines[1]; l.Path != "retry.attempts" || l.TextIn(context.Background()) != "removed (was 2)" {
		t.Fatalf("a removed value = %+v", l)
	}
	if l := v.Changes[1].Lines; len(l) != 2 || l[0].TextIn(context.Background()) != "merge → append" || l[1].TextIn(context.Background()) != "added: 2" {
		t.Fatalf("the middle version = %+v", l)
	}
	if oldest := v.Changes[2]; !oldest.First || len(oldest.Lines) != 0 {
		t.Fatalf("oldest = %+v", oldest)
	}

	var b bytes.Buffer
	d := postgres.TargetDetail{CatalogEntry: postgres.CatalogEntry{Target: "bigquery://acme/landing/*",
		Writers: []postgres.CatalogWriter{{Gateway: &postgres.GatewayWriter{Name: "edge", Stream: "/v1/tables", Config: c}}}}}
	if err := configurations(BuildTarget(d, time.Now())).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Changes", "1m → 2m", "removed (was 2)", "First published"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}

// A history cut at the page's limit does not claim its oldest line is the first.
func TestATruncatedHistoryDoesNotClaimAFirstPublish(t *testing.T) {
	c := tablesConfig()
	for i := range postgres.HistoryShown {
		c.History = append(c.History, postgres.ConfigVersion{
			At: time.Unix(int64(1000-i), 0), Fields: []catalog.ManifestField{{Path: "x", Value: float64(i)}}})
	}
	last := BuildConfig(c).Changes[postgres.HistoryShown-1]
	if last.First {
		t.Fatal("the oldest of a cut history claims to be the first publish")
	}
}

// A step's configuration: Source, Transform, Target, and each version linked
// to the run that first wrote with it.
func TestTheDestinationPageShowsAStepsConfiguration(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	fieldsNow := []catalog.ManifestField{
		{Path: "source.driver", Value: "from.HTTP"}, {Path: "source.url", Value: "https://api.example.com/v1/items"},
		{Path: "transform.1.map", Value: []any{"compute(provider)", "ingestion_id"}},
		{Path: "target.driver", Value: "bigquery.Table"}, {Path: "target.dedup", Value: "merge"},
	}
	before := []catalog.ManifestField{
		{Path: "source.driver", Value: "from.HTTP"}, {Path: "source.url", Value: "https://api.example.com/v1/items"},
		{Path: "transform.1.map", Value: []any{"compute(provider)", "ingestion_id"}},
		{Path: "target.driver", Value: "bigquery.Table"},
	}
	d := postgres.TargetDetail{CatalogEntry: postgres.CatalogEntry{
		Target: "bigquery://acme/landing/items", Kind: "bigquery",
		Writers: []postgres.CatalogWriter{
			{Workflow: "items_sync", Node: "load", LastLoaded: at,
				Config: &postgres.WriterConfig{Fields: fieldsNow, History: []postgres.ConfigVersion{
					{At: at, Fields: fieldsNow, RunID: "11111111-2222-3333-4444-555555555555"},
					{At: at.Add(-time.Hour), Fields: before, RunID: "99999999-2222-3333-4444-555555555555"},
				}}},
			{Workflow: "python_job", Node: "build", LastLoaded: at},
		},
	}}
	var b bytes.Buffer
	if err := configurations(BuildTarget(d, time.Now())).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	html := b.String()
	for _, want := range []string{
		`data-config="items_sync load"`,
		">Source<", ">Transform<", ">Target<",
		`data-path="source.url"`,
		"compute(provider), ingestion_id",
		"added: merge",
		`href="/runs/11111111-2222-3333-4444-555555555555"`,
		"Reads https://api.example.com/v1/items",
		`data-config="python_job build"`,
		"This step did not describe itself",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}
