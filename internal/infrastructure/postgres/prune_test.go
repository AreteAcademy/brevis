package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// aged writes a run of the given age and status, with a step carrying a log,
// phases, published output and a row in the load trend.
func aged(t *testing.T, pool *postgres.Pool, slug string, age time.Duration, status dom.Status) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: slug, IdempotencyKey: uuid.NewString(),
		TriggerType: "schedule", Definition: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE runs SET status = $2, criado_em = now() - $3::interval WHERE id = $1`,
		r.ID, string(status), age.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO task_runs (id, run_id, node_id, status, attempt, map_index, etapas, log, saida)
		VALUES ($1, $2, 'carga', 'success', 0, -1,
		        '[{"indice":0,"nome":"load","estado":"done","em":"x","ms":900,"numeros":{"rows":10}}]',
		        'fetched page 1 of 300', '{"rows":10}')`,
		uuid.New(), r.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordLoad(ctx, r.ID, dom.Step("carga"), slug,
		app.LoadNumbers{Rows: 10, LoadMs: 900}); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// bulk reads back what the step still carries.
func bulk(t *testing.T, pool *postgres.Pool, runID uuid.UUID) (log string, stages string, saida *string, found bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT log, etapas::text, saida::text FROM task_runs WHERE run_id = $1`, runID).
		Scan(&log, &stages, &saida)
	if err != nil {
		return "", "", nil, false
	}
	return log, stages, saida, true
}

func runExists(t *testing.T, pool *postgres.Pool, runID uuid.UUID) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM runs WHERE id = $1`, runID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func trendRows(t *testing.T, pool *postgres.Pool, slug string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM load_metrics WHERE workflow_slug = $1`, slug).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestPruneWithNothingConfigured.
//
// The standing rule, and the one this repository has been bitten by twice: a
// feature with a default gets a test that configures NOTHING. Every other test
// here sets the windows it is testing, which is exactly how two features
// shipped switched off and nobody noticed.
//
// So this one takes the policy an operator gets by typing `brevis prune`, and
// asserts the defaults do the thing the defaults promise.
func TestPruneWithNothingConfigured(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()

	// The command's own defaults, written the way the flags declare them.
	policy := postgres.Retention{
		TrimAfter:  30 * 24 * time.Hour,
		PurgeAfter: 365 * 24 * time.Hour,
	}

	yesterday := aged(t, pool, "vendas", 24*time.Hour, dom.StatusSuccess)
	lastQuarter := aged(t, pool, "vendas", 60*24*time.Hour, dom.StatusSuccess)
	twoYearsAgo := aged(t, pool, "vendas", 730*24*time.Hour, dom.StatusSuccess)

	report, err := postgres.NewRunRepo(pool).Prune(ctx, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Trimmed != 2 || report.Purged != 1 {
		t.Errorf("the defaults did %s", report)
	}

	// Yesterday's run is untouched: its log is what somebody is reading now.
	if log, stages, saida, ok := bulk(t, pool, yesterday); !ok ||
		log == "" || stages == "[]" || saida == nil {
		t.Errorf("yesterday's run was trimmed: log=%q stages=%q", log, stages)
	}
	// Last quarter's is trimmed but still there.
	if log, stages, saida, ok := bulk(t, pool, lastQuarter); !ok ||
		log != "" || stages != "[]" || saida != nil {
		t.Errorf("a 60-day-old run kept its bulk: log=%q stages=%q saida=%v", log, stages, saida)
	}
	if !runExists(t, pool, lastQuarter) {
		t.Error("a 60-day-old run was deleted; the default only trims until a year")
	}
	// Two years ago is gone.
	if runExists(t, pool, twoYearsAgo) {
		t.Error("a two-year-old run survived the default purge")
	}
	// And the trend kept every one of the three.
	if got := trendRows(t, pool, "vendas"); got != 3 {
		t.Errorf("the trend has %d of 3 rows; the summary must outlive the detail", got)
	}
}

// A trimmed run still opens, and everything an incident actually needs is still
// on it. This is the assertion that makes trimming safe to default.
func TestATrimmedRunKeepsEverythingButTheBulk(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()
	id := aged(t, pool, "vendas", 60*24*time.Hour, dom.StatusFailed)
	if _, err := pool.Exec(ctx,
		`UPDATE task_runs SET status = 'failed', exit_code = 3, erro = 'connection refused',
		 iniciado_em = now() - interval '60 days', terminado_em = now() - interval '60 days' + interval '4 minutes'
		 WHERE run_id = $1`, id); err != nil {
		t.Fatal(err)
	}

	if _, err := postgres.NewRunRepo(pool).Prune(ctx,
		postgres.Retention{TrimAfter: 30 * 24 * time.Hour}, false); err != nil {
		t.Fatal(err)
	}

	var status, erro string
	var exit int
	var started, finished *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT status, erro, exit_code, iniciado_em, terminado_em FROM task_runs WHERE run_id = $1`, id).
		Scan(&status, &erro, &exit, &started, &finished); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || erro != "connection refused" || exit != 3 {
		t.Errorf("the trim took the diagnosis with the log: status=%q erro=%q exit=%d", status, erro, exit)
	}
	if started == nil || finished == nil {
		t.Error("the trim took the timings")
	}
}

// Running it twice does the work once.
//
// Not a nicety: without the `log <> ”` half of the eligibility query this does
// not terminate at all. Every batch finds the same rows still eligible, returns
// a full batch, and the loop goes round forever -- which is how this test found
// it, by hanging rather than by failing.
func TestASecondPruneTouchesNothing(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	for i := 0; i < 3; i++ {
		aged(t, pool, "vendas", 60*24*time.Hour, dom.StatusSuccess)
	}
	policy := postgres.Retention{TrimAfter: 30 * 24 * time.Hour}

	first, err := repo.Prune(ctx, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Trimmed != 3 {
		t.Fatalf("the first prune trimmed %d of 3", first.Trimmed)
	}
	second, err := repo.Prune(ctx, policy, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Trimmed != 0 {
		t.Errorf("the second prune rewrote %d row(s) that were already empty", second.Trimmed)
	}
}

// A run the system may still act on is never deleted, however old the row is.
//
// A queue item stuck since March is a bug worth seeing. Deleting it would hide
// the bug and take with it whatever a dispatcher still believes it owns -- and
// `failed` is on the other side of that line, because the state machine allows
// a retry but nothing is going to retry a run from last year on its own.
func TestAnInFlightRunIsNeverPurgedHoweverOld(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()

	kept := map[dom.Status]uuid.UUID{}
	for _, s := range dom.RunStatuses() {
		kept[s] = aged(t, pool, "vendas", 730*24*time.Hour, s)
	}
	if _, err := postgres.NewRunRepo(pool).Prune(ctx,
		postgres.Retention{TrimAfter: time.Hour, PurgeAfter: 365 * 24 * time.Hour}, false); err != nil {
		t.Fatal(err)
	}

	for status, id := range kept {
		alive := runExists(t, pool, id)
		if status.InFlight() && !alive {
			t.Errorf("a two-year-old %s run was deleted; something may still own it", status)
		}
		if !status.InFlight() && alive {
			t.Errorf("a two-year-old %s run survived the purge", status)
		}
	}
}

// --dry-run changes nothing, and counts what it would have changed. An operator
// running this against a real installation for the first time has to be able to
// look before believing.
func TestDryRunCountsAndChangesNothing(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()
	old := aged(t, pool, "vendas", 60*24*time.Hour, dom.StatusSuccess)
	ancient := aged(t, pool, "vendas", 730*24*time.Hour, dom.StatusSuccess)

	report, err := postgres.NewRunRepo(pool).Prune(ctx, postgres.Retention{
		TrimAfter: 30 * 24 * time.Hour, PurgeAfter: 365 * 24 * time.Hour,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun {
		t.Error("the report does not say it was a dry run")
	}
	// Both are old enough to trim; one is old enough to purge as well.
	if report.Trimmed != 2 || report.Purged != 1 {
		t.Errorf("the dry run counted %s", report)
	}
	if log, _, _, _ := bulk(t, pool, old); log == "" {
		t.Error("--dry-run trimmed a run")
	}
	if !runExists(t, pool, ancient) {
		t.Error("--dry-run deleted a run")
	}
}

// Purge zero means never, which is a real answer and not a missing one.
func TestPurgeZeroNeverDeletes(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()
	ancient := aged(t, pool, "vendas", 3650*24*time.Hour, dom.StatusSuccess)

	report, err := postgres.NewRunRepo(pool).Prune(ctx,
		postgres.Retention{TrimAfter: 30 * 24 * time.Hour, PurgeAfter: 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Purged != 0 {
		t.Errorf("purge-after=0 deleted %d run(s)", report.Purged)
	}
	if !runExists(t, pool, ancient) {
		t.Error("a ten-year-old run was deleted with purging switched off")
	}
}

// The batching is what keeps a neglected database from taking one long lock,
// and the loop has to finish the job rather than stopping at the first batch.
func TestABatchSmallerThanTheWorkStillFinishesIt(t *testing.T) {
	pool := loadDB(t)
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		aged(t, pool, "vendas", 60*24*time.Hour, dom.StatusSuccess)
	}
	report, err := postgres.NewRunRepo(pool).Prune(ctx,
		postgres.Retention{TrimAfter: 30 * 24 * time.Hour, Batch: 2}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Trimmed != 7 {
		t.Errorf("a batch of 2 trimmed %d of 7", report.Trimmed)
	}
}

// A policy that would destroy more than it says is refused before it runs.
func TestAnImpossiblePolicyIsRefused(t *testing.T) {
	for name, p := range map[string]postgres.Retention{
		"trim of zero":          {TrimAfter: 0, PurgeAfter: time.Hour},
		"negative trim":         {TrimAfter: -time.Hour},
		"purge before the trim": {TrimAfter: 30 * 24 * time.Hour, PurgeAfter: 24 * time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			if err := p.Validate(); err == nil {
				t.Errorf("%+v was accepted", p)
			}
		})
	}
	ok := postgres.Retention{TrimAfter: 30 * 24 * time.Hour, PurgeAfter: 365 * 24 * time.Hour}
	if err := ok.Validate(); err != nil {
		t.Errorf("the default policy is refused: %v", err)
	}
}

// Every run status is on one side of the retention line or the other.
//
// This is the test that makes deriving the SQL list from the domain worth
// anything: a new status arrives with no opinion about whether it is safe to
// delete, and somebody has to give it one.
func TestEveryRunStatusIsClassified(t *testing.T) {
	all := dom.RunStatuses()
	if len(all) < 5 {
		t.Fatalf("the domain reports %d run statuses, which cannot be right: %v", len(all), all)
	}
	settled := map[dom.Status]bool{}
	for _, s := range dom.Settled() {
		settled[s] = true
	}
	for _, s := range all {
		if settled[s] == s.InFlight() {
			t.Errorf("%q is both settled and in flight, or neither", s)
		}
	}
	// And the two that must never move, spelled out: a queued run is owned by
	// somebody, and a finished one is not.
	if dom.StatusQueued.InFlight() != true || dom.StatusSuccess.InFlight() != false {
		t.Error("the two anchors of this classification moved")
	}
}
