package kubernetes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution"
)

func simpleTask() execution.TaskExec {
	return execution.TaskExec{
		NodeID:  "fetch_occurrences",
		Image:   "data-pipeline-go:local",
		Command: "/usr/local/bin/gabriel",
	}
}

// TestTheCredentialVolumeMountsAndInjectsTheDirectory: with the PVC configured,
// every step pod gains the volume and the env var the SDK reads. It is step 5 of
// the volume's spec.
func TestTheCredentialVolumeMountsAndInjectsTheDirectory(t *testing.T) {
	pod, err := BuildPod(simpleTask(), Options{
		CredentialPVC: "brevis-credentials",
	}.comPadroes())
	if err != nil {
		t.Fatalf("BuildPod: %v", err)
	}

	if len(pod.Spec.Volumes) != 1 {
		t.Fatalf("volumes = %d, expected 1", len(pod.Spec.Volumes))
	}
	v := pod.Spec.Volumes[0]
	if v.PVC == nil || v.PVC.ClaimName != "brevis-credentials" {
		t.Errorf("PVC errado: %+v", v)
	}

	c := pod.Spec.Containers[0]
	if len(c.VolumeMounts) != 1 || c.VolumeMounts[0].Name != v.Name {
		t.Fatalf("the mount does not point at the volume: %+v", c.VolumeMounts)
	}
	if got := c.VolumeMounts[0].MountPath; got != "/var/brevis/credentials" {
		t.Errorf("mountPath = %q, expected the default", got)
	}

	var dir string
	for _, e := range c.Env {
		if e.Name == "BREVIS_CREDENTIAL_DIR" {
			dir = e.Value
		}
	}
	if dir != c.VolumeMounts[0].MountPath {
		t.Errorf("BREVIS_CREDENTIAL_DIR = %q, and the mount is at %q", dir, c.VolumeMounts[0].MountPath)
	}
}

// TestTheVolumesPathIsConfigurable.
func TestTheVolumesPathIsConfigurable(t *testing.T) {
	pod, err := BuildPod(simpleTask(), Options{
		CredentialPVC:  "meu-pvc",
		CredentialPath: "/mnt/cred",
	}.comPadroes())
	if err != nil {
		t.Fatalf("BuildPod: %v", err)
	}
	if got := pod.Spec.Containers[0].VolumeMounts[0].MountPath; got != "/mnt/cred" {
		t.Errorf("mountPath = %q", got)
	}
}

// TestWithNoPVCNothingChanges: this is how the feature stays a shortcut rather
// than a requirement -- an installation that did not configure it must see no
// difference.
func TestWithNoPVCNothingChanges(t *testing.T) {
	pod, err := BuildPod(simpleTask(), Options{}.comPadroes())
	if err != nil {
		t.Fatalf("BuildPod: %v", err)
	}
	if len(pod.Spec.Volumes) != 0 {
		t.Errorf("it mounted a volume with no PVC: %+v", pod.Spec.Volumes)
	}
	if len(pod.Spec.Containers[0].VolumeMounts) != 0 {
		t.Errorf("it added a mount with no PVC")
	}
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == "BREVIS_CREDENTIAL_DIR" {
			t.Errorf("it injected the env var with no volume at all: the SDK would try to write into a directory that does not exist")
		}
	}
	b, _ := json.Marshal(pod)
	if strings.Contains(string(b), "volumes") {
		t.Errorf("o JSON carrega volumes vazio:\n%s", b)
	}
}

// TestTheStepsEnvBeatsTheDefaultDirectory: a step that declares its own
// BREVIS_CREDENTIAL_DIR knows what it is doing, and the injection must not
// duplicate the variable -- two values for one name and the server picking one.
func TestTheStepsEnvBeatsTheDefaultDirectory(t *testing.T) {
	task := simpleTask()
	task.Env = map[string]string{"BREVIS_CREDENTIAL_DIR": "/outro/lugar"}

	pod, err := BuildPod(task, Options{CredentialPVC: "pvc"}.comPadroes())
	if err != nil {
		t.Fatalf("BuildPod: %v", err)
	}
	var vistos []string
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == "BREVIS_CREDENTIAL_DIR" {
			vistos = append(vistos, e.Value)
		}
	}
	if len(vistos) != 1 {
		t.Fatalf("BREVIS_CREDENTIAL_DIR aparece %d vezes: %v", len(vistos), vistos)
	}
	if vistos[0] != "/outro/lugar" {
		t.Errorf("the injection overwrote what the step declared: %q", vistos[0])
	}
}
