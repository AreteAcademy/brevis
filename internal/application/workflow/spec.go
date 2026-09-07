// Package workflow (application) translates the YAML file into the domain.
//
// The separation exists because §22 of the plan is explicit: once published, the
// database is the source of truth, not the file. The YAML is publishing INPUT --
// it comes in here, becomes domain, and the domain is what persists. Changing the
// file format must not touch the graph's invariants.
package workflow

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	dominio "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// Spec mirrors the YAML, and nothing more. Loose fields here, invariants in the domain.
type Spec struct {
	Name      string       `yaml:"name"`
	Schedule  string       `yaml:"schedule"`
	Type      string       `yaml:"type"`
	Tags      []string     `yaml:"tags"`
	Image     string       `yaml:"image"`
	Resources ResourceSpec `yaml:"resources"`
	Params    []ParamSpec  `yaml:"params"`

	// Env and Secrets apply to every step; a step overrides them name by name.
	Env     map[string]string `yaml:"env"`
	Secrets map[string]string `yaml:"secrets"`
	// Concurrency is Kestra's `concurrency.limit`, under the name most
	// orchestrators use.
	Concurrency int        `yaml:"concurrency"`
	Steps       []StepSpec `yaml:"steps"`
}

// ResourceSpec is the CPU and memory request in Kubernetes' format.
//
// `limits` kept apart from `requests` because the difference between them is the
// difference between "how much I reserve" and "when I get killed": a dbt that
// blows the limit dies with OOMKilled, one that merely exceeds the request keeps
// running.
type ResourceSpec struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
	Limits struct {
		CPU    string `yaml:"cpu"`
		Memory string `yaml:"memory"`
	} `yaml:"limits"`
}

func (r ResourceSpec) dominio() dominio.Resources {
	return dominio.Resources{
		CPU: r.CPU, Memory: r.Memory,
		CPULimit: r.Limits.CPU, MemoryLimit: r.Limits.Memory,
	}
}

// ParamSpec is a run parameter as written in the file.
//
//	params:
//	  - name: load_full
//	    type: boolean
//	    default: "false"
//	  - name: start_date
//	    type: string
//	    pattern: '^\d{4}-\d{2}-\d{2}$'
type ParamSpec struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`
	Default     string   `yaml:"default"`
	Description string   `yaml:"description"`
	Enum        []string `yaml:"enum"`
	Pattern     string   `yaml:"pattern"`
}

func (p ParamSpec) dominio() dominio.Param {
	kind := dominio.ParamType(strings.TrimSpace(p.Type))
	if kind == "" {
		// `string` as the default: it is the most common type and the only one
		// that does not change the value's meaning. Requiring the key on every
		// param would be noise.
		kind = dominio.ParamString
	}
	return dominio.Param{
		Name: strings.TrimSpace(p.Name), Type: kind,
		Default: p.Default, Description: p.Description,
		Enum: p.Enum, Pattern: p.Pattern,
	}
}

// StepSpec is a step as written in the file.
type StepSpec struct {
	ID        string         `yaml:"id"`
	Run       string         `yaml:"run"`
	Action    string         `yaml:"action"`
	With      map[string]any `yaml:"with"`
	DependsOn []Dependency   `yaml:"depends_on"`

	// Image e Resources sobrescrevem os do workflow. Ausentes = herda.
	Image     string       `yaml:"image"`
	Resources ResourceSpec `yaml:"resources"`

	// Env are variables with a literal value in the file.
	//
	//	env:
	//	  BREVIS_LOG_LEVEL: info
	Env map[string]string `yaml:"env"`

	// Secrets are variables whose value is NOT in the file: the key is the
	// variable's name, the value is where to find it.
	//
	//	secrets:
	//	  GABRIEL_SESSION_COOKIE: gabriel-session/cookie
	Secrets map[string]string `yaml:"secrets"`

	// Runtime and Tools say what this step runs in, when the engine cannot
	// work it out from `run:` and `image:`.
	//
	//	runtime: python
	//	tools: [dbt]
	//
	// Both optional, and the engine INFERS both when they are absent -- which
	// is the normal case. Declaring one is for when the inference is wrong or
	// blind: a wrapper script, a bare binary path, an image whose name says
	// nothing.
	//
	// An id outside the vocabulary is refused at publish, naming what is
	// valid. The alternative is a chip that renders blank on a screen three
	// days later, with nothing to trace it to.
	Runtime string   `yaml:"runtime"`
	Tools   []string `yaml:"tools"`

	// Shell: a pointer, to tell "did not declare" from "declared false". Without
	// the pointer, every step without the key would become `shell: false` and
	// images that do have a shell -- most of them -- would start receiving an
	// argv, breaking any command with a pipe or a variable.
	Shell *bool `yaml:"shell"`

	// When is the trigger rule. Empty is `all_success`, which is what every
	// workflow written before this existed means.
	When string `yaml:"when"`

	// Marker is a step that does nothing and exists to be a point in the
	// graph -- a `start`, an `end`, a join. See dominio.Node.Marker.
	Marker bool `yaml:"marker"`

	// UnlessEmpty names a context key that decides whether this step runs.
	// A key, not an expression. See dominio.Node.UnlessEmpty.
	UnlessEmpty string `yaml:"unless_empty"`

	// ForEach names a context key holding a list. The step runs once per
	// element. See dominio.Node.ForEach.
	ForEach string `yaml:"for_each"`

	// Group draws this step inside a named box on the graph. Visual only.
	// See dominio.Node.Group.
	Group string `yaml:"group"`

	// OnError announces this step's failures.
	//
	//	on_error:
	//	  type: SLACK
	//	  when: attempt     # optional; the default is give_up
	//
	// A pointer for the same reason Shell is: absent has to be different from
	// declared-and-empty, and a zero OnError would look like a step asking to
	// be announced to nowhere.
	OnError *OnErrorSpec `yaml:"on_error"`
}

// Dependency is one entry of `depends_on`. It accepts both shapes:
//
//	depends_on: [extract]
//	depends_on:
//	  - {step: determine_load_type, label: changed existing data}
//
// The bare form stays the normal one -- most dependencies have nothing to say
// and a label on every arrow is noise. The object exists for the branch, where
// two arrows leaving the same step with no labels is a diagram that requires
// opening the source to read.
type Dependency struct {
	Step  string `yaml:"step"`
	Label string `yaml:"label"`
}

// UnmarshalYAML accepts a scalar or a mapping.
//
// The scalar branch is what keeps every workflow ever written working: a list
// of strings decodes exactly as it did, and nothing in a published file has to
// change.
func (d *Dependency) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		return value.Decode(&d.Step)
	}
	// A named type without the method, or this recurses forever.
	type plain Dependency
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	*d = Dependency(p)
	if d.Step == "" {
		return fmt.Errorf("a `depends_on` entry has a label but no `step`")
	}
	return nil
}

// OnErrorSpec is a step's alert declaration as written in the file.
//
// There is no `webhook`, `url` or `channel` field, and there will not be. The
// destination is a credential; the installation owns it. This says WHETHER and
// HOW.
type OnErrorSpec struct {
	Type string `yaml:"type"`
	When string `yaml:"when"`
}

func (o *OnErrorSpec) dominio() *dominio.OnError {
	if o == nil {
		return nil
	}
	// The type is upper-cased and the moment lower-cased, matching how each is
	// written in the vocabulary. `type: slack` in a file is a typo, not a
	// different channel, and refusing it would be pedantry with a 4am cost.
	return &dominio.OnError{
		Type: strings.ToUpper(strings.TrimSpace(o.Type)),
		When: strings.ToLower(strings.TrimSpace(o.When)),
	}
}

// Parse reads the YAML and returns the workflow already validated.
//
// `caminho` serves two purposes: deriving the slug when the file carries no
// `name`, and naming the file in error messages -- a graph error without the
// file's name is useless when there are dozens of them.
func Parse(path string, conteudo []byte) (dominio.Workflow, error) {
	var s Spec
	if err := yaml.Unmarshal(conteudo, &s); err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: invalid yaml: %w", path, err)
	}

	slug := s.Name
	if slug == "" {
		slug = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	kind, err := parseKind(s.Type)
	if err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: %w", path, err)
	}

	w := dominio.Workflow{
		Slug:     slug,
		Name:     slug,
		Kind:     kind,
		Schedule: strings.TrimSpace(s.Schedule),
		Tags:     normalizeTags(s.Tags),

		// The workflow's image is the steps' default runtime: in Kubernetes each
		// step becomes a pod, and it is the image that decides what that pod
		// knows how to do.
		Image:     strings.TrimSpace(s.Image),
		Resources: s.Resources.dominio(),
		MaxActive: s.Concurrency,
		Env:       aparar(s.Env),
		Secrets:   aparar(s.Secrets),
	}
	for _, ps := range s.Params {
		w.Params = append(w.Params, ps.dominio())
	}
	for _, st := range s.Steps {
		w.Nodes = append(w.Nodes, dominio.Node{
			ID: st.ID, Run: st.Run, Action: st.Action, With: st.With,
			Image: strings.TrimSpace(st.Image), Resources: st.Resources.dominio(),
			Shell: st.Shell,
			Env:   aparar(st.Env), Secrets: aparar(st.Secrets),
			Runtime:     strings.ToLower(strings.TrimSpace(st.Runtime)),
			Tools:       normalizeTools(st.Tools),
			When:        strings.ToLower(strings.TrimSpace(st.When)),
			Marker:      st.Marker,
			UnlessEmpty: strings.TrimSpace(st.UnlessEmpty),
			ForEach:     strings.TrimSpace(st.ForEach),
			Group:       strings.TrimSpace(st.Group),
			OnError:     st.OnError.dominio(),
		})
	}

	w.Edges, err = edges(kind, s.Steps)
	if err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: %w", path, err)
	}

	if err := w.Validate(); err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: %w", path, err)
	}
	return w, nil
}

// normalizeTools lowercases and trims, and drops the empties.
//
// It does NOT validate: an unknown id has to be refused by Validate, with the
// workflow's slug and the list of what is valid, and not silently dropped here
// where the message would have neither.
func normalizeTools(in []string) []string {
	var out []string
	for _, t := range in {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// aparar trims the name and the value, and discards an entry with an empty
// name.
//
// `GABRIEL_SESSION_COOKIE : gabriel-session/cookie` with a space before the
// colon is valid YAML, and the space would travel inside the variable's name --
// the pod starts, the binary does not find the variable, and nothing along the
// way says why.
func aparar(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizarTags trims spaces, drops empty ones and deduplicates while keeping
// the file's order. Without it, `tags: [dbt, dbt , ""]` would become three chips
// on the screen, two of them identical and one blank.
func normalizeTags(brutas []string) []string {
	if len(brutas) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(brutas))
	out := make([]string, 0, len(brutas))
	for _, t := range brutas {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ja := seen[t]; ja {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// arestas turns the declaration into a graph.
//
// `chain` is sugar: every step depends on the previous one. Converting here, the
// execution engine knows only DAGs — one more format in the file, zero extra
// paths at runtime.
func edges(kind dominio.Kind, steps []StepSpec) ([]dominio.Edge, error) {
	var out []dominio.Edge

	switch kind {
	case dominio.KindChain:
		for _, st := range steps {
			if len(st.DependsOn) > 0 {
				return nil, fmt.Errorf("step %q usa `depends_on` num workflow `chain`; "+
					"in a chain the order is the file's -- use `type: dag` to declare dependencies", st.ID)
			}
		}
		for i := 1; i < len(steps); i++ {
			out = append(out, dominio.Edge{From: steps[i-1].ID, To: steps[i].ID})
		}

	case dominio.KindDAG:
		for _, st := range steps {
			for _, dep := range st.DependsOn {
				out = append(out, dominio.Edge{
					From:  strings.TrimSpace(dep.Step),
					To:    st.ID,
					Label: strings.TrimSpace(dep.Label),
				})
			}
		}
	}
	return out, nil
}

func parseKind(t string) (dominio.Kind, error) {
	switch strings.TrimSpace(t) {
	case "", string(dominio.KindDAG):
		// With no `type`, assume DAG: it is the general model, and a file with no
		// declared dependencies becomes a graph of loose nodes that run in
		// parallel. `chain` has to be asked for, because it imposes order.
		return dominio.KindDAG, nil
	case string(dominio.KindChain):
		return dominio.KindChain, nil
	default:
		return "", fmt.Errorf("unknown type %q (use `chain` or `dag`)", t)
	}
}
