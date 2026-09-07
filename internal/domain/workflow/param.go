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
)

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
// interprets: quotes, `;`, `|`, `&`, `$`, backticks,
// parenteses e redirecionamentos.
var safeCharacters = regexp.MustCompile(`^[A-Za-z0-9_.:/=,+@\- ]*$`)

var paramName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Validate checks the param's declaration, not the value.
func (p Param) Validate() error {
	if !paramName.MatchString(p.Name) {
		return fmt.Errorf("param %q: the name has to be lowercase, start with a letter and hold only letters, digits and _", p.Name)
	}
	switch p.Type {
	case ParamString, ParamBool, ParamInteger:
	case "":
		return fmt.Errorf("param %q has no type (string, boolean or integer)", p.Name)
	default:
		return fmt.Errorf("param %q: tipo %q desconhecido", p.Name, p.Type)
	}
	if p.Pattern != "" {
		if _, err := regexp.Compile(p.Pattern); err != nil {
			return fmt.Errorf("param %q: the pattern is not valid: %w", p.Name, err)
		}
	}
	// The default has to be valid by its own rules: a refused default would only
	// apareceria no primeiro disparo agendado, de madrugada.
	if p.Default != "" {
		if err := p.Accepts(p.Default); err != nil {
			return fmt.Errorf("param %q: the default value is not valid: %w", p.Name, err)
		}
	}
	return nil
}

// Accepts validates a VALUE against the declaration.
func (p Param) Accepts(value string) error {
	switch p.Type {
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

	for nome := range given {
		if _, existe := declared[nome]; !existe {
			return nil, fmt.Errorf("workflow %q does not declare the param %q (declared: %s)",
				w.Slug, nome, namesOf(w.Params))
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
		return "nenhum"
	}
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}
