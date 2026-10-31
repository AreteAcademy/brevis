package pages

import (
	"context"
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func fields(pairs ...any) *postgres.WriterConfig {
	c := &postgres.WriterConfig{}
	for i := 0; i+1 < len(pairs); i += 2 {
		c.Fields = append(c.Fields, catalog.ManifestField{Path: pairs[i].(string), Value: pairs[i+1]})
	}
	return c
}

// One sentence a person who does not read YAML can follow: what comes in,
// where it goes, how it is written, how often, and where the refused go.
func TestTheSummarySaysWhatTheStreamDoesInOneSentence(t *testing.T) {
	cases := []struct {
		name string
		c    *postgres.WriterConfig
		want string
	}{
		{"auto_table into BigQuery", fields(
			"path", "/v1/tables", "format", "json",
			"buffer.flush.every", "1m", "buffer.flush.records", float64(2000),
			"sink.type", "auto_table", "sink.naming.allow", []any{"app_", "svc_"},
			"sink.into.type", "bigquery", "sink.into.project", "acme-prod", "sink.into.dataset", "landing",
			"sink.into.write", "merge",
			"dead_letter.type", "files", "dead_letter.path", "./dead-letter/tables/"),
			"JSON events posted to /v1/tables become app_* or svc_* tables in BigQuery acme-prod.landing, " +
				"merged by ingestion id so a redelivery is ignored, in batches every 1 minute or 2,000 events; " +
				"batches the destination refuses go to files at ./dead-letter/tables/."},

		{"pubsub", fields(
			"path", "/v1/clicks", "format", "json",
			"buffer.flush.every", "1s", "buffer.flush.records", float64(500),
			"sink.type", "pubsub", "sink.project", "acme-prod", "sink.topic", "clicks",
			"dead_letter.type", "files", "dead_letter.path", "gs://acme-dead-letter/clicks/"),
			"JSON events posted to /v1/clicks are published to Pub/Sub topic acme-prod/clicks, " +
				"in batches every 1 second or 500 events; " +
				"batches the destination refuses go to files at gs://acme-dead-letter/clicks/."},

		{"postgres append", fields(
			"path", "/v1/orders", "format", "ndjson",
			"sink.type", "postgres", "sink.table", "landing.orders", "sink.write", "append"),
			"Newline-delimited JSON events posted to /v1/orders land in Postgres table landing.orders, " +
				"appended so a redelivery is a second row."},

		{"bigquery with a table", fields(
			"path", "/v1/telemetry", "format", "array",
			"sink.type", "bigquery", "sink.project", "acme-prod", "sink.dataset", "landing",
			"sink.table", "telemetry", "sink.write", "merge"),
			"JSON arrays of events posted to /v1/telemetry land in BigQuery table acme-prod.landing.telemetry, " +
				"merged by ingestion id so a redelivery is ignored."},

		{"auto_table with no allow list", fields(
			"path", "/v1/t", "format", "json", "sink.type", "auto_table",
			"sink.into.type", "postgres"),
			"JSON events posted to /v1/t become one table per event's table_name in Postgres."},

		{"a batch of one", fields(
			"path", "/v1/ingestion", "format", "json",
			"buffer.flush.every", "1m", "buffer.flush.records", float64(1),
			"sink.type", "pubsub", "sink.project", "p", "sink.topic", "t"),
			"JSON events posted to /v1/ingestion are published to Pub/Sub topic p/t, " +
				"in batches every 1 minute or 1 event."},

		{"a sink this page has no words for", fields(
			"path", "/v1/x", "format", "json", "sink.type", "kafka"),
			"JSON events posted to /v1/x are written to a kafka sink."},

		{"nothing to go on", fields(),
			"The configuration does not say where this stream writes."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Summary(context.Background(), c.c); got != c.want {
				t.Fatalf("\n got: %s\nwant: %s", got, c.want)
			}
		})
	}
}

func TestDurationsAreWrittenInWords(t *testing.T) {
	for in, want := range map[string]string{
		"1m": "1 minute", "5m": "5 minutes", "1s": "1 second", "60s": "1 minute",
		"500ms": "500 milliseconds", "1h": "1 hour", "1m30s": "90 seconds", "bogus": "bogus",
	} {
		if got := durationWords(context.Background(), in); got != want {
			t.Errorf("durationWords(%q) = %q, want %q", in, got, want)
		}
	}
}

// An SDK pipeline in one sentence: what it reads, what it does to it, where it
// writes, and how.
func TestTheSummarySaysWhatAPipelineDoes(t *testing.T) {
	cases := []struct {
		name string
		c    *postgres.WriterConfig
		want string
	}{
		{"http into bigquery, merged", fields(
			"source.driver", "from.HTTP", "source.url", "https://api.example.com/v1/items", "source.method", "GET",
			"transform.1.map", []any{"compute(provider)", "ingestion_id"},
			"target.driver", "bigquery.Table", "target.project", "acme-prod", "target.dataset", "landing",
			"target.table", "items", "target.dedup", "merge"),
			"Reads GET https://api.example.com/v1/items, applies 2 transformations " +
				"and writes to BigQuery table acme-prod.landing.items, merged by ingestion id so a re-run changes nothing."},

		{"files to files, with an aggregation", fields(
			"source.driver", "from.Files", "source.path", "gs://acme-raw/orders/",
			"transform.1.map", []any{"compute(area)"},
			"transform.2.aggregate.by", []any{"area", "year"}, "transform.2.aggregate.total", "sum(valor)",
			"target.driver", "to.Files", "target.path", "gs://acme-silver/orders/"),
			"Reads files at gs://acme-raw/orders/, applies 1 transformation, groups by area, year " +
				"and writes to files at gs://acme-silver/orders/, appended."},

		{"a query into postgres", fields(
			"source.driver", "postgres.Query", "source.host", "db.internal", "source.database", "shop",
			"target.driver", "postgres.Table", "target.schema", "landing", "target.table", "orders"),
			"Runs a query on Postgres database shop at db.internal " +
				"and writes to Postgres table landing.orders, appended."},

		{"many sources into pubsub", fields(
			"source.driver", "from.Many", "source.sources.1.driver", "from.HTTP", "source.sources.2.driver", "from.HTTP",
			"target.driver", "pubsub.Topic", "target.project", "acme-prod", "target.topic", "orders"),
			"Reads 2 sources and writes to Pub/Sub topic acme-prod/orders."},

		{"drivers this page has no words for", fields(
			"source.driver", "main.MySource", "target.driver", "main.MyWriter", "target.target", "postgres://dw/x/y"),
			"Reads from main.MySource and writes to main.MyWriter (postgres://dw/x/y), appended."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Summary(context.Background(), c.c); got != c.want {
				t.Fatalf("\n got: %s\nwant: %s", got, c.want)
			}
		})
	}
}
