package kubernetes_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/execution"
	k8s "github.com/AreteAcademy/brevis/internal/execution/kubernetes"
)

func task() execution.TaskExec {
	return execution.TaskExec{
		ExecutionID: "wf:passo",
		NodeID:      "bronze_workspace",
		Workflow:    "platform_workspace",
		RunID:       "1f2e3d4c-0000-0000-0000-000000000000",
		Image:       "us-central1-docker.pkg.dev/acme/apps/dbt:1.10.3",
		Command:     "dbt build --select bronze_workspace+",
		Shell:       true,
		CPU:         "200m",
		Memoria:     "1Gi",
		MemoriaMax:  "2Gi",
		Env:         map[string]string{"STAGE": "prod", "GOOGLE_PROJECT_ID": "acme"},
		Timeout:     30 * time.Minute,
	}
}

func TestThePodCarriesImageCommandAndResources(t *testing.T) {
	p, err := k8s.BuildPod(task(), k8s.Options{Namespace: "dados", ServiceAccount: "brevis"})
	if err != nil {
		t.Fatal(err)
	}

	c := p.Spec.Containers[0]
	if c.Image != task().Image {
		t.Errorf("imagem = %q", c.Image)
	}
	if len(c.Command) != 3 || c.Command[0] != "/bin/sh" || c.Command[1] != "-c" {
		t.Errorf("comando = %v, want sh -c", c.Command)
	}
	if !strings.HasPrefix(c.Command[2], "dbt build") {
		t.Errorf("command line = %q", c.Command[2])
	}
	if c.Resources.Requests["cpu"] != "200m" || c.Resources.Requests["memory"] != "1Gi" {
		t.Errorf("requests = %v", c.Resources.Requests)
	}
	if c.Resources.Limits["memory"] != "2Gi" {
		t.Errorf("limits = %v", c.Resources.Limits)
	}
	if _, temCPU := c.Resources.Limits["cpu"]; temCPU {
		t.Error("an undeclared CPU limit must not be invented -- silent throttling is worse than no limit")
	}

	// Never: what counts attempts and applies backoff is the dispatcher. Letting
	// the kubelet restart would create a second retry policy, an invisible one.
	if p.Spec.RestartPolicy != "Never" {
		t.Errorf("restartPolicy = %q", p.Spec.RestartPolicy)
	}
	if p.Spec.ServiceAccountName != "brevis" || p.Metadata.Namespace != "dados" {
		t.Errorf("identidade errada: sa=%q ns=%q", p.Spec.ServiceAccountName, p.Metadata.Namespace)
	}
	if p.Spec.ActiveDeadlineSeconds == nil || *p.Spec.ActiveDeadlineSeconds != 1800 {
		t.Errorf("deadline = %v, want 1800s", p.Spec.ActiveDeadlineSeconds)
	}
}

// A distroless image has no shell: `sh -c` would fail with "no such file or
// directory", an error that says nothing about the cause.
func TestWithNoShellItUsesArgvDirectly(t *testing.T) {
	tk := task()
	tk.Shell = false
	tk.Command = "/notify --canal dados"

	p, err := k8s.BuildPod(tk, k8s.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := p.Spec.Containers[0].Command
	if len(c) != 3 || c[0] != "/notify" || c[2] != "dados" {
		t.Errorf("comando = %v, want argv direto", c)
	}
}

func TestEnvIsOrderedAndEnvFrom(t *testing.T) {
	p, err := k8s.BuildPod(task(), k8s.Options{
		EnvFromSecrets:    []string{"brevis-bigquery"},
		EnvFromConfigMaps: []string{"brevis-config"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := p.Spec.Containers[0]
	// A stable order: two pods with the same contents have to produce the same
	// JSON, otherwise comparing two deploys becomes noise.
	if len(c.Env) != 2 || c.Env[0].Name != "GOOGLE_PROJECT_ID" || c.Env[1].Name != "STAGE" {
		t.Errorf("env = %v, want ordem alfabetica", c.Env)
	}
	if len(c.EnvFrom) != 2 || c.EnvFrom[0].SecretRef.Name != "brevis-bigquery" {
		t.Errorf("envFrom = %+v", c.EnvFrom)
	}
}

// The BigQuery credential comes in through envFrom, the INSTALLATION's decision.
// A pipeline's YAML must not get to choose the service account it runs as.
func TestTheOptionsDoNotComeFromTheWorkflow(t *testing.T) {
	p, _ := k8s.BuildPod(task(), k8s.Options{
		ServiceAccount: "restrita",
		PullSecrets:    []string{"registry"},
		NodeSelector:   map[string]string{"pool": "dados"},
	})
	if p.Spec.ServiceAccountName != "restrita" ||
		p.Spec.ImagePullSecrets[0].Name != "registry" ||
		p.Spec.NodeSelector["pool"] != "dados" {
		t.Errorf("the installation's options did not reach the pod: %+v", p.Spec)
	}
}

func TestAPodWithNoImageIsRefused(t *testing.T) {
	tk := task()
	tk.Image = ""
	if _, err := k8s.BuildPod(tk, k8s.Options{}); err == nil {
		t.Error("a pod with no image has nothing to run")
	}
}

// The name has to be stable for the SAME attempt: if the process dies between
// creating the pod and recording that, the next attempt finds the existing pod
// instead of
// vez de subir um segundo rodando o mesmo dbt em paralelo.
func TestThePodsNameIsStablePerAttempt(t *testing.T) {
	a := k8s.PodName(task())
	if b := k8s.PodName(task()); a != b {
		t.Errorf("the same attempt produced %q and %q", a, b)
	}

	outra := task()
	outra.Attempt = 1
	if c := k8s.PodName(outra); c == a {
		t.Error("attempts diferentes deveriam gerar pods diferentes")
	}
}

func TestThePodsNameObeysKubernetesLimit(t *testing.T) {
	tk := task()
	tk.Workflow = strings.Repeat("workflow-de-nome-absurdamente-longo-", 3)
	tk.NodeID = strings.Repeat("passo-tambem-enorme-", 3)

	name := k8s.PodName(tk)
	if len(name) > 63 {
		t.Errorf("a name with %d characters: %q", len(name), name)
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			t.Fatalf("caractere invalido %q em %q", r, name)
		}
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		t.Errorf("a name may neither start nor end with a hyphen: %q", name)
	}

	// Two long names sharing a prefix must not collide after the trim.
	outro := tk
	outro.NodeID = strings.Repeat("passo-tambem-enorme-", 3) + "-b"
	if k8s.PodName(outro) == name {
		t.Error("the 63-character trim created a collision between two steps")
	}
}

func TestTheLabelsMakeTheRunsPodsFindable(t *testing.T) {
	p, _ := k8s.BuildPod(task(), k8s.Options{})
	if p.Metadata.Labels["app.kubernetes.io/managed-by"] != "brevis" {
		t.Error("without the management label there is no way to find Brevis's pods")
	}
	if p.Metadata.Labels["brevis.dev/workflow"] != "platform-workspace" {
		t.Errorf("rotulo de workflow = %q (sanitizado)", p.Metadata.Labels["brevis.dev/workflow"])
	}
	// The original value lives in the annotation: a label has a 63-character limit
	// and a restricted alphabet, an annotation does not.
	if p.Metadata.Annotations["brevis.dev/workflow"] != "platform_workspace" {
		t.Errorf("annotation = %q, want the original value", p.Metadata.Annotations["brevis.dev/workflow"])
	}
}

// The object has to be accepted as Kubernetes JSON: empty fields omitted, so as
// not to send `resources: {}` or `nodeSelector: null`.
func TestTheJSONCarriesNoEmptyFields(t *testing.T) {
	tk := task()
	tk.CPU, tk.Memoria, tk.CPUMax, tk.MemoriaMax = "", "", "", ""
	tk.Timeout = 0

	p, _ := k8s.BuildPod(tk, k8s.Options{})
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, proibido := range []string{`"resources"`, `"nodeSelector"`, `"activeDeadlineSeconds"`, `"imagePullSecrets"`, `"status"`} {
		if strings.Contains(string(b), proibido) {
			t.Errorf("the JSON carries %s with no value: %s", proibido, b)
		}
	}
}
