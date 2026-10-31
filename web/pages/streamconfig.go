package pages

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// A stream's configuration, explained (screen-streams T4): the settings a
// gateway publishes in cards a person reads -- what comes in, when it is
// written, where, how it retries, the event's identity, where refusals go --
// each with a label in the reader's language and its YAML key under it. The
// key stays: it is what somebody searches the file for.

// ExplainedCard is one card: its key ("buffer") and its settings, in the
// order the label table gives them.
type ExplainedCard struct {
	Key  string
	Rows []ExplainedRow
}

// ExplainedRow is one setting: its path, its value as the file would write
// it, whether the gateway filled it in, and the file's note on it.
type ExplainedRow struct {
	Path, Value, Note string
	Default           bool
	label             string // the catalog key; "" for a setting with no words here
}

// LabelIn is the setting in the reader's words, or its key when this console
// has none for it.
func (r ExplainedRow) LabelIn(ctx context.Context) string {
	if r.label == "" {
		return r.Path
	}
	return i18n.T(ctx, r.label)
}

// cardOrder is the cards, in the order the page draws them; "other" holds
// what the label table does not know.
var cardOrder = []string{"intake", "buffer", "destination", "retry", "identity", "dead_letter", "other"}

// streamLabels is every setting this console has words for: its card and the
// catalog key of its label, in the order a card lists them. A setting the
// gateway adds tomorrow is not here, and is shown under "other" by its key
// rather than dropped.
var streamLabels = []struct{ path, card, label string }{
	{"name", "intake", "streamcfg.name"},
	{"path", "intake", "streamcfg.path"},
	{"format", "intake", "streamcfg.format"},
	{"hook", "intake", "streamcfg.hook"},
	{"stamp_loaded_at", "intake", "streamcfg.stamp_loaded_at"},

	{"buffer.durability", "buffer", "streamcfg.buffer.durability"},
	{"buffer.flush.every", "buffer", "streamcfg.buffer.every"},
	{"buffer.flush.records", "buffer", "streamcfg.buffer.records"},
	{"buffer.flush.max_age", "buffer", "streamcfg.buffer.max_age"},
	{"buffer.flush.size", "buffer", "streamcfg.buffer.size"},
	{"buffer.flush.claim", "buffer", "streamcfg.buffer.claim"},
	{"buffer.workers", "buffer", "streamcfg.buffer.workers"},
	{"buffer.queue", "buffer", "streamcfg.buffer.queue"},
	{"buffer.max_records", "buffer", "streamcfg.buffer.max_records"},
	{"buffer.max_bytes", "buffer", "streamcfg.buffer.max_bytes"},

	{"sink.type", "destination", "streamcfg.sink.type"},
	{"sink.naming.allow", "destination", "streamcfg.sink.allow"},
	{"sink.naming.pattern", "destination", "streamcfg.sink.pattern"},
	{"sink.naming.max_new_per_hour", "destination", "streamcfg.sink.max_new_per_hour"},
	{"sink.project", "destination", "streamcfg.sink.project"},
	{"sink.dataset", "destination", "streamcfg.sink.dataset"},
	{"sink.table", "destination", "streamcfg.sink.table"},
	{"sink.topic", "destination", "streamcfg.sink.topic"},
	{"sink.write", "destination", "streamcfg.sink.write"},
	{"sink.into.type", "destination", "streamcfg.sink.type"},
	{"sink.into.project", "destination", "streamcfg.sink.project"},
	{"sink.into.dataset", "destination", "streamcfg.sink.dataset"},
	{"sink.into.write", "destination", "streamcfg.sink.write"},
	{"sink.shape", "destination", "streamcfg.sink.shape"},
	{"sink.attributes", "destination", "streamcfg.sink.attributes"},
	{"sink.dsn_from", "destination", "streamcfg.sink.dsn_from"},
	{"sink.metastore.type", "destination", "streamcfg.sink.metastore"},
	{"sink.metastore.addr_from", "destination", "streamcfg.sink.metastore_addr"},
	{"sink.metastore.ttl", "destination", "streamcfg.sink.metastore_ttl"},

	{"retry.attempts", "retry", "streamcfg.retry.attempts"},
	{"retry.backoff", "retry", "streamcfg.retry.backoff"},
	{"retry.max_backoff", "retry", "streamcfg.retry.max_backoff"},

	{"identity.provider", "identity", "streamcfg.identity.provider"},
	{"identity.entity", "identity", "streamcfg.identity.entity"},
	{"identity.source_key", "identity", "streamcfg.identity.source_key"},
	{"identity.record_ts", "identity", "streamcfg.identity.record_ts"},

	{"dead_letter.type", "dead_letter", "streamcfg.dead_letter.type"},
	{"dead_letter.path", "dead_letter", "streamcfg.dead_letter.path"},
	{"oversize.larger_than", "dead_letter", "streamcfg.oversize.larger_than"},
	{"oversize.hook", "dead_letter", "streamcfg.oversize.hook"},
	{"oversize.archive.type", "dead_letter", "streamcfg.oversize.archive_type"},
	{"oversize.archive.path", "dead_letter", "streamcfg.oversize.archive_path"},
}

// cardTitle is a card's heading in the reader's language.
func cardTitle(ctx context.Context, key string) string {
	switch key {
	case "intake":
		return i18n.T(ctx, "streamcfg.card.intake")
	case "buffer":
		return i18n.T(ctx, "streamcfg.card.buffer")
	case "destination":
		return i18n.T(ctx, "streamcfg.card.destination")
	case "retry":
		return i18n.T(ctx, "streamcfg.card.retry")
	case "identity":
		return i18n.T(ctx, "streamcfg.card.identity")
	case "dead_letter":
		return i18n.T(ctx, "streamcfg.card.dead_letter")
	}
	return i18n.T(ctx, "streamcfg.card.other")
}

// ExplainConfig lays a stream's settings out in cards. A card with nothing
// in it is left out.
func ExplainConfig(c *postgres.WriterConfig) []ExplainedCard {
	if c == nil {
		return nil
	}
	byPath := map[string]ExplainedRow{}
	for _, f := range c.Fields {
		byPath[f.Path] = ExplainedRow{Path: f.Path, Value: valueText(f.Value), Default: f.Default, Note: c.Notes[f.Path]}
	}
	rows := map[string][]ExplainedRow{}
	known := map[string]bool{}
	for _, l := range streamLabels {
		known[l.path] = true
		if r, ok := byPath[l.path]; ok {
			r.label = l.label
			rows[l.card] = append(rows[l.card], r)
		}
	}
	for _, f := range c.Fields {
		if !known[f.Path] {
			rows["other"] = append(rows["other"], byPath[f.Path])
		}
	}
	var out []ExplainedCard
	for _, k := range cardOrder {
		if len(rows[k]) > 0 {
			out = append(out, ExplainedCard{Key: k, Rows: rows[k]})
		}
	}
	return out
}

// ConfigYAML is the stream written back as YAML from what was published: its
// settings nested by their keys in the order they came, lists as lists, and
// the file's notes as comments above what they are about. It is not the
// gateway's file -- its ordering and quoting are the console's -- and the
// page says so.
func ConfigYAML(c *postgres.WriterConfig) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	comment := func(note, indent string) {
		for _, line := range strings.Split(strings.TrimSpace(note), "\n") {
			b.WriteString(indent + "# " + strings.TrimSpace(line) + "\n")
		}
	}
	if n := c.Notes[""]; n != "" {
		comment(n, "")
	}
	// A tree, so a setting that comes after another block's still lands in
	// its own: each block where it first appeared, its children in the
	// order they came.
	type node struct {
		key      string
		value    any
		leaf     bool
		children []*node
	}
	root := &node{}
	child := func(n *node, key string) *node {
		for _, c := range n.children {
			if c.key == key {
				return c
			}
		}
		c := &node{key: key}
		n.children = append(n.children, c)
		return c
	}
	for _, f := range c.Fields {
		n := root
		for _, part := range strings.Split(f.Path, ".") {
			n = child(n, part)
		}
		n.value, n.leaf = f.Value, true
	}
	var write func(n *node, path string, depth int)
	write = func(n *node, path string, depth int) {
		indent := strings.Repeat("  ", depth)
		if note := c.Notes[path]; note != "" {
			comment(note, indent)
		}
		switch {
		case !n.leaf:
			b.WriteString(indent + n.key + ":\n")
			for _, ch := range n.children {
				write(ch, path+"."+ch.key, depth+1)
			}
		default:
			if list, ok := n.value.([]any); ok {
				b.WriteString(indent + n.key + ":\n")
				for _, e := range list {
					b.WriteString(indent + "  - " + yamlScalar(e) + "\n")
				}
				return
			}
			b.WriteString(indent + n.key + ": " + yamlScalar(n.value) + "\n")
		}
	}
	for _, ch := range root.children {
		write(ch, ch.key, 0)
	}
	return b.String()
}

// plainUnsafe is what a YAML reader would take for something other than the
// string written: a bool, a null, a number, or a start or a sequence YAML
// gives a meaning to.
var plainUnsafe = regexp.MustCompile(`^$|^(true|false|yes|no|on|off|null|~)$|^[-+]?(\.?[0-9]+|[0-9]+\.[0-9]*)([eE][-+]?[0-9]+)?$|^[*&!|>'"%@#\[\]{},?:` + "`" + `]|^- |: | #|:$`)

// yamlScalar is one value as YAML writes it, quoted only when it has to be.
func yamlScalar(v any) string {
	s, isString := v.(string)
	if !isString {
		return valueText(v)
	}
	if plainUnsafe.MatchString(strings.ToLower(s)) {
		return strconv.Quote(s)
	}
	return s
}
