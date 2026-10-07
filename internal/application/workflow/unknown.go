package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// UnknownFields lists what the decoder threw away, so a typo becomes a line
// somebody reads instead of a field that quietly does nothing.
//
// IT WARNS AND DOES NOT REFUSE, which is the whole design. This decoder has
// never been strict, so any workflow anybody has may carry a stray key today;
// turning that into an error would fail files that have worked for a year in
// order to catch a typo in one of them. The engine behaves exactly as it did.
//
// It is the near half of the gap `host:` exposed. An engine that does not know
// a field DROPS it -- so a step meant for the machine with the licence on it
// runs in the engine's own container instead, with nothing said. Nothing here
// fixes that for an OLD engine, and nothing can; it is why the silence was
// worth breaking on this one.
//
// Separate from Parse rather than inside it: Parse's signature is what three
// call sites and the whole suite agree on, and a warning is not an error.
func UnknownFields(path string, content []byte) []string {
	var s Spec
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)

	var te *yaml.TypeError
	if err := dec.Decode(&s); !errors.As(err, &te) {
		return nil
	}

	var out []string
	for _, e := range te.Errors {
		m := ignored.FindStringSubmatch(e)
		if m == nil {
			// A type error -- `concurrency: not-a-number`. Parse already
			// refuses it with a message about the value, and a warning beside
			// that would be a second line about the same thing, phrased as
			// though something had been ignored when nothing was.
			continue
		}
		out = append(out, fmt.Sprintf("%s: line %s: `%s` is not a field of %s, and was ignored",
			path, m[1], m[2], levelOf(m[3])))
	}
	return out
}

// The decoder's own wording, which is stable and has to be matched rather than
// parsed: anything that does not look like this is not an ignored field.
var ignored = regexp.MustCompile(`^line (\d+): field (\S+) not found in type (\S+)$`)

// levelOf turns the Go type the decoder names into the thing the author wrote.
//
// `workflow.StepSpec` is true and is not the reader's business -- somebody
// editing YAML has no way to act on a type name, and a message they cannot act
// on is a message they learn to skip.
func levelOf(goType string) string {
	switch goType {
	case "workflow.Spec":
		return "a workflow"
	case "workflow.StepSpec":
		return "a step"
	case "workflow.ParamSpec":
		return "a param"
	case "workflow.ResourceSpec":
		return "`resources:`"
	case "workflow.OnErrorSpec":
		return "`on_error:`"
	case "workflow.Dependency":
		return "a dependency"
	}
	// A type added later and not added here. Vague and true beats precise and
	// about Go, and the test calls this branch directly so it is not a promise
	// nobody checks.
	return "this file"
}
