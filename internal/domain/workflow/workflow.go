// Package workflow is the domain model of a flow and its graph.
//
// The domain does not know what YAML is. Translating the file into these
// structs lives in internal/application/workflow -- so the file format can
// change without touching the invariants.
package workflow

import (
	"fmt"
	"regexp"
	"strings"
)

// Kind distinguishes how the graph was declared. `chain` is syntactic sugar:
// the parser turns it into edges before it gets here, so the execution engine
// only ever knows a DAG. One engine, two ways of writing.
type Kind string

const (
	KindChain Kind = "chain"
	KindDAG   Kind = "dag"
)

// Workflow is a flow's definition. Immutable once published: §22 of the plan
// requires a Run to keep a snapshot of the version that produced it.
type Workflow struct {
	Slug     string
	Name     string
	Kind     Kind
	Schedule string // cron; vazio = so disparo manual

	// Tags classify the workflow for search and filtering in the UI. They are
	// free-form labels from the YAML's author, not domain: nothing in the
	// engine depends on them.
	Tags []string

	// Image is the steps' default runtime. In Kubernetes EVERY step becomes a
	// pod, and the image is what defines what that pod knows how to do: a dbt
	// step brings up the dbt image, a Go binary brings up a 10 MB one.
	// Declaring it here avoids repeating the same line in ten steps; a step
	// overrides it when it needs a different runtime.
	Image string

	// Resources is the default CPU and memory request, for the same reason.
	Resources Resources

	// Env are environment variables every step receives. A literal value, which
	// is why they are NOT for secrets: the YAML is in git.
	Env map[string]string

	// Secrets are variables whose value the engine neither sees nor stores. See Node.
	Secrets map[string]string

	// Params are the values that change between two dispatches of the same
	// workflow -- `load_full`, a date window, a limit. See param.go.
	Params []Param

	// MaxActive caps simultaneous runs OF THIS workflow. Zero means no limit.
	//
	// It differs from the global step ceiling: that one protects the CLUSTER,
	// this one protects the DATA. A `*/15` that takes 20 minutes overlaps
	// itself, and two `dbt build` on the same model at once fight over the same
	// table.
	MaxActive int

	Nodes []Node
	Edges []Edge
}

// Node is a unit of work. Exactly one form of execution must be filled in --
// `Run` (a command) or `Action` (a typed action with parameters).
type Node struct {
	ID     string
	Run    string
	Action string
	With   map[string]any

	// Image overrides the workflow's. Empty inherits.
	Image string

	// Resources sizes this step's pod. The gain from separating per step is
	// concrete: a Go fetcher fits in 64Mi while the dbt next to it asks for
	// 1Gi, and under a single image both would pay the larger of the two.
	Resources Resources

	// Shell decides how the command enters the container. Nil means with a
	// shell, which is what `run:` suggests ("python fetch.py"). False passes the
	// argv directly, for a distroless image -- where `sh -c` would fail with
	// "no such file or directory", an error that says nothing about the
	// cause.
	Shell *bool

	// Env are this step's environment variables, with a literal value in the
	// file. They override the workflow's, name by name.
	//
	//	env:
	//	  BREVIS_LOG_LEVEL: info
	Env map[string]string

	// Secrets are variables whose VALUE never appears in the file. The key is
	// the variable's name; the value is where to find it, as `secret/key`.
	//
	//	secrets:
	//	  GABRIEL_SESSION_COOKIE: gabriel-session/cookie
	//
	// Two keys and not one, on purpose. With a single one, the shortest path to
	// making it work would be pasting the secret into the YAML -- and the YAML
	// is in git. `env:` accepts a literal, `secrets:` does not.
	//
	// Where the coordinate resolves depends on the executor, and the asymmetry
	// is deliberate:
	//
	//   Kubernetes  valueFrom.secretKeyRef{name: gabriel-session, key: cookie}
	//   local       the variable of the same name in the engine's own
	//               environment, and missing is an ERROR -- not an empty string
	//
	// In either case the engine passes it on without reading: the value enters
	// no log, no database and no rendered command.
	Secrets map[string]string
}

// Resources are a pod's requests and limits, in Kubernetes' own format
// ("200m", "1Gi"). Text and not a number on purpose: the format belongs to
// Kubernetes, and converting to a unit of our own would only create a second
// vocabulary for the same thing.
type Resources struct {
	CPU         string
	Memory      string
	CPULimit    string
	MemoryLimit string
}

// Empty says whether nothing was declared -- the pod then starts without
// `resources`, inheriting the namespace's LimitRange.
func (r Resources) Empty() bool {
	return r.CPU == "" && r.Memory == "" && r.CPULimit == "" && r.MemoryLimit == ""
}

// WithDefaults fills what the step did not declare from the workflow's. Inheriting
// field by field, rather than the whole block, lets a step ask for more memory
// alone without losing the default CPU.
func (r Resources) WithDefaults(p Resources) Resources {
	if r.CPU == "" {
		r.CPU = p.CPU
	}
	if r.Memory == "" {
		r.Memory = p.Memory
	}
	if r.CPULimit == "" {
		r.CPULimit = p.CPULimit
	}
	if r.MemoryLimit == "" {
		r.MemoryLimit = p.MemoryLimit
	}
	return r
}

// ImageFor resolves a step's effective image.
func (w Workflow) ImageFor(n Node) string {
	if n.Image != "" {
		return n.Image
	}
	return w.Image
}

// ResourcesFor resolves a step's effective resources.
func (w Workflow) ResourcesFor(n Node) Resources {
	return n.Resources.WithDefaults(w.Resources)
}

// EnvDe resolves a step's effective literal variables: the workflow's, with
// the step's on top.
func (w Workflow) EnvDe(n Node) map[string]string { return sobrepor(w.Env, n.Env) }

// SecretsDe resolves a step's effective secrets, by the same rule.
func (w Workflow) SecretsDe(n Node) map[string]string { return sobrepor(w.Secrets, n.Secrets) }

// sobrepor returns base with cima on top, mutating neither: the maps come from
// the published workflow and are read by every step at the same time.
func sobrepor(base, up map[string]string) map[string]string {
	if len(base) == 0 && len(up) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(up))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range up {
		out[k] = v
	}
	return out
}

// UsaShell says whether the command enters through `sh -c`.
func (n Node) UsaShell() bool { return n.Shell == nil || *n.Shell }

// Edge links two nodes: From runs before To.
type Edge struct {
	From string
	To   string
}

// Validate applies the invariants §5 of the plan requires before saving.
// Order matters: duplicate IDs and missing dependencies are checked before the
// cycle, because a graph with a dangling edge cannot be walked.
func (w Workflow) Validate() error {
	if w.Slug == "" {
		return fmt.Errorf("workflow with no slug")
	}
	if len(w.Nodes) == 0 {
		return fmt.Errorf("workflow %q has no steps at all", w.Slug)
	}

	vistos := make(map[string]struct{}, len(w.Nodes))
	for _, n := range w.Nodes {
		if n.ID == "" {
			return fmt.Errorf("workflow %q has a step with no id", w.Slug)
		}
		if _, dup := vistos[n.ID]; dup {
			return fmt.Errorf("workflow %q: duplicate step id: %q", w.Slug, n.ID)
		}
		vistos[n.ID] = struct{}{}

		temRun, temAction := n.Run != "", n.Action != ""
		switch {
		case temRun && temAction:
			return fmt.Errorf("step %q declares both `run` and `action`; use one of the two", n.ID)
		case !temRun && !temAction:
			return fmt.Errorf("step %q declares neither `run` nor `action`", n.ID)
		case temRun && len(n.With) > 0:
			return fmt.Errorf("step %q uses `with`, which only applies with `action`", n.ID)
		}
	}

	if w.MaxActive < 0 {
		return fmt.Errorf("workflow %q: negative concurrency (%d); use 0 for no limit", w.Slug, w.MaxActive)
	}
	if err := validateResources(w.Slug, "workflow", w.Resources); err != nil {
		return err
	}
	for _, n := range w.Nodes {
		if err := validateResources(w.Slug, "step "+n.ID, n.Resources); err != nil {
			return err
		}
	}

	for _, e := range w.Edges {
		if _, ok := vistos[e.From]; !ok {
			return fmt.Errorf("workflow %q: dependency %q does not exist", w.Slug, e.From)
		}
		if _, ok := vistos[e.To]; !ok {
			return fmt.Errorf("workflow %q: dependency %q does not exist", w.Slug, e.To)
		}
		if e.From == e.To {
			return fmt.Errorf("step %q depends on itself", e.From)
		}
	}

	seenParams := make(map[string]struct{}, len(w.Params))
	for _, p := range w.Params {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("workflow %q: %w", w.Slug, err)
		}
		if _, ja := seenParams[p.Name]; ja {
			return fmt.Errorf("workflow %q: duplicate param: %q", w.Slug, p.Name)
		}
		seenParams[p.Name] = struct{}{}
	}

	if err := validateEnvironment(w.Slug, "workflow", w.Env, w.Secrets); err != nil {
		return err
	}
	for _, n := range w.Nodes {
		// Against the effective view, not the declared one: an `env:` on the
		// workflow and a `secrets:` of the same name on the step collide just
		// the same, and only the inherited view sees it.
		if err := validateEnvironment(w.Slug, "step "+n.ID, w.EnvDe(n), w.SecretsDe(n)); err != nil {
			return err
		}
	}

	if cycle := w.findCycle(); cycle != "" {
		return fmt.Errorf("workflow %q has a cycle: %s", w.Slug, cycle)
	}
	return nil
}

// validateEnvironment refuses what would silently become the wrong variable.
//
// The name comes first because an invalid environment variable name is accepted
// by the YAML and refused by the Kubernetes server much later, with a message
// about a container field rather than a file line.
func validateEnvironment(slug, where string, env, secrets map[string]string) error {
	for name := range env {
		if err := validateVarName(name); err != nil {
			return fmt.Errorf("workflow %q, %s: env: %w", slug, where, err)
		}
	}

	for name, coord := range secrets {
		if err := validateVarName(name); err != nil {
			return fmt.Errorf("workflow %q, %s: secrets: %w", slug, where, err)
		}

		// A variable defined in both places is ambiguous, and any tie-break
		// chosen here would be a rule nobody remembers.
		if _, colide := env[name]; colide {
			return fmt.Errorf("workflow %q, %s: %q is in both `env` and `secrets`; "+
				"the same variable cannot have a literal value and come from a secret",
				slug, where, name)
		}

		// The value does NOT go into the message. The most likely cause of an
		// invalid coordinate is somebody having pasted the real secret -- and
		// `brevis validate` runs in CI, whose log plenty of people read. An
		// error that teaches the format does not need to repeat what it got.
		secret, key, ok := strings.Cut(coord, "/")
		if !ok || secret == "" || key == "" || strings.Contains(key, "/") {
			return fmt.Errorf("workflow %q, %s: secrets[%q] is not a coordinate "+
				"(got %d characters). Use `secret-name/key`, as in "+
				"`gabriel-session/cookie`. If the value pasted there is the secret "+
				"itself, it is already in git: change the key and rotate the secret",
				slug, where, name, len(coord))
		}
	}
	return nil
}

// validateVarName accepts what a POSIX shell accepts: letters, digits and
// underscore, not starting with a digit.
func validateVarName(name string) error {
	if name == "" {
		return fmt.Errorf("empty variable name")
	}
	if name[0] >= '0' && name[0] <= '9' {
		return fmt.Errorf("variable name %q starts with a digit", name)
	}
	for _, r := range name {
		ok := r == '_' ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("variable name %q has an invalid character %q; "+
				"use letters, digits and underscores", name, r)
		}
	}
	return nil
}

// findCycle devolve o caminho do ciclo, ou vazio se o grafo for aciclico.
//
// It returns the PATH, not just a boolean: whoever wrote the DAG needs to know
// which steps close the loop in order to fix it.
func (w Workflow) findCycle() string {
	outgoing := make(map[string][]string, len(w.Nodes))
	for _, e := range w.Edges {
		outgoing[e.From] = append(outgoing[e.From], e.To)
	}

	const (
		novo = iota // without iota, emUso and pronto would repeat novo's value
		emUso
		pronto
	)
	state := make(map[string]int, len(w.Nodes))
	var path []string
	var achado string

	var visitar func(string) bool
	visitar = func(id string) bool {
		state[id] = emUso
		path = append(path, id)

		for _, prox := range outgoing[id] {
			switch state[prox] {
			case emUso:
				// closes the loop: it slices the path from where `prox` went in
				for i, v := range path {
					if v == prox {
						achado = formatCycle(append(append([]string{}, path[i:]...), prox))
						return true
					}
				}
			case novo:
				if visitar(prox) {
					return true
				}
			}
		}

		path = path[:len(path)-1]
		state[id] = pronto
		return false
	}

	for _, n := range w.Nodes {
		if state[n.ID] == novo && visitar(n.ID) {
			return achado
		}
	}
	return ""
}

func formatCycle(ids []string) string {
	s := ""
	for i, id := range ids {
		if i > 0 {
			s += " -> "
		}
		s += id
	}
	return s
}

// quantidade is Kubernetes's format: an integer or a decimal with an optional
// suffix (m for CPU; Ki/Mi/Gi/K/M/G for memory).
var quantity = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(m|[KMGTPE]i?)?$`)

// validateResources refuses a malformed quantity at PUBLISH time.
//
// Without this the error only shows up when the pod is created -- hours later,
// in the middle of the night, as a 422 from the API server that names neither
// the file nor the step.
func validateResources(slug, where string, r Resources) error {
	for field, value := range map[string]string{
		"cpu": r.CPU, "memory": r.Memory,
		"cpu_limit": r.CPULimit, "memory_limit": r.MemoryLimit,
	} {
		if value == "" {
			continue
		}
		if !quantity.MatchString(value) {
			return fmt.Errorf("workflow %q, %s: %s=%q is not a valid quantity (e.g. 200m, 1, 512Mi, 2Gi)",
				slug, where, field, value)
		}
	}
	return nil
}
