package alerts_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/alerts"
	dom "github.com/AreteAcademy/brevis/internal/domain/run"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/notify"
)

func testDB(t *testing.T) *postgres.Pool {
	t.Helper()
	url := os.Getenv("BREVIS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BREVIS_TEST_DATABASE_URL to run the outbox tests (make up)")
	}
	p, err := postgres.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if _, err := p.Exec(context.Background(),
		`TRUNCATE alertas, queue_items, task_runs, runs, schedules, workflows, projects CASCADE`); err != nil {
		t.Fatal(err)
	}
	return p
}

func noLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// channel is a destination that can be told to fail, for as long as the test
// wants it to.
type channel struct {
	mu       sync.Mutex
	received []notify.Alert
	failFor  int // fail this many times, then succeed
	err      error
}

func (c *channel) Failed(_ context.Context, a notify.Alert) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failFor > 0 {
		c.failFor--
		return c.err
	}
	c.received = append(c.received, a)
	return nil
}

func (c *channel) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.received)
}

// raise writes one alert the way the dispatcher does, in a transaction.
func raise(t *testing.T, pool *postgres.Pool, chName string) (uuid.UUID, *alerts.Outbox) {
	t.Helper()
	ctx := context.Background()
	repo := postgres.NewRunRepo(pool)
	r, err := repo.Create(ctx, dom.Run{
		WorkflowSlug: "id_verification", IdempotencyKey: uuid.NewString(),
		TriggerType: "schedule", Definition: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Attempt with a budget of 1 gives up immediately, which is what writes
	// the row -- through the same path production uses, not a hand-built
	// INSERT.
	if _, _, err := repo.Attempt(ctx, r.ID, 1, func(attempt int) *alerts.Pending {
		return &alerts.Pending{
			RunID: r.ID, Kind: alerts.KindRun, Channel: chName,
			Payload: notify.Alert{
				Workflow: "id_verification", RunID: r.ID.String(),
				Status: "failed", Attempts: attempt, Err: "exited with code 2",
			},
		}
	}); err != nil {
		t.Fatal(err)
	}
	return r.ID, alerts.New(pool.Pool)
}

func state(t *testing.T, o *alerts.Outbox, runID uuid.UUID) alerts.Record {
	t.Helper()
	rows, err := o.ForRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows for the run, wanted 1", len(rows))
	}
	return rows[0]
}

func drain(t *testing.T, d *alerts.Deliverer, until func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if until() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the deliverer never reached the state the test needs")
}

// TestAnAlertSurvivesSlackBeingDown is the defect this whole feature exists for.
//
// Before the outbox, a failing webhook meant a log line and an alert that was
// simply gone: no retry, no record, nothing on a screen to say anybody should
// have been told. Here the channel refuses twice and the alert still arrives.
func TestAnAlertSurvivesSlackBeingDown(t *testing.T) {
	pool := testDB(t)
	runID, outbox := raise(t, pool, alerts.ChannelSlack)

	ch := &channel{failFor: 2, err: errors.New("slack respondeu 503")}
	d := alerts.NewDeliverer(alerts.Config{
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond, MaxAttempts: 5,
	}, outbox, map[string]notify.Notificador{alerts.ChannelSlack: ch}, noLog())

	drain(t, d, func() bool { return ch.count() > 0 })

	row := state(t, outbox, runID)
	if row.State() != "delivered" {
		t.Errorf("the alert arrived but the row reads %q", row.State())
	}
	if row.Attempts != 3 {
		t.Errorf("attempts = %d; two refusals and one success is 3", row.Attempts)
	}
	// The error from the failed tries is cleared: a row that succeeded on the
	// third try must not still show the second try's failure.
	if row.Err != "" {
		t.Errorf("a delivered alert still carries an error: %q", row.Err)
	}
}

// TestItGivesUpAndKeepsTheRow.
//
// "Raised, not delivered, 4 attempts, 403 from Slack" is the most useful row
// this table produces: it is the case where somebody is waiting for a message
// that is not coming. Deleting it would make that indistinguishable from an
// alert nobody ever raised.
func TestItGivesUpAndKeepsTheRow(t *testing.T) {
	pool := testDB(t)
	runID, outbox := raise(t, pool, alerts.ChannelSlack)

	ch := &channel{failFor: 1000, err: errors.New("slack respondeu 403: invalid_token")}
	d := alerts.NewDeliverer(alerts.Config{
		Interval: 10 * time.Millisecond, BackoffBase: time.Millisecond, MaxAttempts: 3,
	}, outbox, map[string]notify.Notificador{alerts.ChannelSlack: ch}, noLog())

	drain(t, d, func() bool { return state(t, outbox, runID).State() == "undelivered" })

	row := state(t, outbox, runID)
	if row.Attempts != 3 {
		t.Errorf("attempts = %d, wanted the configured 3", row.Attempts)
	}
	if row.Err == "" {
		t.Error("it gave up without saying why, which is the half that matters")
	}
	// And the message itself survives, so the screen can show what was never
	// sent rather than only that something was not.
	if row.Payload.Workflow != "id_verification" {
		t.Errorf("the payload was lost: %+v", row.Payload)
	}
}

// TestAnAlertSurvivesTheDelivererDying. Killing the process mid-claim must not
// strand the row: without the visibility sweep it stays claimed forever, and
// the table says "in progress" about an alert nobody will ever send.
func TestAnAlertSurvivesTheDelivererDying(t *testing.T) {
	pool := testDB(t)
	runID, outbox := raise(t, pool, alerts.ChannelSlack)
	ctx := context.Background()

	// Claimed by a process that then dies.
	claimed, err := outbox.Claim(ctx, "the-one-that-died", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d alerts, wanted 1", len(claimed))
	}
	// Nobody else can take it while it is held.
	again, err := outbox.Claim(ctx, "another", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("a claimed alert was handed out again")
	}

	n, err := outbox.Recover(ctx, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the sweep recovered %d, wanted 1", n)
	}

	ch := &channel{}
	d := alerts.NewDeliverer(alerts.Config{
		Interval: 10 * time.Millisecond,
	}, outbox, map[string]notify.Notificador{alerts.ChannelSlack: ch}, noLog())
	drain(t, d, func() bool { return ch.count() > 0 })

	if s := state(t, outbox, runID).State(); s != "delivered" {
		t.Errorf("after the sweep the alert reads %q", s)
	}
}

// TestAnUnknownChannelIsNotRetriedForever.
//
// A row naming a destination this build cannot reach gives the same answer
// every time, so retrying it six times over five minutes buys nothing and
// delays every alert behind it. It should be unreachable -- publish refuses an
// unknown channel -- and it is handled anyway, because a row outlives the
// binary that wrote it.
func TestAnUnknownChannelIsNotRetriedForever(t *testing.T) {
	pool := testDB(t)
	runID, outbox := raise(t, pool, "TEAMS")

	ch := &channel{}
	d := alerts.NewDeliverer(alerts.Config{
		Interval: 10 * time.Millisecond, MaxAttempts: 50,
	}, outbox, map[string]notify.Notificador{alerts.ChannelSlack: ch}, noLog())

	drain(t, d, func() bool { return state(t, outbox, runID).State() == "undelivered" })

	row := state(t, outbox, runID)
	if row.Attempts != 1 {
		t.Errorf("attempts = %d: an undeliverable channel was retried", row.Attempts)
	}
	// The error names what IS valid. "Unknown channel" alone sends the reader
	// to the source.
	if !strings.Contains(row.Err, alerts.ChannelSlack) {
		t.Errorf("the error does not say what is valid: %q", row.Err)
	}
	if ch.count() != 0 {
		t.Error("it was delivered to Slack, which is not what the row asked for")
	}
}

// TestTheOutboxDepthComesFromTheTable backs the metric, and the two states have
// to be told apart: alerts still trying are patience, alerts given up on are an
// incident.
func TestTheOutboxDepthComesFromTheTable(t *testing.T) {
	pool := testDB(t)
	ctx := context.Background()
	_, outbox := raise(t, pool, alerts.ChannelSlack)
	_, _ = raise(t, pool, alerts.ChannelSlack)

	waiting, undelivered, err := outbox.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if waiting != 2 || undelivered != 0 {
		t.Fatalf("waiting=%d undelivered=%d, wanted 2 and 0", waiting, undelivered)
	}

	claimed, _ := outbox.Claim(ctx, "w", 1)
	if err := outbox.GaveUp(ctx, claimed[0].ID, errors.New("403")); err != nil {
		t.Fatal(err)
	}
	waiting, undelivered, err = outbox.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if waiting != 1 || undelivered != 1 {
		t.Errorf("waiting=%d undelivered=%d, wanted 1 and 1", waiting, undelivered)
	}
}

// TestAKnownChannelIsRefusedAtPublishTime pins the vocabulary itself. Adding a
// name here without an implementation in the deliverer is how a workflow gets
// to declare a destination that goes nowhere.
func TestOnlyImplementedChannelsAreOffered(t *testing.T) {
	if !alerts.KnownChannel(alerts.ChannelSlack) {
		t.Error("SLACK is not in the vocabulary")
	}
	if alerts.KnownChannel("TEAMS") {
		t.Error("TEAMS is offered and nothing delivers to it")
	}
	if len(alerts.Channels()) == 0 {
		t.Error("no channel is offered at all")
	}
}
