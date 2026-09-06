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

// O cano inteiro, com peças de verdade: um binário compilado com o SDK, o
// executor de processo, um pipe do sistema operacional, o runner e o Postgres.
//
// Tudo o mais no caminho das etapas era testado com executor falso e
// persistidor espião. A linha `@brevis:` nunca tinha atravessado um pipe real
// nem um bufio.Scanner -- e foi assim que a v0.4.0 do motor foi publicada com
// a feature nunca exercitada de ponta a ponta.
//
// O que continua fora do alcance daqui: o `Logs(ctx, pod, follow=true)` do
// Kubernetes. Ele exige um cluster, e os contextos desta máquina são clusters
// reais, inclusive produção.
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
	// O WorkDir do passo é o diretório do teste, porque o fetcher lê
	// "entrada.ndjson" relativo a ele. O cwd VOLTA no fim: deixá-lo num
	// diretório temporário quebraria qualquer teste seguinte deste pacote que
	// use caminho relativo -- e este mesmo teste usa, para compilar o fetcher.
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

	// O selo, observado do binário. "devel" porque o testdata usa replace --
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
