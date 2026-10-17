package pages

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
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

	// Changes are the writer's configurations over time, newest first, each
	// with what it changed from the one before.
	Changes []ConfigChange

	// DeadLetter is where a gateway stream's refused batches go and how to get
	// them back; nil for a writer that published none.
	DeadLetter *DeadLetterView
}

// DeadLetterView is the dead-letter panel of a gateway stream (#86): where the
// buried batches are, and the commands that read them back and resend them --
// run where the destination's credentials are. The console reads nothing of
// the dead letter itself and calls nothing: it only knows what was published.
type DeadLetterView struct {
	// Where is the dead letter in the summary's words: "files at gs://…".
	Where string
	// Stream is the stream's NAME, which --stream takes; the page's own key
	// is the stream's path.
	Stream string
	// Readable is a `files` dead letter, the only kind the commands read.
	Readable bool
	// Write is the destination's write mode: append, merge, or empty for a
	// sink with no such notion -- Pub/Sub publishes again whatever it is
	// given. Read on the clicks stream of the local stack, which a merge
	// sentence described wrongly.
	Write string
}

// Commands are the two lines to copy, one per line. A string and not markup
// inside the <pre>: templ joins the lines of a template's text, and two
// commands pasted as one is a command that fails.
//
// <gateway.yaml> is a placeholder because the manifest does not carry where
// the config sits inside the gateway's pod.
func (d DeadLetterView) Commands() string {
	return "gateway deadletter ls <gateway.yaml> --stream " + d.Stream + "\n" +
		"gateway deadletter resend <gateway.yaml> <batch> --dry-run"
}

// deadLetterOf builds the panel from the published fields, or nil.
func deadLetterOf(c *postgres.WriterConfig) *DeadLetterView {
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
	kind := text("dead_letter.type")
	if kind == "" {
		return nil
	}
	write := text("sink.write")
	if text("sink.type") == "auto_table" {
		write = text("sink.into.write")
	}
	return &DeadLetterView{
		Where:    destination(text, "dead_letter", true),
		Stream:   text("name"),
		Readable: kind == "files",
		Write:    write,
	}
}

// ConfigChange is one version: from when, and what it changed.
type ConfigChange struct {
	At    time.Time
	Lines []ChangeLine

	// RunID is the run that first wrote with it, for a step's.
	RunID string

	// First marks the oldest version on record -- where the record begins,
	// with nothing before it to compare. False on the oldest of a history cut
	// at HistoryShown, which is not the first.
	First bool
}

// ChangeLine is one setting's change, in words: "1m → 2m".
type ChangeLine struct {
	Path, Text string
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

	// List is the value's items when it is a list (`declared.schema`), each
	// as Value would write it; nil otherwise.
	List []string

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
// it is: a gateway stream's (Gateway set) or a workflow step's.
type WriterConfigView struct {
	Gateway, Stream, Role string
	PublishedAt           time.Time

	Workflow, Node string

	// Described is false for a step that never sent its configuration --
	// Python, dbt, a shell. Its section says so instead of being left out:
	// a writer missing from the list reads as nothing to see.
	Described bool

	Config ConfigView

	// Flow is how data reaches the destination through this writer, drawn
	// from what its configuration declares; nil when there is none to draw.
	Flow *Flow
}

// Key is what the page tags the section with: "gateway stream" or
// "workflow node".
func (w WriterConfigView) Key() string {
	if w.Gateway != "" {
		return w.Gateway + " " + w.Stream
	}
	return w.Workflow + " " + w.Node
}

// BuildConfig lays a configuration out by its paths alone, so a setting a
// writer adds tomorrow has a place on the page with no change here.
func BuildConfig(c *postgres.WriterConfig) ConfigView {
	v := ConfigView{Summary: Summary(c), Note: c.Notes[""], DeadLetter: deadLetterOf(c)}
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
		if items, ok := f.Value.([]any); ok {
			for _, e := range items {
				row.List = append(row.List, valueText(e))
			}
		}
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
	v.Changes = buildChanges(c.History)
	return v
}

// buildChanges compares each version with the one before it.
func buildChanges(h []postgres.ConfigVersion) []ConfigChange {
	out := make([]ConfigChange, len(h))
	for i, version := range h {
		out[i].At, out[i].RunID = version.At, version.RunID
		if i+1 == len(h) {
			out[i].First = len(h) < postgres.HistoryShown
			continue
		}
		for _, c := range catalog.Diff(h[i+1].Fields, version.Fields) {
			out[i].Lines = append(out[i].Lines, ChangeLine{Path: c.Path, Text: changeText(c)})
		}
	}
	return out
}

// changeText is one change as a person reads it.
func changeText(c catalog.Change) string {
	switch c.Kind {
	case catalog.Added:
		return "added: " + valueText(c.After)
	case catalog.Removed:
		return "removed (was " + valueText(c.Before) + ")"
	case catalog.NowWritten:
		return valueText(c.After) + ", now written in the file"
	case catalog.NowDefault:
		return valueText(c.After) + ", now the default"
	}
	return valueText(c.Before) + " → " + valueText(c.After)
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
