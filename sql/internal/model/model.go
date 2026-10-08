// Package model reads one `.sql` file as a model: a header, a query, and the
// name the two of them make.
//
// THE CONFIG IS A COMMENT, so the file stays valid SQL. It opens in an editor
// with highlighting, it runs in a console by hand, and `psql -f` executes it.
// That is the whole reason the header is not front matter: a model you cannot
// paste into a client is a model you debug twice.
package model

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Materialisation is what a model becomes in the warehouse.
type Materialisation string

const (
	View  Materialisation = "view"
	Table Materialisation = "table"
)

// Model is one file, read.
type Model struct {
	// Schema and Name come from the PATH and never from the header:
	// `models/<schema>/<name>.sql` is `<schema>.<name>`. Two places to say
	// where a model lands is one place for them to disagree.
	Schema string
	Name   string

	Materialised Materialisation
	Tests        []Test
	// DependsOn is the explicit override for an edge the extractor cannot
	// see -- SQL built at runtime, or one of the forms refs names as its
	// limits. Added to what was inferred, never instead of it.
	DependsOn []string

	// SQL is the query with the header still in it. The header is a comment,
	// so the warehouse ignores it and the file that ran is the file on disk.
	SQL string
}

// Test is one assertion about a model, as a name and the columns it is about.
type Test struct {
	Kind    string
	Columns []string
	// Values is `accepted_values`' list; To and Field are `relationships`'.
	Values []string
	To     string
	Field  string
}

// Ref is `<schema>.<name>`, which is how a model is named everywhere else.
func (m Model) Ref() string { return m.Schema + "." + m.Name }

// header is the YAML shape, kept apart from Model so the file's vocabulary
// and the program's can differ without one dragging the other.
type header struct {
	Materialized string           `yaml:"materialized"`
	Tests        []map[string]any `yaml:"tests"`
	DependsOn    []string         `yaml:"depends_on"`
}

const openMark, closeMark = "/* brevis", "*/"

// Parse reads one model.
//
// `path` is both the name in every error and where the model's identity comes
// from, so it has to be the path as the project sees it.
func Parse(path string, content []byte) (Model, error) {
	schema, name, err := identify(path)
	if err != nil {
		return Model{}, err
	}
	m := Model{Schema: schema, Name: name, Materialised: View, SQL: string(content)}

	raw, firstLine, found := headerOf(string(content))
	if !found {
		// NOT an error. A model with no header is a view with no tests, which
		// is the smallest useful thing somebody can write, and making the
		// header mandatory would mean six lines of ceremony before the first
		// query runs.
		return m, nil
	}

	var h header
	dec := yaml.NewDecoder(strings.NewReader(raw))
	// Strict, and this format can afford it: nothing exists that was written
	// before these fields did. `materialised:` with an s is a typo the author
	// finds now rather than a model that silently stays a view.
	dec.KnownFields(true)
	if err := dec.Decode(&h); err != nil && err.Error() != "EOF" {
		return Model{}, fmt.Errorf("%s: the `/* brevis */ header is not valid YAML: %w",
			path, offsetLines(err, firstLine))
	}

	switch h.Materialized {
	case "":
	case string(View), string(Table):
		m.Materialised = Materialisation(h.Materialized)
	default:
		return Model{}, fmt.Errorf("%s: `materialized: %s` is not one this understands; "+
			"it is `view` or `table`", path, h.Materialized)
	}

	m.DependsOn = h.DependsOn
	for _, t := range h.Tests {
		parsed, err := readTest(path, t)
		if err != nil {
			return Model{}, err
		}
		m.Tests = append(m.Tests, parsed)
	}
	return m, nil
}

// identify turns `models/<schema>/<name>.sql` into the two halves.
func identify(path string) (schema, name string, err error) {
	clean := filepath.ToSlash(filepath.Clean(path))
	base := filepath.Base(clean)
	if filepath.Ext(base) != ".sql" {
		return "", "", fmt.Errorf("%s: a model is a `.sql` file", path)
	}
	name = strings.TrimSuffix(base, ".sql")

	dir := filepath.Base(filepath.Dir(clean))
	if dir == "." || dir == "/" || dir == "" || dir == "models" {
		return "", "", fmt.Errorf("%s: a model lives in `models/<schema>/<name>.sql`, "+
			"and the directory above it is the schema it lands in", path)
	}
	return dir, name, nil
}

// headerOf returns the YAML between the opening marker and `*/`, and the line
// the YAML starts on so an error can name the right one.
func headerOf(sql string) (yamlText string, firstLine int, found bool) {
	trimmed := strings.TrimLeft(sql, " \t\r\n")
	if !strings.HasPrefix(trimmed, openMark) {
		// Only a LEADING header counts. A `/* brevis */` halfway down is a
		// comment somebody wrote about the query, and reading it as config
		// would make a sentence into a setting.
		return "", 0, false
	}
	start := strings.Index(sql, openMark)
	end := strings.Index(sql[start:], closeMark)
	if end < 0 {
		return "", 0, false
	}
	body := sql[start+len(openMark) : start+end]
	return body, strings.Count(sql[:start], "\n") + 1, true
}

func readTest(path string, raw map[string]any) (Test, error) {
	if len(raw) != 1 {
		return Test{}, fmt.Errorf("%s: a test is one entry, `- not_null: [id]`; "+
			"this one has %d", path, len(raw))
	}
	for kind, body := range raw {
		t := Test{Kind: kind}
		switch kind {
		case "not_null", "unique":
			cols, ok := stringList(body)
			if !ok {
				return Test{}, fmt.Errorf("%s: `%s` takes a list of columns", path, kind)
			}
			t.Columns = cols
		case "accepted_values":
			m, ok := body.(map[string]any)
			if !ok {
				return Test{}, fmt.Errorf("%s: `accepted_values` takes `{column: …, values: […]}`", path)
			}
			col, _ := m["column"].(string)
			vals, _ := stringList(m["values"])
			if col == "" || len(vals) == 0 {
				return Test{}, fmt.Errorf("%s: `accepted_values` needs a `column` and a non-empty `values`", path)
			}
			t.Columns, t.Values = []string{col}, vals
		case "relationships":
			m, ok := body.(map[string]any)
			if !ok {
				return Test{}, fmt.Errorf("%s: `relationships` takes `{column: …, to: …, field: …}`", path)
			}
			col, _ := m["column"].(string)
			t.To, _ = m["to"].(string)
			t.Field, _ = m["field"].(string)
			if col == "" || t.To == "" || t.Field == "" {
				return Test{}, fmt.Errorf("%s: `relationships` needs `column`, `to` and `field`", path)
			}
			t.Columns = []string{col}
		default:
			return Test{}, fmt.Errorf("%s: `%s` is not a test this knows; they are "+
				"not_null, unique, accepted_values and relationships", path, kind)
		}
		return t, nil
	}
	return Test{}, nil
}

// stringList accepts a YAML list of scalars, which is what every one of these
// fields takes.
func stringList(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, i := range items {
		s, ok := i.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, len(out) > 0
}

// offsetLines moves a YAML error's line number to the file's.
//
// The decoder counts from the start of the fragment it was given, so an error
// in a six-line header of a file whose header starts at line 1 reads "line 3"
// and means line 3 -- but a header that starts lower down would send somebody
// to the wrong place, and the one thing an error about a config file owes is
// the line.
func offsetLines(err error, firstLine int) error {
	if firstLine <= 1 {
		return err
	}
	msg := err.Error()
	out := msg
	for n := 1; n <= 200; n++ {
		from := fmt.Sprintf("line %d:", n)
		if strings.Contains(msg, from) {
			out = strings.ReplaceAll(out, from, fmt.Sprintf("line %d:", n+firstLine-1))
		}
	}
	return fmt.Errorf("%s", out)
}
