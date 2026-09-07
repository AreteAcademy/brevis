package execution_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution/local"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// The whole pipe, with real parts: a binary compiled against the SDK, the process
// executor, an operating-system pipe, the runner and Postgres.
//
// Everything else on the stages path was tested with a fake executor and a spy
// persister. The `@brevis:` line had never crossed a real pipe or a
// bufio.Scanner -- and that is how the engine's v0.4.0 was published with the
// feature never exercised end to end.
//
// What stays out of reach here: Kubernetes's `Logs(ctx, pod, follow=true)`. It
// requires a cluster, and this machine's contexts are real clusters, production
// among them.
func TestIntegrationStagesReachTheDatabaseThroughARealBinary(t *testing.T) {
	dsn := os.Getenv("BREVIS_IT_PG_DSN")
	if dsn == "" {
		t.Skip("BREVIS_IT_PG_DSN ausente")
	}

	ctx := context.Background()
	pool, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	defer pool.Close()

	dir := t.TempDir()
	binary := buildFetcher(t, dir)
	input := filepath.Join(dir, "entrada.ndjson")
	if err := os.WriteFile(input, []byte(
		`{"id":1,"ts":"2026-09-05T00:00:00Z"}`+"\n"+
			`{"id":2,"ts":"2026-09-05T01:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runID := uuid.New()
	repo := postgres.NewRunRepo(pool)
	if err := createRun(ctx, pool, runID); err != nil {
		t.Fatalf("criando a run: %v", err)
	}

	r := app.Runner{
		RunID:    runID,
		Persist:  repo,
		Processo: localExecutor(t),
	}
	w := wf.Workflow{
		Slug:  "e2e",
		Nodes: []wf.Node{{ID: "collectOutput", Run: binary + " 2>&1"}},
	}
	// The step's WorkDir is the test's directory, because the fetcher reads
	// "entrada.ndjson" relative to it. The cwd is RESTORED at the end: leaving it
	// in a temporary directory would break any later test in this package that
	// uses a relative path -- and this very test does, to compile the fetcher.
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, w); err != nil {
		t.Fatalf("run: %v", err)
	}

	states, err := repo.NodeStates(ctx, runID)
	if err != nil {
		t.Fatalf("estado: %v", err)
	}
	e, ok := states["collectOutput"]
	if !ok {
		t.Fatal("the step never reached the database")
	}

	// The badge, observed from the binary. "devel" because the testdata uses a
	// replace --
	// and it is the truth: the code came from a directory, not from a version.
	if e.SdkVersion == "" {
		t.Error("the step did not announce itself as an SDK one")
	}
	if e.SdkVersion != "devel" {
		t.Errorf("with a replace the version has to be \"devel\", got %q", e.SdkVersion)
	}

	byName := map[string]postgres.Stage{}
	for _, et := range e.Stages {
		byName[et.Name] = et
	}
	for _, name := range []string{"check", "extract", "map", "load"} {
		et, ok := byName[name]
		if !ok {
			t.Errorf("stage %q never reached the database (these did: %v)", name, e.Stages)
			continue
		}
		if et.State != "done" {
			t.Errorf("stage %q finished at %q", name, et.State)
		}
	}
	// What only a Map stage knows.
	if n := byName["map"].Numbers; n == nil || n["in"] != 2.0 || n["out"] != 2.0 {
		t.Errorf("the transform did not report its counts: %v", byName["transform"].Numbers)
	}
	// And a Map stage still has no clock.
	if byName["map"].Ms != nil {
		t.Errorf("a map stage reported a duration: %v", *byName["map"].Ms)
	}

	// The marker must NOT have become the step's log.
	logs, err := repo.LogsDaRun(ctx, runID)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	for _, l := range logs {
		if strings.Contains(l.Log, "@brevis:") {
			t.Errorf("the marker ended up in the step's log:\n%s", l.Log)
		}
	}
}

// buildFetcher builds the testdata binary, which is a module of its own.
func buildFetcher(t *testing.T, dir string) string {
	t.Helper()
	output := filepath.Join(dir, "fetcher")
	cmd := exec.Command("go", "build", "-o", output, ".")
	cmd.Dir = "testdata/fetcher"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compilando o fetcher: %v\n%s", err, out)
	}
	return output
}

func createRun(ctx context.Context, pool *postgres.Pool, id uuid.UUID) error {
	def, _ := json.Marshal(wf.Workflow{Slug: "e2e"})
	_, err := pool.Exec(ctx, `
		INSERT INTO runs (id, workflow_slug, idempotency_key, status, definicao, criado_em)
		VALUES ($1, 'e2e', $2, 'running', $3, $4)`,
		id, "e2e-"+id.String(), def, time.Now())
	return err
}

func localExecutor(t *testing.T) *local.ProcessExecutor {
	t.Helper()
	e, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	return e
}
