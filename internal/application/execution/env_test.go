package execution

import (
	"testing"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// TestTheEnvironmentsPrecedence: do mais fraco ao mais forte -- ambiente global do
// engine's, the workflow's `env:`, the step's `env:`.
//
// The step beating the global is the part that changed. The other way round, a
// variable declared in the file would lose quietly to a BREVIS_TASK_ENV somebody
// configured months ago -- and "loses quietly" is the failure mode this
// projeto mais persegue.
func TestTheEnvironmentsPrecedence(t *testing.T) {
	w := wf.Workflow{
		Slug: "x",
		Env:  map[string]string{"NIVEL": "workflow", "SO_DO_WORKFLOW": "1"},
		Nodes: []wf.Node{
			{ID: "com_env", Run: "echo", Env: map[string]string{"NIVEL": "passo"}},
			{ID: "sem_env", Run: "echo"},
		},
	}
	r := Runner{Env: map[string]string{"NIVEL": "global", "SO_DO_GLOBAL": "1"}}

	casos := map[string]string{"com_env": "passo", "sem_env": "workflow"}
	for _, n := range w.Nodes {
		env := mesclarEnv(r.Env, r.runContext(n.ID, false, 0), w.EnvDe(n))

		if got := env["NIVEL"]; got != casos[n.ID] {
			t.Errorf("%s: NIVEL = %q, expected %q", n.ID, got, casos[n.ID])
		}
		// Inheriting must not mean losing the rest.
		if env["SO_DO_WORKFLOW"] != "1" {
			t.Errorf("%s: it lost the variable only the workflow declared", n.ID)
		}
		if env["SO_DO_GLOBAL"] != "1" {
			t.Errorf("%s: it lost the variable only the global declared", n.ID)
		}
	}
}

// TestSecretsDoNotEnterTheTasksEnv: if they did, the value would have to be
// resolved at assembly time -- and would pass through the dispatcher, through
// the log and through any TaskExec dump somebody writes later.
func TestSecretsDoNotEnterTheTasksEnv(t *testing.T) {
	w := wf.Workflow{
		Slug:    "x",
		Secrets: map[string]string{"TOKEN": "cofre/token"},
		Nodes:   []wf.Node{{ID: "a", Run: "echo"}},
	}
	n := w.Nodes[0]

	env := mesclarEnv(nil, nil, w.EnvDe(n))
	if _, tem := env["TOKEN"]; tem {
		t.Error("the secret went into the task's Env")
	}
	if w.SecretsDe(n)["TOKEN"] != "cofre/token" {
		t.Error("the secret did not arrive as a coordinate")
	}
}
