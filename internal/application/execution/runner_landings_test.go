package execution_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	app "github.com/AreteAcademy/brevis/internal/application/execution"
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
