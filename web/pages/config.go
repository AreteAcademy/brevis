package pages

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// ConfigView is one writer's configuration, laid out for a page: the
// writer's own note, then its settings in sections, in the order the
// writer declared them.
type ConfigView struct {
	// Summary is the configuration in one sentence. See Summary.
	Summary string

	// Note is the comment on the writer as a whole -- for a gateway, the one
	// above `- name:`, which is usually the contract its producers follow.
	Note     string
	Sections []ConfigSection
}

// ConfigSection is one block of settings: `buffer`, `sink`. Settings with no
// block of their own -- name, path, format -- are the "Stream" section.
type ConfigSection struct {
	Key, Title string

	// Note is the comment on the block's own key.
	Note string

	Rows []ConfigRow
}

// ConfigRow is one setting.
type ConfigRow struct {
	// Path is the setting's full path, and what the page tags the row with.
	Path string

	// Label is the path inside the section: `flush.every` under Buffer.
	Label string

	Value   string
	Default bool
	Note    string

	// Before are the comments of the blocks this row is the first setting of:
	// `sink.metastore`'s, before `metastore.type`. Shown once, where the block
	// begins, the way the file shows them.
	Before []ConfigNote
}

// ConfigNote is a block's comment, by the block's path inside its section.
type ConfigNote struct {
	Label, Text string
}

// WriterConfigView is a configuration on a destination's page, with whose
// it is.
type WriterConfigView struct {
	Gateway, Stream, Role string
	PublishedAt           time.Time
	Config                ConfigView
}

// BuildConfig lays a configuration out by its paths alone, so a setting a
// writer adds tomorrow has a place on the page with no change here.
func BuildConfig(c *postgres.WriterConfig) ConfigView {
	v := ConfigView{Summary: Summary(c), Note: c.Notes[""]}
	index := map[string]int{}
	before := map[string]bool{}

	for _, f := range c.Fields {
		key, label := "", f.Path
		if head, rest, ok := strings.Cut(f.Path, "."); ok {
			key, label = head, rest
		}
		i, ok := index[key]
		if !ok {
			i = len(v.Sections)
			index[key] = i
			sec := ConfigSection{Key: key, Title: sectionTitle(key)}
			// The Stream section's key is "", which is also where the
			// writer's own note lives -- already shown above the sections.
			if key != "" {
				sec.Note = c.Notes[key]
			}
			v.Sections = append(v.Sections, sec)
		}

		row := ConfigRow{Path: f.Path, Label: label, Value: valueText(f.Value),
			Default: f.Default, Note: c.Notes[f.Path]}
		// Every block between the section and the setting: `sink.metastore`
		// for `sink.metastore.ttl`, `sink.into` for `sink.into.write`.
		parts := strings.Split(f.Path, ".")
		for n := 2; n < len(parts); n++ {
			block := strings.Join(parts[:n], ".")
			if before[block] {
				continue
			}
			before[block] = true
			if text := c.Notes[block]; text != "" {
				row.Before = append(row.Before, ConfigNote{Label: strings.Join(parts[1:n], "."), Text: text})
			}
		}
		v.Sections[i].Rows = append(v.Sections[i].Rows, row)
	}
	return v
}

// sectionTitle is a block's key as a heading: `dead_letter` is "Dead letter".
func sectionTitle(key string) string {
	if key == "" {
		return "Stream"
	}
	t := strings.ReplaceAll(key, "_", " ")
	return strings.ToUpper(t[:1]) + t[1:]
}

// valueText is a setting as the file would write it. A number that JSON
// carried as a float is an integer when it is one: `4`, not `4.000000`.
func valueText(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		parts := make([]string, len(v))
		for i, e := range v {
			parts[i] = valueText(e)
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(v)
}

// NoteHead is a note's first line, which is what a closed note shows.
func NoteHead(note string) string {
	head, _, _ := strings.Cut(note, "\n")
	return head
}

// NoteRest is everything after the first line, or "" for a one-line note.
func NoteRest(note string) string {
	_, rest, _ := strings.Cut(note, "\n")
	return strings.TrimSpace(rest)
}

// boolText is a bool as an attribute value: data-defaulted="true".
func boolText(b bool) string { return strconv.FormatBool(b) }
