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

// richPipeline is an SDK pipeline that fills every part of the drawer's
// configuration: three sources, a transformation and an aggregation, a
// declared schema, a merge, and a change in its history.
func richPipeline() *postgres.WriterConfig {
	f := func(p string, v any) catalog.ManifestField { return catalog.ManifestField{Path: p, Value: v} }
	fields := []catalog.ManifestField{
		f("source.driver", "from.Many"), f("source.workers", 2.0),
		f("source.sources.1.driver", "from.Files"), f("source.sources.1.path", "/in/a.csv"),
		f("source.sources.2.driver", "from.Files"), f("source.sources.2.path", "/in/b.csv"),
		f("source.sources.3.driver", "from.HTTP"), f("source.sources.3.url", "https://api.acme.io"),
		f("transform.1.map", []any{"compute(loaded_by)"}),
		f("transform.2.aggregate.by", []any{"sku"}), f("transform.2.aggregate.qty", "sum(qty)"),
		f("target.driver", "bigquery.Table"), f("target.project", "acme"), f("target.dataset", "bronze"), f("target.table", "orders"),
		f("target.declared.columns", []any{"sku", "qty"}), f("target.declared.schema", []any{"sku STRING", "qty INT64"}),
		f("target.dedup", "merge"),
	}
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	old := append([]catalog.ManifestField{}, fields[:len(fields)-1]...)
	old = append(old, f("target.dedup", "none"), f("target.timeout", "30s"))
	return &postgres.WriterConfig{Fields: fields, History: []postgres.ConfigVersion{
		{Fields: fields, At: at, RunID: "aaaaaaaa-0000-0000-0000-000000000001"},
		{Fields: old, At: at.Add(-time.Hour), RunID: "aaaaaaaa-0000-0000-0000-000000000002"},
	}}
}

func configDrawn(t *testing.T, lang string) string {
	t.Helper()
	w := hourlyWriter("orders", "load", clock(11, 5), true)
	w.Config = richPipeline()
	d := postgres.TargetDetail{CatalogEntry: postgres.CatalogEntry{Target: "bigquery://acme/bronze/orders", Kind: "bigquery",
		Writers: []postgres.CatalogWriter{w}}}
	l := BuildLoad(d, "orders", "load", catalogNow)
	undescribed := BuildLoad(postgres.TargetDetail{CatalogEntry: postgres.CatalogEntry{Target: "s3://x/", Kind: "s3",
		Writers: []postgres.CatalogWriter{hourlyWriter("py", "run", clock(11, 0), true)}}}, "py", "run", catalogNow)
	return visibleText(drawIn(t, lang, loadConfiguration(l))) + " " + visibleText(drawIn(t, lang, loadChanges(l))) + " " +
		visibleText(drawIn(t, lang, loadConfiguration(undescribed)))
}

// A LOAD'S CONFIGURATION SPEAKS THE READER'S LANGUAGE (screen-loads T5): the
// frame, the tabs, the one-line summary, the stages, the flow, what the
// sources share, the schema, the changes and the step that described
// nothing. The settings' names and values are the data's own.
func TestTheConfigurationHasNoEnglishLeft(t *testing.T) {
	text := configDrawn(t, "pt-BR")
	for _, english := range []string{
		"Configuration", "Summary", "Sources", "Transform ", "Destination", "Schema",
		"Reads", "applies", "transformation", "groups by", "writes to", "merged by", "re-run",
		// "workers" alone is a setting's name, the data's own.
		"sources ·", "2 workers", "stages", "Source ", "Common to", "every setting differs",
		"Kind", "Where", "Object", "Column", "Type", "Role", "changes", "removed (was", "First recorded",
		"did not describe", "Only the Go SDK", "definition",
	} {
		if at := strings.Index(text, english); at >= 0 {
			t.Errorf("the Portuguese configuration still says %q: …%s…", english, text[max(0, at-60):min(len(text), at+40)])
		}
	}
	// The data's own words stay as they are.
	for _, data := range []string{"from.Many", "bigquery.Table", "compute(loaded_by)", "target.dedup", "dedup merge"} {
		if !strings.Contains(text, data) {
			t.Errorf("the configuration lost %q", data)
		}
	}
}

// THE SUMMARY IS ONE SENTENCE IN EACH LANGUAGE, a pipeline's and a stream's.
func TestTheSummaryIsInEachLanguage(t *testing.T) {
	stream := &postgres.WriterConfig{Fields: []catalog.ManifestField{
		{Path: "format", Value: "json"}, {Path: "path", Value: "/v1/clicks"},
		{Path: "sink.type", Value: "pubsub"}, {Path: "sink.project", Value: "acme"}, {Path: "sink.topic", Value: "clicks"},
		{Path: "sink.write", Value: "merge"}, {Path: "buffer.flush.every", Value: "1m"}, {Path: "buffer.flush.records", Value: 2000.0},
		{Path: "dead_letter.type", Value: "files"}, {Path: "dead_letter.path", Value: "./dl/"},
	}}
	for lang, want := range map[string][2]string{
		"en":    {"Reads 3 sources, applies 1 transformation, groups by sku and writes to BigQuery table acme.bronze.orders, merged by ingestion id so a re-run changes nothing.", "JSON events posted to /v1/clicks are published to Pub/Sub topic acme/clicks, merged by ingestion id so a redelivery is ignored, in batches every 1 minute or 2,000 events; batches the destination refuses go to files at ./dl/."},
		"pt-BR": {"Lê 3 origens, aplica 1 transformação, agrupa por sku e grava na tabela BigQuery acme.bronze.orders, mesclando pelo id de ingestão, então reexecutar não muda nada.", "Eventos JSON enviados para /v1/clicks são publicados no tópico Pub/Sub acme/clicks, mesclando pelo id de ingestão, então uma reentrega é ignorada, em lotes a cada 1 minuto ou 2.000 eventos; os lotes que o destino recusa vão para arquivos em ./dl/."},
		"es":    {"Lee 3 orígenes, aplica 1 transformación, agrupa por sku y escribe en la tabla BigQuery acme.bronze.orders, fusionando por id de ingesta, así que reejecutar no cambia nada.", "Eventos JSON enviados a /v1/clicks se publican en el tópico Pub/Sub acme/clicks, fusionando por id de ingesta, así que una reentrega se ignora, en lotes cada 1 minuto o 2.000 eventos; los lotes que el destino rechaza van a archivos en ./dl/."},
	} {
		ctx := i18n.WithLang(context.Background(), lang)
		if got := Summary(ctx, richPipeline()); got != want[0] {
			t.Errorf("%s pipeline:\n got %s\nwant %s", lang, got, want[0])
		}
		if got := Summary(ctx, stream); got != want[1] {
			t.Errorf("%s stream:\n got %s\nwant %s", lang, got, want[1])
		}
	}
}
