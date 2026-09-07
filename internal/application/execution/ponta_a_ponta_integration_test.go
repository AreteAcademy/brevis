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

// The whole pipe, with real parts: a binary compiled against the SDK, the
// executor de processo, um pipe do sistema operacional, o runner e o Postgres.
//
// Everything else on the stages path was tested with a fake executor and a spy
// persister. The `@brevis:` line had never crossed a real pipe or a
// bufio.Scanner -- and that is how the engine's v0.4.0 was published with the
// feature never exercised end to end.
//
// What stays out of reach here: Kubernetes's `Logs(ctx, pod, follow=true)`. It
// requires a cluster, and this machine's contexts are real clusters, production
// among them.
func TestIntegrationEtapasChegamAoBancoPorUmBinarioDeVerdade(t *testing.T) {
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
	binario := compilarFetcher(t, dir)
	entrada := filepath.Join(dir, "entrada.ndjson")
	if err := os.WriteFile(entrada, []byte(
		`{"id":1,"ts":"2026-09-05T00:00:00Z"}`+"\n"+
			`{"id":2,"ts":"2026-09-05T01:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runID := uuid.New()
	repo := postgres.NewRunRepo(pool)
	if err := criarRun(ctx, pool, runID); err != nil {
		t.Fatalf("criando a run: %v", err)
	}

	r := app.Runner{
		RunID:    runID,
		Persist:  repo,
		Processo: executorLocal(t),
	}
	w := wf.Workflow{
		Slug:  "e2e",
		Nodes: []wf.Node{{ID: "coletar", Run: binario + " 2>&1"}},
	}
	// The step's WorkDir is the test's directory, because the fetcher reads
	// "entrada.ndjson" relative to it. The cwd is RESTORED at the end: leaving it
	// in a temporary directory would break any later test in this package that
	// uses a relative path -- and this very test does, to compile the fetcher.
	anterior, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(anterior) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, w); err != nil {
		t.Fatalf("run: %v", err)
	}

	estados, err := repo.EstadoDosNos(ctx, runID)
	if err != nil {
		t.Fatalf("estado: %v", err)
	}
	e, ok := estados["coletar"]
	if !ok {
		t.Fatal("o passo não chegou ao banco")
	}

	// The badge, observed from the binary. "devel" because the testdata uses a
	// replace --
	// e é a verdade: o código veio de um diretório, não de uma versão.
	if e.SdkVersao == "" {
		t.Error("o passo não se anunciou como SDK")
	}
	if e.SdkVersao != "devel" {
		t.Errorf("com replace a versão tem de ser \"devel\", veio %q", e.SdkVersao)
	}

	porNome := map[string]postgres.Etapa{}
	for _, et := range e.Etapas {
		porNome[et.Nome] = et
	}
	for _, nome := range []string{"check", "extract", "map", "load"} {
		et, ok := porNome[nome]
		if !ok {
			t.Errorf("a etapa %q não chegou ao banco (chegaram: %v)", nome, e.Etapas)
			continue
		}
		if et.State != "done" {
			t.Errorf("etapa %q terminou em %q", nome, et.State)
		}
	}
	// What only a Map stage knows.
	if n := porNome["map"].Numeros; n == nil || n["in"] != 2.0 || n["out"] != 2.0 {
		t.Errorf("the transform did not report its counts: %v", porNome["transform"].Numeros)
	}
	// And a Map stage still has no clock.
	if porNome["map"].Ms != nil {
		t.Errorf("a map stage reported a duration: %v", *porNome["map"].Ms)
	}

	// A marca NÃO pode ter virado log do passo.
	logs, err := repo.LogsDaRun(ctx, runID)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	for _, l := range logs {
		if strings.Contains(l.Log, "@brevis:") {
			t.Errorf("a marca foi parar no log do passo:\n%s", l.Log)
		}
	}
}

// compilarFetcher constrói o binário do testdata, que é um módulo próprio.
func compilarFetcher(t *testing.T, dir string) string {
	t.Helper()
	saida := filepath.Join(dir, "fetcher")
	cmd := exec.Command("go", "build", "-o", saida, ".")
	cmd.Dir = "testdata/fetcher"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compilando o fetcher: %v\n%s", err, out)
	}
	return saida
}

func criarRun(ctx context.Context, pool *postgres.Pool, id uuid.UUID) error {
	def, _ := json.Marshal(wf.Workflow{Slug: "e2e"})
	_, err := pool.Exec(ctx, `
		INSERT INTO runs (id, workflow_slug, idempotency_key, status, definicao, criado_em)
		VALUES ($1, 'e2e', $2, 'running', $3, $4)`,
		id, "e2e-"+id.String(), def, time.Now())
	return err
}

func executorLocal(t *testing.T) *local.ProcessExecutor {
	t.Helper()
	e, err := local.New("local")
	if err != nil {
		t.Fatal(err)
	}
	return e
}
