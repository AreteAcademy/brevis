package pages

import (
	"strconv"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// Summary is a writer's configuration in one sentence, for somebody who does
// not read YAML: what comes in, where it goes, how it is written, how often,
// and where what is refused goes.
//
// WRITTEN FROM THE SETTINGS, NEVER GENERATED. Every word here is a template
// filled from a field the gateway published, so the sentence is wrong only
// when the configuration is -- and a setting it has no words for is left out
// rather than guessed at. The full list is right below it.
func Summary(c *postgres.WriterConfig) string {
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

	sink := text("sink.type")
	if sink == "" {
		return "The configuration does not say where this stream writes."
	}

	var b strings.Builder
	b.WriteString(intake(text("format")))
	if p := text("path"); p != "" {
		b.WriteString(" posted to " + p)
	}

	switch sink {
	case "auto_table":
		tables := "one table per event's table_name"
		if allow, ok := f["sink.naming.allow"].([]any); ok && len(allow) > 0 {
			names := make([]string, len(allow))
			for i, a := range allow {
				names[i] = valueText(a) + "*"
			}
			tables = orList(names) + " tables"
		}
		b.WriteString(" become " + tables)
		if into := destination(text, "sink.into", false); into != "" {
			b.WriteString(" in " + into)
		}
	case "pubsub":
		b.WriteString(" are published to " + destination(text, "sink", true))
	case "files":
		b.WriteString(" are written to " + destination(text, "sink", true))
	case "bigquery", "postgres", "mysql", "redshift":
		b.WriteString(" land in " + destination(text, "sink", true))
	default:
		b.WriteString(" are written to a " + sink + " sink")
	}

	write := text("sink.write")
	if write == "" {
		write = text("sink.into.write")
	}
	switch write {
	case "append":
		b.WriteString(", appended so a redelivery is a second row")
	case "merge":
		b.WriteString(", merged by ingestion id so a redelivery is ignored")
	}

	every, records := text("buffer.flush.every"), text("buffer.flush.records")
	switch {
	case every != "" && records != "":
		b.WriteString(", in batches every " + durationWords(every) + " or " + events(records))
	case every != "":
		b.WriteString(", in batches every " + durationWords(every))
	case records != "":
		b.WriteString(", in batches of up to " + events(records))
	}

	if dl := destination(text, "dead_letter", true); dl != "" {
		b.WriteString("; batches the destination refuses go to " + dl)
	}
	b.WriteString(".")
	return b.String()
}

// intake is what a stream accepts, by its declared format.
func intake(format string) string {
	switch format {
	case "json":
		return "JSON events"
	case "array":
		return "JSON arrays of events"
	case "ndjson":
		return "Newline-delimited JSON events"
	}
	return "Events"
}

// destination names a sink in words: "BigQuery table p.d.t", "Pub/Sub topic
// p/t", "files at ./dead-letter/". withTable says whether a table is part of
// it -- auto_table's `into` names where tables go, not one of them.
func destination(text func(string) string, at string, withTable bool) string {
	get := func(k string) string { return text(at + "." + k) }
	join := func(sep string, parts ...string) string {
		var kept []string
		for _, p := range parts {
			if p != "" {
				kept = append(kept, p)
			}
		}
		return strings.Join(kept, sep)
	}
	table := ""
	if withTable {
		table = get("table")
	}
	switch t := get("type"); t {
	case "":
		return ""
	case "bigquery":
		where := join(".", get("project"), get("dataset"), table)
		if table != "" {
			return "BigQuery table " + where
		}
		return join(" ", "BigQuery", where)
	case "postgres", "mysql", "redshift":
		name := map[string]string{"postgres": "Postgres", "mysql": "MySQL", "redshift": "Redshift"}[t]
		if table != "" {
			return name + " table " + table
		}
		return name
	case "pubsub":
		return join(" ", "Pub/Sub topic", join("/", get("project"), get("topic")))
	case "files":
		return join(" ", "files at", get("path"))
	default:
		return "a " + t + " sink"
	}
}

// orList is "a", "a or b", "a, b or c".
func orList(items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

// durationWords is a duration a person says: "1 minute", "500 milliseconds".
// Anything that is not one whole unit stays as written.
func durationWords(s string) string {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return s
	}
	unit := func(n int64, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.FormatInt(n, 10) + " " + one + "s"
	}
	switch {
	case d%time.Hour == 0:
		return unit(int64(d/time.Hour), "hour")
	case d%time.Minute == 0:
		return unit(int64(d/time.Minute), "minute")
	case d%time.Second == 0:
		return unit(int64(d/time.Second), "second")
	case d%time.Millisecond == 0:
		return unit(int64(d/time.Millisecond), "millisecond")
	}
	return s
}

// events is a count of events, "1 event" or "2,000 events".
func events(n string) string {
	if n == "1" {
		return "1 event"
	}
	return countWords(n) + " events"
}

// countWords is a whole number with thousands separated: 2,000.
func countWords(s string) string {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1000 {
		return s
	}
	digits := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
