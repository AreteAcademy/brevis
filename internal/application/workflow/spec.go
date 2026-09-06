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
	tipo := dominio.TipoParam(strings.TrimSpace(p.Type))
	if tipo == "" {
		// `string` as the default: it is the most common type and the only one
		// that does not change the value's meaning. Requiring the key on every
		// param would be noise.
		tipo = dominio.ParamTexto
	}
	return dominio.Param{
		Nome: strings.TrimSpace(p.Name), Tipo: tipo,
		Padrao: p.Default, Descricao: p.Description,
		Enum: p.Enum, Pattern: p.Pattern,
	}
}

// StepSpec is a step as written in the file.
type StepSpec struct {
	ID        string         `yaml:"id"`
	Run       string         `yaml:"run"`
	Action    string         `yaml:"action"`
	With      map[string]any `yaml:"with"`
	DependsOn []string       `yaml:"depends_on"`

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

	// Shell: a pointer, to tell "did not declare" from "declared false". Without
	// the pointer, every step without the key would become `shell: false` and
	// images that do have a shell -- most of them -- would start receiving an
	// argv, breaking any command with a pipe or a variable.
	Shell *bool `yaml:"shell"`
}

// Parse reads the YAML and returns the workflow already validated.
//
// `caminho` serves two purposes: deriving the slug when the file carries no
// `name`, and naming the file in error messages -- a graph error without the
// file's name is useless when there are dozens of them.
func Parse(caminho string, conteudo []byte) (dominio.Workflow, error) {
	var s Spec
	if err := yaml.Unmarshal(conteudo, &s); err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: invalid yaml: %w", caminho, err)
	}

	slug := s.Name
	if slug == "" {
		slug = strings.TrimSuffix(filepath.Base(caminho), filepath.Ext(caminho))
	}

	kind, err := parseKind(s.Type)
	if err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: %w", caminho, err)
	}

	w := dominio.Workflow{
		Slug:     slug,
		Name:     slug,
		Kind:     kind,
		Schedule: strings.TrimSpace(s.Schedule),
		Tags:     normalizarTags(s.Tags),

		// The workflow's image is the steps' default runtime: in Kubernetes each
		// step becomes a pod, and it is the image that decides what that pod
		// knows how to do.
		Image:     strings.TrimSpace(s.Image),
		Resources: s.Resources.dominio(),
		MaxAtivos: s.Concurrency,
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
		})
	}

	w.Edges, err = arestas(kind, s.Steps)
	if err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: %w", caminho, err)
	}

	if err := w.Validate(); err != nil {
		return dominio.Workflow{}, fmt.Errorf("%s: %w", caminho, err)
	}
	return w, nil
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
func normalizarTags(brutas []string) []string {
	if len(brutas) == 0 {
		return nil
	}
	vistas := make(map[string]struct{}, len(brutas))
	out := make([]string, 0, len(brutas))
	for _, t := range brutas {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ja := vistas[t]; ja {
			continue
		}
		vistas[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// arestas turns the declaration into a graph.
//
// `chain` is sugar: every step depends on the previous one. Converting here, the
// execution engine knows only DAGs — one more format in the file, zero extra
// paths at runtime.
func arestas(kind dominio.Kind, steps []StepSpec) ([]dominio.Edge, error) {
	var out []dominio.Edge

	switch kind {
	case dominio.KindChain:
		for _, st := range steps {
			if len(st.DependsOn) > 0 {
				return nil, fmt.Errorf("step %q usa `depends_on` num workflow `chain`; "+
					"em chain a ordem e a do arquivo — use `type: dag` para declarar dependencias", st.ID)
			}
		}
		for i := 1; i < len(steps); i++ {
			out = append(out, dominio.Edge{From: steps[i-1].ID, To: steps[i].ID})
		}

	case dominio.KindDAG:
		for _, st := range steps {
			for _, dep := range st.DependsOn {
				out = append(out, dominio.Edge{From: dep, To: st.ID})
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
		return "", fmt.Errorf("type %q desconhecido (use `chain` ou `dag`)", t)
	}
}
