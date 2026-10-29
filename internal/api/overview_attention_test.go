package api_test

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// failingList answers the failing workflows, as RunRepo would.
type failingList struct{ list []postgres.FailingWorkflow }

func (f failingList) FailingWorkflows(context.Context) ([]postgres.FailingWorkflow, error) {
	return f.list, nil
}
func (f failingList) FailureStreak(context.Context, string) (int, error) { return 0, nil }

// attentionExecs answers each failed run and its logs, and counts the logs
// read.
type attentionExecs struct {
	execsFake
	runs  map[uuid.UUID]dom.Run
	logs  map[uuid.UUID][]postgres.StepLog
	reads *int
}

func (e attentionExecs) Get(_ context.Context, id uuid.UUID) (dom.Run, error) {
	if r, ok := e.runs[id]; ok {
		return r, nil
	}
	return dom.Run{}, pgx.ErrNoRows
}
func (e attentionExecs) LogsDaRun(_ context.Context, id uuid.UUID) ([]postgres.StepLog, error) {
	*e.reads++
	return e.logs[id], nil
}

type failed struct {
	slug   string
	ago    time.Duration
	streak int
	log    string
}

func attentionUI(t *testing.T, fs ...failed) (*api.UI, *int) {
	t.Helper()
	reads := 0
	e := attentionExecs{runs: map[uuid.UUID]dom.Run{}, logs: map[uuid.UUID][]postgres.StepLog{}, reads: &reads}
	var list []postgres.FailingWorkflow
	for _, f := range fs {
		id := uuid.New()
		list = append(list, postgres.FailingWorkflow{Slug: f.slug, RunID: id, FailedAt: time.Now().Add(-f.ago), Streak: f.streak})
		e.runs[id] = dom.Run{ID: id, WorkflowSlug: f.slug, Status: dom.StatusFailed}
		code := 1
		e.logs[id] = []postgres.StepLog{{NodeID: "test", MapIndex: dom.Unmapped, Status: "failed", ExitCode: &code, Log: f.log}}
	}
	ui := api.NewUI(overviewReads{asked: &struct {
		span  time.Duration
		grain postgres.Grain
		n     int
	}{}}, nil, e, nil, nil, nil, branding.Default(), slog.New(slog.DiscardHandler)).WithFailing(failingList{list})
	return ui, &reads
}

func attentionOf(t *testing.T, body string) string {
	t.Helper()
	at := strings.Index(body, "data-attention")
	if at < 0 {
		t.Fatal("no attention section")
	}
	end := strings.Index(body[at:], "</section>")
	return body[at : at+end]
}

// WHAT NEEDS YOU COMES FIRST: each workflow whose last run failed, the ones
// failing again and again before the rest, each with why -- the step, how it
// exited, the line -- read from the run's log as its own page reads it.
func TestTheOverviewOpensWithWhatNeedsYou(t *testing.T) {
	ui, _ := attentionUI(t,
		failed{"environment_alert", 5 * time.Minute, 1, "1 of 2 PASS a\nError: not_null_alerts_id failed\n"},
		failed{"vendors_alert_ipma", time.Hour, 3, "Timeout of 60s fetching https://api.ipma.pt\n"},
	)
	_, body := get(t, ui, "/")
	if strings.Index(body, "data-attention") > strings.Index(body, "data-overview-meta") {
		t.Error("what needs you is not the first section")
	}
	att := attentionOf(t, body)
	if strings.Index(att, "vendors_alert_ipma") > strings.Index(att, "environment_alert") {
		t.Error("a workflow failing again and again is not first")
	}
	for _, want := range []string{
		"Failed 3 times in a row", "Step test failed (exit 1)", "Error: not_null_alerts_id failed",
		"failed 5 min ago", `#logs"`,
	} {
		if !strings.Contains(att, want) {
			t.Errorf("the attention lacks %q", want)
		}
	}
	if n := regexp.MustCompile(`data-attention-item=`).FindAllStringIndex(att, -1); len(n) != 2 {
		t.Errorf("%d items; wanted 2", len(n))
	}
}

func TestASecondFailureSaysSo(t *testing.T) {
	ui, _ := attentionUI(t, failed{"daily", time.Minute, 2, "Error: x\n"})
	_, body := get(t, ui, "/")
	if !strings.Contains(attentionOf(t, body), "2nd time in a row") {
		t.Error("a second failure in a row does not say so")
	}
}

// EIGHT, AND HOW MANY MORE. Each item's cause is a log read, so the section
// reads eight and counts the rest, with the way to all of them.
func TestTheAttentionShowsEightAndCountsTheRest(t *testing.T) {
	var fs []failed
	for i := 0; i < 11; i++ {
		fs = append(fs, failed{fmt.Sprintf("wf_%02d", i), time.Duration(i+1) * time.Minute, 1, "Error: x\n"})
	}
	ui, reads := attentionUI(t, fs...)
	_, body := get(t, ui, "/")
	att := attentionOf(t, body)
	if n := strings.Count(att, "data-attention-item="); n != 8 {
		t.Errorf("%d items; wanted 8", n)
	}
	if *reads != 8 {
		t.Errorf("%d logs read; wanted 8", *reads)
	}
	if !strings.Contains(att, `href="/runs?state=failed"`) || !strings.Contains(att, "and 3 more failing") {
		t.Error("the rest is not counted, with the way to it")
	}
	if !strings.Contains(att, ">11<") {
		t.Error("the section does not count every failing workflow")
	}
}

func TestNothingNeedsYou(t *testing.T) {
	ui, _ := attentionUI(t)
	_, body := get(t, ui, "/")
	att := attentionOf(t, body)
	if !strings.Contains(att, "Nothing needs you right now") || strings.Contains(att, "data-attention-item") {
		t.Error("an empty attention does not say all is well")
	}
}
