package workflow

import (
	"bytes"

	"gopkg.in/yaml.v3"

	dominio "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// SpecOf is a published workflow written back as the file: the inverse of
// Parse, for the console's Definition tab. The database keeps the domain and
// not the file, so this is not the file the author wrote -- no comments, keys
// in the format's order -- but publishing it publishes the same workflow,
// which TestEveryExampleRoundTripsThroughItsYAML holds.
func SpecOf(w dominio.Workflow) Spec {
	s := Spec{
		Name: w.Slug, Description: w.Description, Schedule: w.Schedule,
		Tags: w.Tags, Image: w.Image, Resources: resourceSpecOf(w.Resources),
		Env: w.Env, Secrets: w.Secrets,
		Concurrency: ConcurrencySpec{Limit: w.MaxActive, Group: w.Group},
		Trigger:     TriggerSpec{OnLanded: w.Trigger.OnLanded, Debounce: w.Trigger.Debounce},
	}
	if w.Kind == dominio.KindChain {
		s.Type = string(dominio.KindChain)
	}
	for _, p := range w.Params {
		s.Params = append(s.Params, ParamSpec{
			Name: p.Name, Type: string(p.Type), Default: p.Default, Description: p.Description,
			Enum: p.Enum, Pattern: p.Pattern, Title: p.Title, Group: p.Group, Warn: p.Warn, Uses: p.Uses,
		})
	}
	for _, n := range w.Nodes {
		st := StepSpec{
			ID: n.ID, Run: n.Run, Action: n.Action, With: n.With,
			Image: n.Image, Host: n.Host, Resources: resourceSpecOf(n.Resources),
			Env: n.Env, PersistContext: n.PersistContext, Secrets: n.Secrets,
			Runtime: n.Runtime, Tools: n.Tools, Shell: n.Shell, When: n.When, Marker: n.Marker,
			UnlessEmpty: n.UnlessEmpty, ForEach: n.ForEach, Group: n.Group, Uses: n.Uses,
		}
		if n.OnError != nil {
			st.OnError = &OnErrorSpec{Type: n.OnError.Type, When: n.OnError.When}
		}
		// A chain's edges are its order; a DAG's are each step's depends_on.
		if w.Kind != dominio.KindChain {
			for _, e := range w.Edges {
				if e.To == n.ID {
					st.DependsOn = append(st.DependsOn, Dependency{Step: e.From, Label: e.Label})
				}
			}
		}
		s.Steps = append(s.Steps, st)
	}
	return s
}

func resourceSpecOf(r dominio.Resources) ResourceSpec {
	var s ResourceSpec
	s.CPU, s.Memory = r.CPU, r.Memory
	s.Limits.CPU, s.Limits.Memory = r.CPULimit, r.MemoryLimit
	return s
}

// YAML is a published workflow as the file that publishes it, indented as
// the examples are.
func YAML(w dominio.Workflow) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(SpecOf(w)); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// MarshalYAML writes the short form when it says everything: `concurrency:
// 3` for a limit alone, the mapping when there is a group.
func (c ConcurrencySpec) MarshalYAML() (any, error) {
	if c.Group == "" {
		return c.Limit, nil
	}
	return struct {
		Limit int    `yaml:"limit"`
		Group string `yaml:"group"`
	}{c.Limit, c.Group}, nil
}

// MarshalYAML writes a dependency with no label as its step's name.
func (d Dependency) MarshalYAML() (any, error) {
	if d.Label == "" {
		return d.Step, nil
	}
	type plain Dependency
	return plain(d), nil
}

// MarshalYAML writes the debounce in its unit: yaml.v3 would write the
// nanoseconds, which the reader refuses (TestADebounceNeedsItsUnit).
func (t TriggerSpec) MarshalYAML() (any, error) {
	out := struct {
		OnLanded []string `yaml:"on_landed,omitempty"`
		Debounce string   `yaml:"debounce,omitempty"`
	}{OnLanded: t.OnLanded}
	if t.Debounce > 0 {
		out.Debounce = t.Debounce.String()
	}
	return out, nil
}
