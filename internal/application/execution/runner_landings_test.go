package execution_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/execution"
)

// speakerThatFails prints its lines and then exits non-zero.
type speakerThatFails struct{ lines []string }

func (speakerThatFails) Name() string                         { return "falha" }
func (speakerThatFails) Cancel(context.Context, string) error { return nil }

func (f speakerThatFails) Execute(context.Context, execution.TaskExec) (<-chan execution.Event, error) {
	ch := make(chan execution.Event, len(f.lines)+2)
	ch <- execution.Event{Kind: execution.EventStarted}
	for _, l := range f.lines {
		ch <- execution.Event{Kind: execution.EventLog, NodeID: "collectOutput", Stream: "stdout", Message: l}
	}
	ch <- execution.Event{Kind: execution.EventFailed, ExitCode: 1, Err: fmt.Errorf("exit status 1")}
	close(ch)
	return ch, nil
}

func TestEveryDeclaredLandingReachesThePersister(t *testing.T) {
	spy := &spyPersister{}
	r := runnerSpeaking(spy, sdkSpeaker{lines: []string{
		"writing three tables",
		`@brevis:{"type":"landed","target":"bigquery://acme-prod/silver/orders","rows":10}`,
		`@brevis:{"type":"landed","target":"bigquery://acme-prod/silver/items","rows":40}`,
		`@brevis:{"type":"landed","target":"bigquery://acme-prod/silver/orders","rows":5}`,
		`@brevis:{"type":"landed","target":"s3://acme-archive/orders/"}`,
	}})
	if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := spy.landed()
	if len(got) != 1 {
		t.Fatalf("landings recorded for %d steps, want 1: %+v", len(got), got)
	}
	for _, ls := range got {
		if len(ls) != 3 {
			t.Fatalf("landings = %+v, want three targets", ls)
		}
		if ls[0].Target != "bigquery://acme-prod/silver/orders" || *ls[0].Rows != 15 {
			t.Errorf("first landing = %+v, want orders with 15 rows", ls[0])
		}
		if ls[2].Rows != nil {
			t.Errorf("the archive declared no rows, and got %d", *ls[2].Rows)
		}
	}
}

func TestAStepThatLandsNothingRecordsNothing(t *testing.T) {
	spy := &spyPersister{}
	r := runnerSpeaking(spy, sdkSpeaker{lines: []string{"copying files", "done"}})
	if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := spy.landed(); len(got) != 0 {
		t.Fatalf("landings = %+v, want none", got)
	}
}

// A step that wrote a table and then failed did write the table. The run's
// failure is on the calendar; the rows are in the warehouse, and the catalog
// says so.
func TestAStepThatLandsAndThenFailsStillRecordsItsLandings(t *testing.T) {
	spy := &spyPersister{}
	r := runnerSpeaking(spy, speakerThatFails{lines: []string{
		`@brevis:{"type":"landed","target":"postgres://analytics/public/orders","rows":7}`,
		"Traceback (most recent call last): ...",
	}})
	_ = r.Run(context.Background(), oneStepWorkflow())

	got := spy.landed()
	if len(got) != 1 {
		t.Fatalf("landings recorded for %d steps, want 1", len(got))
	}
}

func runnerSpeaking(spy *spyPersister, ex execution.Executor) app.Runner {
	return app.Runner{RunID: uuid.New(), Persist: spy, Report: &spyReporter{}, Processo: ex}
}

// Every landing of an attempt carries the configuration the pipeline
// described: what wrote this table, and how.
func TestLandingsCarryThePipelinesConfiguration(t *testing.T) {
	spy := &spyPersister{}
	r := runnerSpeaking(spy, sdkSpeaker{lines: []string{
		`@brevis:{"type":"pipeline","v":1,"fields":[{"path":"target.dedup","value":"merge"}]}`,
		`@brevis:{"type":"landed","target":"bigquery://acme-prod/silver/orders","rows":10}`,
		`@brevis:{"type":"landed","target":"bigquery://acme-prod/silver/items","rows":4}`,
	}})
	if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, ls := range spy.landed() {
		for _, l := range ls {
			if l.Config == nil || l.Config.Hash == "" {
				t.Fatalf("landing %s has no configuration", l.Target)
			}
		}
	}
}

// A step that did not describe itself -- Python, dbt, a shell -- lands as it
// always did.
func TestLandingsWithoutADescriptionHaveNoConfiguration(t *testing.T) {
	spy := &spyPersister{}
	r := runnerSpeaking(spy, sdkSpeaker{lines: []string{
		`@brevis:{"type":"landed","target":"bigquery://acme-prod/silver/orders","rows":10}`,
	}})
	if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, ls := range spy.landed() {
		if ls[0].Config != nil {
			t.Fatalf("config = %+v, want none", ls[0].Config)
		}
	}
}

// landingsFail is a persister whose landings never reach the database.
type landingsFail struct{ *spyPersister }

func (landingsFail) RecordLandings(context.Context, uuid.UUID, dom.StepKey, string, []app.Landing) error {
	return errors.New(`ERROR: null character not permitted (SQLSTATE 54000)`)
}

// A landing that could not be recorded is a destination missing from /data,
// and the step that wrote it still succeeded -- nothing else would say so. A
// bug in this very path once made every landing carrying a configuration
// fail, and the runner discarded the error. It now says it, once, naming
// the run, the step, how many destinations were lost, and why.
func TestALandingThatCouldNotBeRecordedIsLogged(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	r := app.Runner{RunID: uuid.New(), Persist: landingsFail{&spyPersister{}}, Report: &spyReporter{}, Processo: sdkSpeaker{lines: []string{
		`@brevis:{"type":"landed","target":"bigquery://acme-prod/silver/orders","rows":10}`,
		`@brevis:{"type":"landed","target":"bigquery://acme-prod/silver/items","rows":4}`,
	}}}
	if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
		t.Fatalf("the run failed because a landing was not recorded: %v", err)
	}

	out := logs.String()
	if n := strings.Count(out, "could not record"); n != 1 {
		t.Fatalf("%d warnings, want one:\n%s", n, out)
	}
	for _, want := range []string{"level=WARN", r.RunID.String(), "workflow=clima", "step=collectOutput", "targets=2", "SQLSTATE 54000"} {
		if !strings.Contains(out, want) {
			t.Errorf("the warning lacks %q:\n%s", want, out)
		}
	}
}

// persistFails is a persister whose phases and load numbers never reach the
// database.
type persistFails struct{ *spyPersister }

func (persistFails) RecordStages(context.Context, uuid.UUID, dom.StepKey, int, string, json.RawMessage) error {
	return errors.New("stages: connection refused")
}

func (persistFails) RecordLoad(context.Context, uuid.UUID, dom.StepKey, string, app.LoadNumbers) error {
	return errors.New("load: connection refused")
}

// The phases and the load trend fail as quietly as landings did. Each says
// so -- and the phases, recorded on EVERY marked line, say it once per step:
// a warning per line would bury the log it is trying to be read in.
func TestPhasesAndLoadNumbersThatCouldNotBeRecordedAreLoggedOnce(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	r := app.Runner{RunID: uuid.New(), Persist: persistFails{&spyPersister{}}, Report: &spyReporter{}, Processo: sdkSpeaker{lines: []string{
		`@brevis:{"type":"stage","index":0,"name":"extract","state":"running"}`,
		`@brevis:{"type":"stage","index":0,"name":"extract","state":"done","ms":10}`,
		`@brevis:{"type":"stage","index":1,"name":"load","state":"running"}`,
		`@brevis:{"type":"stage","index":1,"name":"load","state":"done","ms":20,"rows":99,"records":99}`,
	}}}
	if err := r.Run(context.Background(), oneStepWorkflow()); err != nil {
		t.Fatalf("the run failed because its phases were not recorded: %v", err)
	}

	out := logs.String()
	if n := strings.Count(out, "could not record a step's phases"); n != 1 {
		t.Errorf("%d warnings for the phases over four marked lines, want one:\n%s", n, out)
	}
	if n := strings.Count(out, "could not record a step's load"); n != 1 {
		t.Errorf("%d warnings for the load, want one:\n%s", n, out)
	}
	for _, want := range []string{r.RunID.String(), "step=collectOutput", "stages: connection refused", "load: connection refused"} {
		if !strings.Contains(out, want) {
			t.Errorf("the warnings lack %q:\n%s", want, out)
		}
	}
}
