package kubernetes

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution"
)

// nomeContainer is fixed: the pod has a single container, and a stable name
// makes `kubectl logs` predictable without reading the spec.
const nomeContainer = "step"

// Pod is the subset of the object this engine uses. Writing the structs by hand
// instead of importing client-go's keeps the dependency tree small and makes it
// visible exactly what is sent to the API server.
type Pod struct {
	APIVersion string   `json:"apiVersion,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Metadata   Metadata `json:"metadata"`
	Spec       PodSpec  `json:"spec,omitempty"`
	// A pointer because `omitempty` does not omit an empty struct: without it
	// every created pod would send `"status":{}` to the server -- harmless, but
	// noise in an object people read to debug.
	Status *PodStatus `json:"status,omitempty"`
}

type Metadata struct {
	Name        string            `json:"name,omitempty"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type PodSpec struct {
	RestartPolicy         string            `json:"restartPolicy,omitempty"`
	ServiceAccountName    string            `json:"serviceAccountName,omitempty"`
	ImagePullSecrets      []RefLocal        `json:"imagePullSecrets,omitempty"`
	NodeSelector          map[string]string `json:"nodeSelector,omitempty"`
	Tolerations           []Toleracao       `json:"tolerations,omitempty"`
	ActiveDeadlineSeconds *int64            `json:"activeDeadlineSeconds,omitempty"`
	Volumes               []Volume          `json:"volumes,omitempty"`
	Containers            []Container       `json:"containers"`
}

// Volume is a PersistentVolumeClaim mounted into the pod.
//
// PVC only, and not the union of everything Kubernetes accepts: the engine
// mounts a volume for one purpose -- keeping a rotated credential between runs
// -- and a field that exists for one purpose should not accept ten shapes.
type Volume struct {
	Name string    `json:"name"`
	PVC  *FontePVC `json:"persistentVolumeClaim,omitempty"`
}

type FontePVC struct {
	ClaimName string `json:"claimName"`
}

type MontagemDeVolume struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

type RefLocal struct {
	Name string `json:"name"`
}

type Toleracao struct {
	Key      string `json:"key,omitempty"`
	Operator string `json:"operator,omitempty"`
	Value    string `json:"value,omitempty"`
	Effect   string `json:"effect,omitempty"`
}

type Container struct {
	Name         string             `json:"name"`
	Image        string             `json:"image"`
	Command      []string           `json:"command,omitempty"`
	Args         []string           `json:"args,omitempty"`
	Env          []Var              `json:"env,omitempty"`
	EnvFrom      []FonteEnv         `json:"envFrom,omitempty"`
	Resources    *Recursos          `json:"resources,omitempty"`
	WorkingDir   string             `json:"workingDir,omitempty"`
	VolumeMounts []MontagemDeVolume `json:"volumeMounts,omitempty"`
}

type Var struct {
	Name string `json:"name"`
	// Value with omitempty because a Var coming from a secret sends `valueFrom`,
	// and sending `"value":""` alongside makes the server refuse both.
	Value     string    `json:"value,omitempty"`
	ValueFrom *FonteVar `json:"valueFrom,omitempty"`
}

// FonteVar points a variable at a key of a Secret. The value never passes
// through the engine: the kubelet reads it when starting the container.
type FonteVar struct {
	SecretKeyRef *RefChave `json:"secretKeyRef,omitempty"`
}

type RefChave struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type FonteEnv struct {
	SecretRef    *RefLocal `json:"secretRef,omitempty"`
	ConfigMapRef *RefLocal `json:"configMapRef,omitempty"`
}

type Recursos struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

type PodStatus struct {
	Phase             string            `json:"phase,omitempty"`
	Conditions        []Condicao        `json:"conditions,omitempty"`
	Reason            string            `json:"reason,omitempty"`
	Message           string            `json:"message,omitempty"`
	ContainerStatuses []StatusContainer `json:"containerStatuses,omitempty"`
}

// Condicao carries PodScheduled, where the scheduler explains why it did not fit.
type Condicao struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type StatusContainer struct {
	Name  string `json:"name"`
	State struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting,omitempty"`
		Running *struct {
			StartedAt string `json:"startedAt"`
		} `json:"running,omitempty"`
		Terminated *struct {
			ExitCode int    `json:"exitCode"`
			Reason   string `json:"reason"`
			Message  string `json:"message"`
		} `json:"terminated,omitempty"`
	} `json:"state"`
}

// Fase returns the current phase; empty until the server answers with a status.
func (p Pod) Fase() string {
	if p.Status == nil {
		return ""
	}
	return p.Status.Phase
}

// Terminou says whether the pod reached a final state.
func (p Pod) Terminou() bool {
	f := p.Fase()
	return f == "Succeeded" || f == "Failed"
}

// Saida returns the container's exit code and whether it has finished.
func (p Pod) Saida() (int, bool) {
	if p.Status == nil {
		return 0, false
	}
	for _, c := range p.Status.ContainerStatuses {
		if c.Name == nomeContainer && c.State.Terminated != nil {
			return c.State.Terminated.ExitCode, true
		}
	}
	return 0, false
}

// MotivoDeEspera explains why the container has not run yet.
//
// It is the most useful piece of information when a step "does nothing":
// ImagePullBackOff and CreateContainerConfigError are configuration problems
// that, without this, would show up only as a pod sitting still until the
// timeout.
func (p Pod) MotivoDeEspera() string {
	if p.Status == nil {
		return ""
	}
	for _, c := range p.Status.ContainerStatuses {
		if c.Name == nomeContainer && c.State.Waiting != nil {
			w := c.State.Waiting
			if w.Message != "" {
				return w.Reason + ": " + w.Message
			}
			return w.Reason
		}
	}
	return ""
}

// Opcoes parameterises how pods are created. These are the INSTALLATION's
// decisions -- credentials, node pool, service account -- not the workflow
// author's: a pipeline YAML must not get to pick the service account it runs
// as.
type Opcoes struct {
	Namespace         string
	ServiceAccount    string
	PullSecrets       []string
	NodeSelector      map[string]string
	Tolerations       []Toleracao
	EnvFromSecrets    []string
	EnvFromConfigMaps []string

	// CredencialPVC and CredencialPath mount a volume where the SDK keeps the
	// credential it rotates between runs.
	//
	// With both set, EVERY step pod gets the volume and a BREVIS_CREDENTIAL_DIR
	// env pointing at the mount. Without them nothing changes -- which is how
	// the feature stays a shortcut rather than a requirement.
	//
	// The credential on the volume is encrypted; the key is an ordinary Secret,
	// arriving through EnvFromSecrets. The engine neither sees it nor needs
	// it.
	CredencialPVC  string
	CredencialPath string

	// SecretsPermitidos are the Secrets a YAML may name in `secrets:`.
	//
	// It exists because `secrets:` inverts who chooses. EnvFromSecrets comes
	// from the scheduler's environment: the INSTALLATION decides. `secrets:` is
	// in the file, and the file is written by somebody else -- without this
	// list, a workflow could mount any Secret in the namespace, including
	// Brevis's own database secret, and run an arbitrary command holding it.
	//
	// Empty denies everything. Denying by default costs one variable in the
	// installation; allowing by default costs the opposite, and the opposite is
	// irreversible.
	//
	// The final division is this: the installation says WHICH secrets exist for
	// workflows, the YAML says WHICH step receives each one.
	SecretsPermitidos []string
	Labels            map[string]string
	Shell             []string
	// EsperaParaIniciar is how long a pod may go without starting before the
	// step gives up. It exists because `Pending` is not an error to Kubernetes:
	// a pod that fits on no node sits there forever, and without this limit the
	// step waits along with it -- no log, no failure, no retry. It happened in
	// dev with a CPU request larger than the pool's free capacity.
	EsperaParaIniciar time.Duration

	// ManterPodEmFalha leaves the pod around for inspection when a step fails.
	// A successful one is always deleted: thousands of Completed pods clutter
	// the namespace and say nothing Brevis's own history does not say better.
	ManterPodEmFalha bool
}

const (
	nomeVolumeCredencial = "brevis-credentials"

	// The same variable the SDK reads. Written here rather than imported from
	// the SDK module on purpose: the engine does not depend on the SDK, and the
	// coupling between them is this name -- documented on both sides.
	envDiretorioCredencial = "BREVIS_CREDENTIAL_DIR"
)

// permiteSecret decides whether a YAML may name this Secret.
//
// The refusal happens while BUILDING the pod and not at the server: a
// secretKeyRef to a forbidden Secret is not even forbidden by Kubernetes -- it
// mounts, and the error one sees is a different one. Here the message names it
// and says where to allow it.
func (o Opcoes) permiteSecret(nome string) error {
	for _, p := range o.SecretsPermitidos {
		if p == nome {
			return nil
		}
	}
	if len(o.SecretsPermitidos) == 0 {
		return fmt.Errorf("secret %q is not allowed for workflows, and neither is any "+
			"other: the installation decides which exist, in "+
			"BREVIS_POD_ALLOWED_SECRETS", nome)
	}
	return fmt.Errorf("secret %q is not in BREVIS_POD_ALLOWED_SECRETS (allowed: %s)",
		nome, strings.Join(o.SecretsPermitidos, ", "))
}

func (o Opcoes) comPadroes() Opcoes {
	if len(o.Shell) == 0 {
		o.Shell = []string{"/bin/sh", "-c"}
	}
	if o.Namespace == "" {
		o.Namespace = "default"
	}
	// The PVC is what turns the feature on; the path has a default because
	// choosing it is nobody's decision -- it only has to be a predictable
	// place.
	if o.CredencialPVC != "" && o.CredencialPath == "" {
		o.CredencialPath = "/var/brevis/credentials"
	}
	if o.EsperaParaIniciar <= 0 {
		// Ten minutes cover pulling a large image (the dbt one is 620 MB) and an
		// autoscaler scale-up, without leaving a step stuck all night.
		o.EsperaParaIniciar = 10 * time.Minute
	}
	return o
}

// MontarPod translates a task into the object that goes to the API server.
//
// A pure function: it takes a task and options and returns the object. That is
// what makes it possible to test the whole spec -- image, command, resources,
// labels -- with no cluster at all.
func MontarPod(t execution.TaskExec, o Opcoes) (Pod, error) {
	o = o.comPadroes()
	if t.Image == "" {
		return Pod{}, fmt.Errorf("step %q has no image: in Kubernetes every step is a "+
			"pod, and the pod has to know what to run", t.NodeID)
	}
	if t.Command == "" {
		return Pod{}, fmt.Errorf("step %q has no command", t.NodeID)
	}

	c := Container{
		Name:       nomeContainer,
		Image:      t.Image,
		WorkingDir: t.WorkDir,
	}
	if t.Shell {
		c.Command = append(append([]string{}, o.Shell...), t.Command)
	} else {
		// Without a shell the command is an argv. Splitting on spaces is simple
		// on purpose: anyone who needs quotes, a pipe or a variable needs a
		// shell, and in that case `shell: false` is the wrong choice.
		c.Command = strings.Fields(t.Command)
	}

	// A sorted environment: two pods with the same content have to produce the
	// same JSON, or comparing two deploys turns into noise.
	chaves := make([]string, 0, len(t.Env))
	for k := range t.Env {
		chaves = append(chaves, k)
	}
	sort.Strings(chaves)
	for _, k := range chaves {
		c.Env = append(c.Env, Var{Name: k, Value: t.Env[k]})
	}

	// The secrets travel in the same list, but by reference: the value is not
	// here and never was -- the kubelet resolves it when starting the
	// container. A dump of this JSON shows the coordinate, not the secret.
	segredos := make([]string, 0, len(t.Secrets))
	for k := range t.Secrets {
		segredos = append(segredos, k)
	}
	sort.Strings(segredos)
	for _, k := range segredos {
		nome, chave, _ := strings.Cut(t.Secrets[k], "/")
		if err := o.permiteSecret(nome); err != nil {
			return Pod{}, fmt.Errorf("step %q, secrets[%q]: %w", t.NodeID, k, err)
		}
		c.Env = append(c.Env, Var{
			Name:      k,
			ValueFrom: &FonteVar{SecretKeyRef: &RefChave{Name: nome, Key: chave}},
		})
	}

	// The credential volume, when the installation configured one. The env
	// points at the mount, and it is the same one the SDK reads on somebody's
	// laptop with BREVIS_CREDENTIAL_DIR=./.brevis -- the same code in both.
	if o.CredencialPVC != "" {
		if _, jaTem := t.Env[envDiretorioCredencial]; !jaTem {
			c.Env = append(c.Env, Var{Name: envDiretorioCredencial, Value: o.CredencialPath})
		}
		c.VolumeMounts = append(c.VolumeMounts, MontagemDeVolume{
			Name: nomeVolumeCredencial, MountPath: o.CredencialPath,
		})
	}

	for _, s := range o.EnvFromSecrets {
		c.EnvFrom = append(c.EnvFrom, FonteEnv{SecretRef: &RefLocal{Name: s}})
	}
	for _, m := range o.EnvFromConfigMaps {
		c.EnvFrom = append(c.EnvFrom, FonteEnv{ConfigMapRef: &RefLocal{Name: m}})
	}
	if r := recursos(t); r != nil {
		c.Resources = r
	}

	spec := PodSpec{
		// Never: the dispatcher decides about another attempt, counting attempts
		// and applying backoff. Letting the kubelet restart on its own would
		// create a second retry policy, invisible to the history.
		RestartPolicy:      "Never",
		ServiceAccountName: o.ServiceAccount,
		NodeSelector:       o.NodeSelector,
		Tolerations:        o.Tolerations,
		Containers:         []Container{c},
	}
	if o.CredencialPVC != "" {
		spec.Volumes = append(spec.Volumes, Volume{
			Name: nomeVolumeCredencial,
			PVC:  &FontePVC{ClaimName: o.CredencialPVC},
		})
	}
	for _, s := range o.PullSecrets {
		spec.ImagePullSecrets = append(spec.ImagePullSecrets, RefLocal{Name: s})
	}
	if t.Timeout > 0 {
		// A safety net on the cluster's side: if the Brevis process dies, the
		// pod still stops on its own instead of running forever.
		segundos := int64(t.Timeout.Seconds())
		spec.ActiveDeadlineSeconds = &segundos
	}

	rotulos := map[string]string{
		"app.kubernetes.io/managed-by": "brevis",
		"brevis.dev/node":              valorDeRotulo(t.NodeID),
	}
	if t.RunID != "" {
		rotulos["brevis.dev/run"] = valorDeRotulo(t.RunID)
	}
	if t.Workflow != "" {
		rotulos["brevis.dev/workflow"] = valorDeRotulo(t.Workflow)
	}
	for k, v := range o.Labels {
		rotulos[k] = v
	}

	return Pod{
		APIVersion: "v1",
		Kind:       "Pod",
		Metadata: Metadata{
			Name:      NomeDoPod(t),
			Namespace: o.Namespace,
			Labels:    rotulos,
			// The annotation keeps the WHOLE value; the label keeps the
			// sanitised one. That way filtering by label works and the original
			// value is not lost.
			Annotations: map[string]string{
				"brevis.dev/workflow": t.Workflow,
				"brevis.dev/node":     t.NodeID,
				"brevis.dev/run":      t.RunID,
			},
		},
		Spec: spec,
	}, nil
}

func recursos(t execution.TaskExec) *Recursos {
	r := &Recursos{Requests: map[string]string{}, Limits: map[string]string{}}
	if t.CPU != "" {
		r.Requests["cpu"] = t.CPU
	}
	if t.Memoria != "" {
		r.Requests["memory"] = t.Memoria
	}
	if t.CPUMax != "" {
		r.Limits["cpu"] = t.CPUMax
	}
	if t.MemoriaMax != "" {
		r.Limits["memory"] = t.MemoriaMax
	}
	if len(r.Requests) == 0 {
		r.Requests = nil
	}
	if len(r.Limits) == 0 {
		r.Limits = nil
	}
	if r.Requests == nil && r.Limits == nil {
		return nil
	}
	return r
}

var invalidoEmNome = regexp.MustCompile(`[^a-z0-9-]+`)

// NomeDoPod produces a valid and STABLE name for the same attempt.
//
// Estavel importa: se o processo morrer entre criar o pod e registrar isso, a
// tentativa seguinte encontra o pod existente (409 AlreadyExists) em vez de
// subir um segundo pod rodando o mesmo dbt em paralelo com o primeiro.
//
// O sufixo de hash resolve a colisao que o corte de 63 caracteres criaria entre
// dois nodes de nome longo e prefixo comum.
func NomeDoPod(t execution.TaskExec) string {
	base := sanitizar(t.Workflow + "-" + t.NodeID)
	soma := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%d",
		t.RunID, t.NodeID, t.TentativaDoRun, t.Tentativa)))
	sufixo := hex.EncodeToString(soma[:4])

	const maxNome = 63
	if len(base)+1+len(sufixo) > maxNome {
		base = base[:maxNome-1-len(sufixo)]
		base = strings.TrimRight(base, "-")
	}
	return base + "-" + sufixo
}

func sanitizar(s string) string {
	s = strings.ToLower(s)
	s = invalidoEmNome.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "step"
	}
	return s
}

// valorDeRotulo obedece o limite de 63 caracteres dos labels; o valor completo
// goes in the annotation, which accepts far more.
func valorDeRotulo(s string) string {
	s = sanitizar(s)
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

// net joins host and port taking care of IPv6, where the host arrives without brackets.
func net_(host, porta string) string { return net.JoinHostPort(host, porta) }

type tlsConfig struct{ pool *x509.CertPool }

func (t tlsConfig) build() *tls.Config {
	return &tls.Config{RootCAs: t.pool, MinVersion: tls.VersionTLS12}
}

// Motivo e o `reason` do status (DeadlineExceeded, OOMKilled, Evicted) — a
// diferenca entre "o codigo falhou" e "o cluster matou o processo".
func (p Pod) Motivo() string {
	if p.Status == nil {
		return ""
	}
	return p.Status.Reason
}
