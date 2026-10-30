package pages

import (
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func sourceConfig(fields ...catalog.ManifestField) *postgres.WriterConfig {
	return &postgres.WriterConfig{Fields: fields}
}

func field(path string, value any) catalog.ManifestField {
	return catalog.ManifestField{Path: path, Value: value}
}

// A STEP'S ORIGIN IS ONLY WHAT IT DECLARED (screen-loads T1): the SDK's own
// sources by their kind and where they read, one of somebody else's as
// "other" by its type, and a step that described nothing as undeclared --
// never guessed from the destination or the step's name.
func TestAStepsOriginIsWhatItDeclared(t *testing.T) {
	for name, c := range map[string]struct {
		config *postgres.WriterConfig
		want   Source
	}{
		"http":     {sourceConfig(field("source.driver", "from.HTTP"), field("source.url", "https://api.ipma.pt/open-data/warnings"), field("source.method", "GET")), Source{Kind: "http", Driver: "from.HTTP", Logo: "http", Where: "https://api.ipma.pt/open-data/warnings"}},
		"files":    {sourceConfig(field("source.driver", "from.Files"), field("source.path", "gs://lake/raw/*.parquet")), Source{Kind: "file", Driver: "from.Files", Logo: "file", Where: "gs://lake/raw/*.parquet"}},
		"postgres": {sourceConfig(field("source.driver", "postgres.Query"), field("source.host", "billing-db"), field("source.database", "billing")), Source{Kind: "database", Driver: "postgres.Query", Logo: "postgres", Where: "billing-db/billing"}},
		"mysql":    {sourceConfig(field("source.driver", "mysql.Query"), field("source.host", "shop")), Source{Kind: "database", Driver: "mysql.Query", Logo: "mysql", Where: "shop"}},
		"own":      {sourceConfig(field("source.driver", "vendors.Feed"), field("target.dedup", "none")), Source{Kind: "other", Driver: "vendors.Feed"}},
		"gateway":  {sourceConfig(field("sink.type", "pubsub")), Source{}},
		"none":     {nil, Source{}},
	} {
		if got := SourceOf(c.config); got != c.want {
			t.Errorf("%s: SourceOf = %+v, want %+v", name, got, c.want)
		}
	}
}

// THE LIST'S WRITERS CARRY THEIR CONFIGURATION, attached by step and
// destination; a gateway writer and a step with none are left alone.
func TestStepConfigsAreAttachedToTheirWriters(t *testing.T) {
	http := sourceConfig(field("source.driver", "from.HTTP"))
	entries := []postgres.CatalogEntry{
		{Target: "bigquery://a/b/c", Writers: []postgres.CatalogWriter{{Workflow: "w", Node: "n"}, {Workflow: "py", Node: "n"}}},
		{Target: "pubsub://a/b", Writers: []postgres.CatalogWriter{{Gateway: &postgres.GatewayWriter{Name: "edge", Stream: "/v1"}}}},
	}
	AttachStepConfigs(entries, map[postgres.StepKey]*postgres.WriterConfig{
		{Workflow: "w", Node: "n", Target: "bigquery://a/b/c"}: http,
	})
	if entries[0].Writers[0].Config != http || entries[0].Writers[1].Config != nil || entries[1].Writers[0].Config != nil {
		t.Errorf("attached = %+v", entries)
	}
}
