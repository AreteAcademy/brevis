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
		titles = append(titles, s.Title)
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
	if err := Target(BuildTarget(d, time.Now())).Render(context.Background(), &b); err != nil {
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
	if err := Target(BuildTarget(d, time.Now())).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "data-path=") {
		t.Fatal("a writer with no configuration drew settings")
	}
}
