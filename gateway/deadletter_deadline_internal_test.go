package gateway

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
)

// blocks until its context is done, which is how a sink times out.
type blockingSink struct{}

func (blockingSink) Describe() string { return "blocking" }
func (blockingSink) Write(ctx context.Context, _ []sdk.Envelope) (int64, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

// recordingDead is a dead letter that behaves like a real one: it CHECKS its
// context before writing. GCS does, which is why the reported incident ended
// in "the dead letter refused them too, and they are lost".
type recordingDead struct {
	mu       sync.Mutex
	got      []sdk.Envelope
	refused  error
	deadline time.Time
	hadOne   bool
}

func (d *recordingDead) Describe() string { return "recording" }
func (d *recordingDead) Write(ctx context.Context, b []sdk.Envelope) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadline, d.hadOne = ctx.Deadline()
	if err := ctx.Err(); err != nil {
		d.refused = err
		return 0, err
	}
	d.got = append(d.got, b...)
	return int64(len(b)), nil
}

// The dead letter must not inherit the deadline that just expired.
//
// Issue #42, and the reporter's measurement was exact: the pod had a 380 s
// grace period and gave up after 60 s, because `send` narrows its context to
// sendTimeout and then hands THAT context to `bury`. When the batch failed
// BECAUSE the 60 s ran out, the dead letter write started with nothing left
// and failed instantly. 41 events were neither loaded nor parked.
//
// The dead letter is the last place an event can survive. Giving it the
// context of the operation that failed makes it useless in the one case it
// exists for.
func TestTheDeadLetterDoesNotInheritTheExpiredDeadline(t *testing.T) {
	restore := sendTimeout
	sendTimeout = 50 * time.Millisecond
	t.Cleanup(func() { sendTimeout = restore })

	dead := &recordingDead{}
	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 1, Backoff: time.Millisecond}},
		sink:    blockingSink{},
		dead:    dead,
		metrics: NewMetrics(),
	}

	batch := []sdk.Envelope{{Payload: map[string]any{"id": "e-1"}}}
	if err := p.send(context.Background(), batch); err == nil {
		t.Fatal("the blocking sink reported success")
	}

	dead.mu.Lock()
	defer dead.mu.Unlock()
	if dead.refused != nil {
		t.Fatalf("the dead letter was handed a dead context (%v): it ran on "+
			"the deadline the send had just exhausted, which is the one case "+
			"it exists for", dead.refused)
	}
	if len(dead.got) != 1 {
		t.Fatalf("the dead letter received %d events, want 1", len(dead.got))
	}

	// And BOUNDED. A dead letter with no deadline at all can hold the drain
	// open past the pod's grace period, and then SIGKILL takes the write
	// anyway — the loss this is meant to prevent, arriving by another road.
	if !dead.hadOne {
		t.Error("the dead letter ran with no deadline: a hanging write would " +
			"hold the drain past the grace period")
	}
	if left := time.Until(dead.deadline); left > deadLetterTimeout+time.Second {
		t.Errorf("the dead letter's budget is %s, which is more than the %s it "+
			"is allowed", left, deadLetterTimeout)
	}
}

// The shipped budget is short against any grace period worth setting.
//
// A LITERAL and not the variable, which the test above compares against: a
// mutation raising deadLetterTimeout to thirty minutes moved both sides of
// that comparison and survived. A bound asserted against itself is not a
// bound.
func TestTheDeadLetterBudgetIsShort(t *testing.T) {
	if deadLetterTimeout > time.Minute {
		t.Errorf("deadLetterTimeout is %s: a dead letter that can run that "+
			"long holds the drain open past the pod's grace period, and then "+
			"SIGKILL takes the write anyway", deadLetterTimeout)
	}
}

// The dead letter's budget comes from the CALLER's context, not from nothing.
//
// Every caller passes context.Background() today, so a `bury` that built its
// deadline from Background directly would behave identically — and survived
// the tests above for exactly that reason. This one hands `send` a context
// that HAS a deadline, which is the only way to tell the two apart, and the
// day a caller passes the drain's budget it is already covered.
func TestTheDeadLetterBudgetComesFromTheCaller(t *testing.T) {
	restoreSend, restoreDead := sendTimeout, deadLetterTimeout
	sendTimeout, deadLetterTimeout = 50*time.Millisecond, 10*time.Second
	t.Cleanup(func() { sendTimeout, deadLetterTimeout = restoreSend, restoreDead })

	parent, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	dead := &recordingDead{}
	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 1, Backoff: time.Millisecond}},
		sink:    blockingSink{},
		dead:    dead,
		metrics: NewMetrics(),
	}
	_ = p.send(parent, []sdk.Envelope{{Payload: map[string]any{"id": "e-1"}}})

	dead.mu.Lock()
	defer dead.mu.Unlock()
	if !dead.hadOne {
		t.Fatal("the dead letter ran with no deadline")
	}
	if left := time.Until(dead.deadline); left > 3*time.Second {
		t.Errorf("the dead letter has %s, and the caller's context had 2s: "+
			"its budget was built from nothing rather than from what the "+
			"caller still had", left)
	}
}

// A dead letter that genuinely refuses still says so, and that line must stay
// reachable: it is the only signal that events were lost.
func TestADeadLetterThatRefusesIsStillReported(t *testing.T) {
	restoreSend, restoreDead := sendTimeout, deadLetterTimeout
	sendTimeout, deadLetterTimeout = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { sendTimeout, deadLetterTimeout = restoreSend, restoreDead })

	// A dead letter that blocks: its own deadline is what stops this from
	// hanging, and a bound that is never reached is a bound nobody can trust.
	p := &pipe{
		stream:  Stream{Name: "ingestion", Retry: Retry{Attempts: 1, Backoff: time.Millisecond}},
		sink:    blockingSink{},
		dead:    blockingSink{},
		metrics: NewMetrics(),
	}
	start := time.Now()
	if err := p.send(context.Background(), []sdk.Envelope{{Payload: map[string]any{"id": "x"}}}); err == nil {
		t.Fatal("the blocking sink reported success")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("send took %s: the dead letter write is not bounded, and a "+
			"hanging one holds the drain past the pod's grace period", took)
	}
}
