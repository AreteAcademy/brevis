// Package workflow is the domain model of a flow and its graph.
//
// The domain does not know what YAML is. Translating the file into these
// structs lives in internal/application/workflow -- so the file format can
// change without touching the invariants.
package workflow

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/AreteAcademy/brevis/internal/domain/runcontext"
	"github.com/AreteAcademy/brevis/internal/domain/runtimes"
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

	// Runtime and Tools say what this step runs in. Both empty is the normal
	// case: the engine infers from Run and Image, and only what the author
	// DECLARED lands here.
	//
	// They are additive in the stored document. A Workflow is written whole as
	// JSON into workflows.definicao and runs.definicao with no tags, so the Go
	// field name is the key -- an older engine ignores what it does not know,
	// and this one reading an older document gets the zero value, which means
	// "not declared" and falls through to the inference. Nothing to migrate.
	// See TestTheParamKeysAreTheOnDiskFormat for the case where that is NOT
	// true.
	Runtime string
	Tools   []string

	// UnlessEmpty names a context key that decides whether this step runs.
	//
	//	- id: transform
	//	  depends_on: [extract]
	//	  unless_empty: extract.has_rows
	//
	// A KEY, not an expression. The tempting design is
	// `when: "{{ context.extract.rows > 0 }}"`, and a mini expression language
	// is a large commitment: a parser, a type system to say what `>` means
	// across a JSON `any`, a security story because the expression comes out of
	// a YAML somebody else wrote, and error messages that point into a string.
	// Every orchestrator that has one has a bug tracker full of it.
	//
	// Here the STEP decides and publishes a boolean, so the decision lives in
	// the language its author already writes and is testable with their own
	// test framework. The engine reads one key and asks whether it is empty.
	UnlessEmpty string

	// Group draws this step inside a named, collapsible box on the graph.
	//
	//	- id: extract_orders
	//	  group: sales_data_reporting
	//
	// VISUAL only, and that is a decision rather than a shortcut. Airflow's
	// TaskGroups also PREFIX the ids inside them, so `extract` in a group
	// becomes `sales.extract` -- which would change every `depends_on`, every
	// context key and every task_runs row in an existing workflow, for a
	// feature whose whole value is that a big graph is readable. Namespacing
	// can be added later; it is a strict addition to this.
	//
	// A group is a LABEL, not a container: steps keep their global ids, a group
	// may span levels, and nothing about execution changes.
	Group string

	// ForEach maps this step over a list published by a step above it.
	//
	//	- id: load
	//	  depends_on: [extract]
	//	  for_each: extract.partitions
	//
	// The step runs once per element, each with a row, a retry and an exit code
	// of its own. The DAG's SHAPE does not change -- it is still one node with
	// one set of edges, and only the number of rows under it varies -- which is
	// what makes this cheap: graph.Levels never sees it.
	//
	// The fan-out is bounded for free. The list travels in the context, and the
	// context has a 4096-byte ceiling inherited from the kubelet, so a workflow
	// cannot ask for ten thousand pods without first finding a way to say so in
	// four kilobytes. That is a limit worth keeping rather than working around.
	ForEach string

	// Marker says this step does nothing and exists to be a point in the graph.
	//
	//	- id: start
	//	  marker: true
	//
	// It is the EmptyOperator every orchestrator ends up with, and it is not
	// decoration: an `end` that depends on everything turns "did the whole
	// thing finish?" into one node instead of six arrows to follow. Without it,
	// Validate refuses a step with neither `run` nor `action` -- correctly,
	// because that is almost always a mistake, and the two cases must not
	// collapse into one.
	Marker bool

	// When is the trigger rule: under what state of its dependencies this step
	// runs at all. Empty is WhenAllSuccess, which is what every workflow
	// published before this existed means.
	//
	//	- id: notify_failure
	//	  depends_on: [extract]
	//	  when: any_failed
	When string

	// OnError declares that this step announces its own failures.
	//
	//	on_error:
	//	  type: SLACK
	//
	// The DESTINATION is not here and never will be. A webhook is a credential
	// -- whoever holds it posts in the channel as if they were the platform --
	// and a workflow file is written by somebody who is not necessarily allowed
	// to choose where the company's alerts go. The YAML says WHETHER and HOW;
	// the installation says WHERE, through the same environment variable it
	// already uses. It is the argument that made BREVIS_POD_ALLOWED_SECRETS a
	// list the installation controls rather than something the YAML picks.
	//
	// Absent is the normal case: the run-level alert already fires when a run
	// gives up, with no block repeated in any file.
	OnError *OnError

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
// IsEmpty decides whether a published value counts as absent, for
// `unless_empty:`.
//
// The list is JavaScript's falsiness minus the surprises, and it is short on
// purpose: false, zero, an empty string, null, an empty list and an empty
// object. Everything else is present.
//
// What it deliberately does NOT do is parse strings. "false" as a STRING is a
// non-empty string and therefore present, because a step that published the
// four characters f-a-l-s-e published something, and guessing that it meant a
// boolean is how a rule starts having opinions its author cannot see. Publish a
// real boolean.
func IsEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case float64:
		return t == 0
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// validateUnlessEmpty refuses at publish what would otherwise be found at run
// time, on a step that quietly never runs again.
func validateUnlessEmpty(w Workflow, n Node) error {
	return validateContextKey(w, n, "unless_empty", n.UnlessEmpty, "extract.has_rows")
}

// validateForEach applies the same rules to the list a step maps over, plus one
// of its own: a marker cannot be mapped, because running nothing four times is
// still nothing and the four rows would say otherwise.
func validateForEach(w Workflow, n Node) error {
	if n.ForEach != "" && n.Marker {
		return fmt.Errorf("workflow %q: step %q is a `marker` and also maps over `%s`; "+
			"a marker does nothing, and doing nothing repeatedly is still nothing",
			w.Slug, n.ID, n.ForEach)
	}
	return validateContextKey(w, n, "for_each", n.ForEach, "extract.partitions")
}

// validateContextKey is the rule both `unless_empty:` and `for_each:` follow:
// the key names a step, and that step is one this one can see.
func validateContextKey(w Workflow, n Node, field, value, example string) error {
	if value == "" {
		return nil
	}
	step, _, ok := strings.Cut(value, ".")
	if !ok || step == "" {
		return fmt.Errorf("workflow %q: step %q: `%s: %s` needs the step "+
			"that publishes it (`%s`, not `%s`)",
			w.Slug, n.ID, field, value, example, value)
	}

	// The named step has to be one this one depends on, transitively -- the
	// same scope the context itself uses. A step reading a key it cannot see
	// would be skipped forever, and finding that out at publish beats finding
	// it out when a nightly stops running.
	visible := runcontext.Visible(edgesByTarget(w), n.ID)
	if !slices.Contains(visible, step) {
		if len(visible) == 0 {
			return fmt.Errorf("workflow %q: step %q reads `%s` but declares no "+
				"`depends_on`, so it can see nothing", w.Slug, n.ID, value)
		}
		return fmt.Errorf("workflow %q: step %q reads `%s`, but %q is not one of the "+
			"steps it depends on (it can see: %s)",
			w.Slug, n.ID, value, step, strings.Join(visible, ", "))
	}
	return nil
}

// edgesByTarget is the dependency map runcontext.Visible wants.
func edgesByTarget(w Workflow) map[string][]string {
	up := map[string][]string{}
	for _, e := range w.Edges {
		up[e.To] = append(up[e.To], e.From)
	}
	return up
}

// The trigger rules, as a CLOSED vocabulary validated at publish.
//
// WhenAllSuccess is the default and it is LOCAL: it asks about this step's own
// dependencies and nothing else. A failure in an unrelated branch does not stop
// this one, which is Airflow's rule and what anybody arriving from it expects.
//
// It was not always: this engine used to abort the whole graph at the first
// failure, and that had a reason -- carrying on after an error produced a
// partial result that looked complete, and a pipeline ran 28 days late without
// anyone seeing it. The protection that replaces it is that the run still
// fails, the graph shows which steps were skipped and why, and the alert still
// goes out. What is given up is that an unrelated branch now writes its data on
// a run that failed elsewhere.
const (
	WhenAllSuccess = "all_success" // the default: nothing has failed, and my dependencies succeeded
	WhenAnyFailed  = "any_failed"  // at least one of my dependencies failed
	WhenAllDone    = "all_done"    // all of my dependencies are finished, however they ended
)

// TriggerRules lists what a `when:` may say.
func TriggerRules() []string { return []string{WhenAllSuccess, WhenAnyFailed, WhenAllDone} }

// WhenOf is the rule this step actually runs under, with the default applied.
func (n Node) WhenOf() string {
	if n.When == "" {
		return WhenAllSuccess
	}
	return n.When
}

// validateWhen refuses an unknown rule at publish, naming what is valid. A rule
// nobody recognises would otherwise mean "the default" -- a step declaring
// `when: on_failure` would run on SUCCESS, which is the opposite of what it
// says, discovered the night it mattered.
func validateWhen(slug string, n Node) error {
	if n.When == "" || slices.Contains(TriggerRules(), n.When) {
		return nil
	}
	return fmt.Errorf("workflow %q: step %q: `when: %s` is not valid (valid: %s)",
		slug, n.ID, n.When, strings.Join(TriggerRules(), ", "))
}

// The alert channels a workflow may name, as a CLOSED vocabulary.
//
// It lives in the domain because it is what a YAML is allowed to declare, and
// it is validated at PUBLISH: an unknown channel is refused when the workflow
// is published, naming what is valid, rather than discovered on the night the
// alert was needed -- which is the only night it matters.
//
// Adding a name here without something that delivers to it is how a workflow
// gets to declare a destination that silently goes nowhere, so the two move
// together. internal/alerts asserts that they agree.
const (
	ChannelSlack = "SLACK"
)

// AlertChannels lists every destination a workflow may name.
func AlertChannels() []string { return []string{ChannelSlack} }

// When an on_error fires.
//
// The default is the quiet one, and the reason is whose night it is: a step
// that fails four times and passes on the fifth would send four messages under
// the other default, and the cost of that choice falls on whoever is asleep.
const (
	OnGiveUp  = "give_up" // the default: only when the run runs out of attempts
	OnAttempt = "attempt" // every failed attempt
)

// OnError is a step's declaration that it announces its failures.
type OnError struct {
	// Type is the channel. Required: an on_error with no type is a step that
	// asks to be announced somewhere unspecified.
	Type string

	// When is OnGiveUp (default) or OnAttempt.
	When string
}

// Fires reports whether this declaration wants an alert now.
func (o *OnError) Fires(gaveUp bool) bool {
	if o == nil {
		return false
	}
	return gaveUp || o.When == OnAttempt
}

// validateOnError refuses at publish what would otherwise be found at 4am.
func validateOnError(slug, step string, o *OnError) error {
	if o == nil {
		return nil
	}
	if o.Type == "" {
		return fmt.Errorf("workflow %q: step %q declares `on_error` with no `type` (valid: %s)",
			slug, step, strings.Join(AlertChannels(), ", "))
	}
	if !slices.Contains(AlertChannels(), o.Type) {
		return fmt.Errorf("workflow %q: step %q: `on_error.type: %s` is not valid (valid: %s)",
			slug, step, o.Type, strings.Join(AlertChannels(), ", "))
	}
	switch o.When {
	case "", OnGiveUp, OnAttempt:
	default:
		return fmt.Errorf("workflow %q: step %q: `on_error.when: %s` is not valid (valid: %s, %s)",
			slug, step, o.When, OnGiveUp, OnAttempt)
	}
	return nil
}

type Edge struct {
	From string
	To   string

	// Label is what this dependency MEANS, shown on the arrow.
	//
	//	depends_on:
	//	  - {step: determine_load_type, label: changed existing data}
	//
	// Empty is the normal case and draws nothing. It exists for the branch: a
	// step with two outgoing arrows and no labels is a diagram that requires
	// opening the source to read, which is the one thing a graph is for.
	//
	// Additive in the stored document, like Runtime and Tools: a Workflow is
	// written whole as JSON with no tags, so an older engine ignores the field
	// and this one reading an older document gets "".
	Label string
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
		case n.Marker && (temRun || temAction):
			// A marker that also declares work is a file saying two things. It
			// is refused rather than silently preferring one, because either
			// reading loses something somebody wrote.
			return fmt.Errorf("step %q is a `marker` and also declares `run` or `action`; "+
				"a marker does nothing", n.ID)
		case n.Marker:
			// Fine: a marker is the one step allowed to declare no work.
		case temRun && temAction:
			return fmt.Errorf("step %q declares both `run` and `action`; use one of the two", n.ID)
		case !temRun && !temAction:
			// Still refused, and deliberately: an empty `run:` is almost always
			// a mistake, and `marker: true` is how somebody says they meant it.
			return fmt.Errorf("step %q declares neither `run` nor `action` "+
				"(if it is meant to do nothing, say `marker: true`)", n.ID)
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

	for _, n := range w.Nodes {
		if err := validateToolchain(w.Slug, n); err != nil {
			return err
		}
		if err := validateOnError(w.Slug, n.ID, n.OnError); err != nil {
			return err
		}
		if err := validateWhen(w.Slug, n); err != nil {
			return err
		}
		if err := validateUnlessEmpty(w, n); err != nil {
			return err
		}
		if err := validateForEach(w, n); err != nil {
			return err
		}
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

// validateToolchain refuses a `runtime:` or a `tools:` outside the vocabulary,
// at PUBLISH time.
//
// The error lists what IS valid, because a refusal that does not say what would
// have been accepted only moves the guessing. And it refuses rather than
// dropping: an id silently discarded here renders as a missing chip on a screen
// three days later, with nothing to trace it to.
func validateToolchain(slug string, n Node) error {
	if n.Runtime != "" && !runtimes.IsRuntime(n.Runtime) {
		return fmt.Errorf("workflow %q, step %q: runtime %q is not one of: %s",
			slug, n.ID, n.Runtime, strings.Join(runtimes.Runtimes, ", "))
	}
	seen := map[string]bool{}
	for _, t := range n.Tools {
		if !runtimes.IsTool(t) {
			return fmt.Errorf("workflow %q, step %q: tool %q is not one of: %s",
				slug, n.ID, t, strings.Join(runtimes.Tools, ", "))
		}
		if seen[t] {
			return fmt.Errorf("workflow %q, step %q: tool %q is listed twice", slug, n.ID, t)
		}
		seen[t] = true
	}
	return nil
}

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
