package pages

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// Summary is a writer's configuration in one sentence -- a gateway stream's or
// an SDK pipeline's, for somebody who does
// not read YAML: what comes in, where it goes, how it is written, how often,
// and where what is refused goes.
//
// WRITTEN FROM THE SETTINGS, NEVER GENERATED. Every word here is a template
// filled from a field the gateway published, so the sentence is wrong only
// when the configuration is -- and a setting it has no words for is left out
// rather than guessed at. The full list is right below it.
func Summary(ctx context.Context, c *postgres.WriterConfig) string {
	tr := func(key string, args ...any) string { return i18n.T(ctx, key, args...) }
	f := map[string]any{}
	for _, fl := range c.Fields {
		f[fl.Path] = fl.Value
	}
	text := func(path string) string {
		if v, ok := f[path]; ok && v != nil {
			return valueText(v)
		}
		return ""
	}

	if text("source.driver") != "" {
		return pipelineSummary(ctx, c.Fields, text)
	}

	sink := text("sink.type")
	if sink == "" {
		return tr("summary.stream.nowhere")
	}

	var b strings.Builder
	b.WriteString(intake(ctx, text("format")))
	if p := text("path"); p != "" {
		b.WriteString(" " + tr("summary.stream.posted", "path", p))
	}

	switch sink {
	case "auto_table":
		tables := tr("summary.stream.tables.each")
		if allow, ok := f["sink.naming.allow"].([]any); ok && len(allow) > 0 {
			names := make([]string, len(allow))
			for i, a := range allow {
				names[i] = valueText(a) + "*"
			}
			tables = tr("summary.stream.tables.named", "names", orList(ctx, names))
		}
		b.WriteString(" " + tr("summary.stream.become", "tables", tables))
		if into := destination(ctx, text, "sink.into", false); into != "" {
			b.WriteString(" " + tr("summary.stream.in", "into", into))
		}
	case "pubsub":
		b.WriteString(" " + tr("summary.stream.published", "to", destination(ctx, text, "sink", true)))
	case "files":
		b.WriteString(" " + tr("summary.stream.written", "to", destination(ctx, text, "sink", true)))
	case "bigquery", "postgres", "mysql", "redshift":
		b.WriteString(" " + tr("summary.stream.land", "to", destination(ctx, text, "sink", true)))
	default:
		b.WriteString(" " + tr("summary.stream.sink", "sink", sink))
	}

	write := text("sink.write")
	if write == "" {
		write = text("sink.into.write")
	}
	switch write {
	case "append":
		b.WriteString(", " + tr("summary.stream.append"))
	case "merge":
		b.WriteString(", " + tr("summary.stream.merge"))
	}

	every, records := text("buffer.flush.every"), text("buffer.flush.records")
	switch {
	case every != "" && records != "":
		b.WriteString(", " + tr("summary.stream.batch.both", "every", durationWords(ctx, every), "records", events(ctx, records)))
	case every != "":
		b.WriteString(", " + tr("summary.stream.batch.every", "every", durationWords(ctx, every)))
	case records != "":
		b.WriteString(", " + tr("summary.stream.batch.records", "records", events(ctx, records)))
	}

	if dl := destination(ctx, text, "dead_letter", true); dl != "" {
		b.WriteString("; " + tr("summary.stream.refused", "to", dl))
	}
	b.WriteString(".")
	return b.String()
}

// intake is what a stream accepts, by its declared format.
func intake(ctx context.Context, format string) string {
	switch format {
	case "json":
		return i18n.T(ctx, "summary.intake.json")
	case "array":
		return i18n.T(ctx, "summary.intake.array")
	case "ndjson":
		return i18n.T(ctx, "summary.intake.ndjson")
	}
	return i18n.T(ctx, "summary.intake.events")
}

// destination names a sink in words: "BigQuery table p.d.t", "Pub/Sub topic
// p/t", "files at ./dead-letter/". withTable says whether a table is part of
// it -- auto_table's `into` names where tables go, not one of them.
func destination(ctx context.Context, text func(string) string, at string, withTable bool) string {
	get := func(k string) string { return text(at + "." + k) }
	table := ""
	if withTable {
		table = get("table")
	}
	tr := func(key string, args ...any) string { return strings.TrimSpace(i18n.T(ctx, key, args...)) }
	switch t := get("type"); t {
	case "":
		return ""
	case "bigquery":
		where := joinWords(".", get("project"), get("dataset"), table)
		if table != "" {
			return tr("summary.dest.bigquery.table", "where", where)
		}
		return tr("summary.dest.bigquery", "where", where)
	case "postgres", "mysql", "redshift":
		name := map[string]string{"postgres": "Postgres", "mysql": "MySQL", "redshift": "Redshift"}[t]
		if table != "" {
			return tr("summary.dest.table", "engine", name, "table", table)
		}
		return name
	case "pubsub":
		return tr("summary.dest.topic", "topic", joinWords("/", get("project"), get("topic")))
	case "files":
		return tr("summary.dest.files", "path", get("path"))
	default:
		return tr("summary.dest.other", "type", t)
	}
}

// orList is "a", "a or b", "a, b or c".
func orList(ctx context.Context, items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + i18n.T(ctx, "summary.or") + " " + items[len(items)-1]
}

// durationWords is a duration a person says: "1 minute", "500 milliseconds".
// Anything that is not one whole unit stays as written.
func durationWords(ctx context.Context, s string) string {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return s
	}
	switch {
	case d%time.Hour == 0:
		return i18n.N(ctx, "summary.unit.hour", int(d/time.Hour))
	case d%time.Minute == 0:
		return i18n.N(ctx, "summary.unit.minute", int(d/time.Minute))
	case d%time.Second == 0:
		return i18n.N(ctx, "summary.unit.second", int(d/time.Second))
	case d%time.Millisecond == 0:
		return i18n.N(ctx, "summary.unit.millisecond", int(d/time.Millisecond))
	}
	return s
}

// events is a count of events, "1 event" or "2,000 events", the thousands
// separated the reader's way.
func events(ctx context.Context, n string) string {
	v, err := strconv.ParseInt(n, 10, 64)
	if err != nil {
		return n
	}
	return i18n.N(ctx, "summary.events", int(v), "n", i18n.Number(ctx, v))
}

// pipelineSummary is an SDK pipeline in one sentence: what it reads, what it
// does to the records, where it writes, and how.
func pipelineSummary(ctx context.Context, fields []catalog.ManifestField, text func(string) string) string {
	tr := func(key string, args ...any) string { return strings.TrimSpace(i18n.T(ctx, key, args...)) }

	var b strings.Builder
	switch driver := text("source.driver"); driver {
	case "from.HTTP":
		b.WriteString(tr("summary.read.http", "what", joinWords(" ", text("source.method"), text("source.url"))))
	case "from.Files":
		b.WriteString(tr("summary.read.files", "path", text("source.path")))
	case "postgres.Query", "mysql.Query":
		engine := map[string]string{"postgres.Query": "Postgres", "mysql.Query": "MySQL"}[driver]
		b.WriteString(tr("summary.read.query", "engine", engine))
		if db := text("source.database"); db != "" {
			b.WriteString(" " + tr("summary.read.database", "db", db))
		}
		if host := text("source.host"); host != "" {
			b.WriteString(" " + tr("summary.read.host", "host", host))
		}
	case "from.Many":
		n := 0
		for _, f := range fields {
			if strings.HasPrefix(f.Path, "source.sources.") && strings.HasSuffix(f.Path, ".driver") {
				n++
			}
		}
		b.WriteString(i18n.N(ctx, "summary.read.many", n))
	default:
		b.WriteString(tr("summary.read.other", "driver", driver))
	}

	transformers := 0
	for _, f := range fields {
		if strings.HasPrefix(f.Path, "transform.") && strings.HasSuffix(f.Path, ".map") {
			if list, ok := f.Value.([]any); ok {
				transformers += len(list)
			}
		}
	}
	if transformers > 0 {
		b.WriteString(", " + i18n.N(ctx, "summary.applies", transformers))
	}
	for _, f := range fields {
		if strings.HasPrefix(f.Path, "transform.") && strings.HasSuffix(f.Path, ".aggregate.by") {
			b.WriteString(", " + tr("summary.groups", "by", valueText(f.Value)))
		}
	}

	target := text("target.driver")
	b.WriteString(" ")
	switch target {
	case "bigquery.Table":
		b.WriteString(tr("summary.write.bigquery", "table", joinWords(".", text("target.project"), text("target.dataset"), text("target.table"))))
	case "postgres.Table", "redshift.Table":
		engine := map[string]string{"postgres.Table": "Postgres", "redshift.Table": "Redshift"}[target]
		b.WriteString(tr("summary.write.table", "engine", engine, "table", joinWords(".", text("target.schema"), text("target.table"))))
	case "mysql.Table":
		b.WriteString(tr("summary.write.table", "engine", "MySQL", "table", joinWords(".", text("target.database"), text("target.table"))))
	case "pubsub.Topic":
		b.WriteString(tr("summary.write.topic", "topic", joinWords("/", text("target.project"), text("target.topic"))))
	case "to.Files":
		b.WriteString(tr("summary.write.files", "path", text("target.path")))
	default:
		b.WriteString(tr("summary.write.other", "driver", target))
		if t := text("target.target"); t != "" {
			b.WriteString(" (" + t + ")")
		}
	}

	switch {
	case target == "pubsub.Topic":
		// A message is published, not written into a table: neither word fits.
	case text("target.dedup") == "merge":
		b.WriteString(", " + tr("summary.pipeline.merge"))
	default:
		b.WriteString(", " + tr("summary.pipeline.append"))
	}
	b.WriteString(".")
	return b.String()
}
