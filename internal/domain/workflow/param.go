package workflow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Param is a run parameter: what changes between two triggers of the SAME
// workflow without editing the file.
//
// It was the largest distance between this engine and Kestra/Leoflow. Without
// params there is no backfill (`load_full=true`) and no window reprocessing, and
// eight of the data repository's 51 flows could not even be converted — their
// command carries `{{ inputs.start_date }}` and the like.
// The tags below are the ON-DISK FORMAT, and they keep the Portuguese field
// names the Go struct used to have.
//
// A Workflow is stored whole as JSON in `workflows.definicao` and
// `runs.definicao`, with no tags of its own -- so the Go field NAME was the
// key. Every workflow published before this rename holds `{"Nome":"date",
// "Tipo":"string","Descricao":"..."}`, and json.Unmarshal ignores a key it
// does not know: reading one with the fields renamed and untagged gives a param
// with no name, no type and no description, with no error and no log. The
// trigger form would render an empty field and the validation would refuse a
// value the author declared as valid.
//
// Pinning the keys costs three tags and keeps both directions working: an
// engine on either side of this commit reads and writes the same document.
type Param struct {
	Name        string    `json:"Nome"`
	Type        ParamType `json:"Tipo"`
	Default     string    `json:"Default"`
	Description string    `json:"Descricao"`

	// Enum restricts the accepted values. Empty = any that passes the type.
	Enum []string `json:"Enum"`

	// Pattern is a regular expression the value has to match. It exists for the
	// author to WIDEN what the `string` type accepts by default — see
	// safeCharacters.
	Pattern string `json:"Pattern"`
}

type ParamType string

const (
	ParamString  ParamType = "string"
	ParamBool    ParamType = "boolean"
	ParamInteger ParamType = "integer"

	// A list is `list|<element type>`: list|string, list|integer, list|boolean.
	//
	// The value travels as ONE comma-separated string, all the way from the
	// form to the step's environment, because every param does: they are a
	// map[string]string in the database column, in BREVIS_RUN_PARAMS and in
	// Render. Making one of them an array would turn that map into
	// map[string]any -- and an SDK built before this change unmarshals the env
	// var into map[string]string, so it would fail and discard EVERY param of
	// that run, with a warning nobody reads and a pipeline that runs with the
	// defaults.
	//
	// The comma is also what makes `{{ .tables }}` keep working in a command:
	// `--select users,orders` is what a dbt selector wants, so a list needs no
	// special case in Render and no new template function. That is paid for by
	// a comma being forbidden INSIDE an element -- see Accepts.
	ParamListPrefix = "list|"
)

// ElementType is the type of each item of a list param, and "" for a param that
// is not a list.
func (t ParamType) ElementType() ParamType {
	if !strings.HasPrefix(string(t), ParamListPrefix) {
		return ""
	}
	return ParamType(strings.TrimPrefix(string(t), ParamListPrefix))
}

// IsList answers whether this param carries many values.
func (t ParamType) IsList() bool { return t.ElementType() != "" }

// scalarTypes is what a param, or a list's element, may be.
var scalarTypes = map[ParamType]bool{ParamString: true, ParamBool: true, ParamInteger: true}

// Items splits a list param's stored value.
//
// The empty string is an EMPTY list and not a list holding one empty string:
// a param nobody filled in has no items, and `for x in $(...)` over one empty
// element runs the body once on nothing.
func (p Param) Items(value string) []string {
	if !p.Type.IsList() || value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, item := range parts {
		out = append(out, strings.TrimSpace(item))
	}
	return out
}

// safeCharacters is what a `string` accepts when the author declares no
// `pattern`.
//
// This is a defence against shell injection, not purism: a param's value goes
// INTO the step's command line, and whoever triggers a run is not necessarily
// whoever wrote the workflow. Without the restriction,
// `--date {{ .date }}` with `date = "; rm -rf /"` would be arbitrary execution
// on the worker.
//
// The set covers what this repository's real params need — dates, dbt selectors,
// uids, paths, comma-separated lists — and leaves out everything the shell
// interprets: quotes, `;`, `|`, `&`, `$`, backticks, parentheses and
// redirections.
var safeCharacters = regexp.MustCompile(`^[A-Za-z0-9_.:/=,+@\- ]*$`)

var paramName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Validate checks the param's declaration, not the value.
func (p Param) Validate() error {
	if !paramName.MatchString(p.Name) {
		return fmt.Errorf("param %q: the name has to be lowercase, start with a letter and hold only letters, digits and _", p.Name)
	}
	switch {
	case p.Type == "":
		return fmt.Errorf("param %q has no type (string, boolean, integer, "+
			"or list|<type> for many values)", p.Name)
	case scalarTypes[p.Type]:
	case p.Type.IsList():
		if !scalarTypes[p.Type.ElementType()] {
			return fmt.Errorf("param %q: %q is not a valid list element type "+
				"(use list|string, list|integer or list|boolean)",
				p.Name, p.Type.ElementType())
		}
	default:
		return fmt.Errorf("param %q: unknown type %q (use string, boolean, integer, "+
			"or list|<type> for many values)", p.Name, p.Type)
	}
	// An enum on a list restricts each ITEM, which is what a multi-select is.
	if len(p.Enum) > 0 && p.Type.IsList() {
		for _, allowed := range p.Enum {
			if strings.Contains(allowed, ",") {
				return fmt.Errorf("param %q: the enum value %q holds a comma, "+
					"which is what separates the items of a list", p.Name, allowed)
			}
		}
	}
	if p.Pattern != "" {
		if _, err := regexp.Compile(p.Pattern); err != nil {
			return fmt.Errorf("param %q: the pattern is not valid: %w", p.Name, err)
		}
	}
	// The default has to be valid by its own rules: a refused default would only
	// surface on the first scheduled run, in the small hours.
	if p.Default != "" {
		if err := p.Accepts(p.Default); err != nil {
			return fmt.Errorf("param %q: the default value is not valid: %w", p.Name, err)
		}
	}
	return nil
}

// Accepts validates a VALUE against the declaration.
//
// For a list, each item is validated on its own by the element's rules -- so
// `list|integer` refuses "3,x,5" naming x, and an `enum` restricts every item
// rather than the whole string.
func (p Param) Accepts(value string) error {
	if p.Type.IsList() {
		return p.acceptsList(value)
	}
	return p.acceptsScalar(value, p.Type)
}

func (p Param) acceptsList(value string) error {
	if value == "" {
		return nil
	}
	// A comma is the separator, so an item cannot hold one. The alternative was
	// JSON in the string, which would have made `{{ .tables }}` render
	// `["a","b"]` into a shell command -- quotes and brackets that safeCharacters
	// refuses and that the shell would mangle anyway.
	elem := p.Type.ElementType()
	seen := make(map[string]bool)
	for i, item := range p.Items(value) {
		if item == "" {
			return fmt.Errorf("item %d is empty; a list is items separated by "+
				"commas, with no empty ones", i+1)
		}
		if seen[item] {
			// A repeated item is nearly always a mistake in a form, and it
			// silently doubles whatever the step does per item.
			return fmt.Errorf("item %q appears more than once", item)
		}
		seen[item] = true
		if err := p.acceptsScalar(item, elem); err != nil {
			return fmt.Errorf("item %d: %w", i+1, err)
		}
	}
	return nil
}

func (p Param) acceptsScalar(value string, kind ParamType) error {
	switch kind {
	case ParamBool:
		if value != "true" && value != "false" {
			return fmt.Errorf("%q is not a boolean (use true or false)", value)
		}
		return nil
	case ParamInteger:
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("%q is not an integer", value)
		}
		return nil
	}

	if len(p.Enum) > 0 {
		for _, allowed := range p.Enum {
			if value == allowed {
				return nil
			}
		}
		return fmt.Errorf("%q is not one of the accepted values (%s)", value, strings.Join(p.Enum, ", "))
	}
	if p.Pattern != "" {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return err
		}
		if !re.MatchString(value) {
			return fmt.Errorf("%q does not match the pattern %q", value, p.Pattern)
		}
		return nil
	}
	if !safeCharacters.MatchString(value) {
		return fmt.Errorf("%q has a character the shell interprets; "+
			"declare a `pattern` on the param if the value genuinely needs it", value)
	}
	return nil
}

// Resolver merges the supplied values with the defaults and validates all of
// them.
//
// An unknown key is an ERROR, not silence: `--param lod_full=true` with a typo
// would run the workflow with the default and nobody would notice the backfill
// did not happen.
func (w Workflow) Resolver(given map[string]string) (map[string]string, error) {
	declared := make(map[string]Param, len(w.Params))
	for _, p := range w.Params {
		declared[p.Name] = p
	}

	for name := range given {
		if _, existe := declared[name]; !existe {
			return nil, fmt.Errorf("workflow %q does not declare the param %q (declared: %s)",
				w.Slug, name, namesOf(w.Params))
		}
	}

	out := make(map[string]string, len(w.Params))
	for _, p := range w.Params {
		value, given1 := given[p.Name]
		if !given1 {
			value = p.Default
		}
		if err := p.Accepts(value); err != nil {
			return nil, fmt.Errorf("param %q: %w", p.Name, err)
		}
		out[p.Name] = value
	}
	return out, nil
}

func namesOf(ps []Param) string {
	if len(ps) == 0 {
		return "none"
	}
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}
