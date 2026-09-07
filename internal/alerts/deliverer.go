package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/AreteAcademy/brevis/internal/notify"
	"github.com/AreteAcademy/brevis/internal/observability/metrics"
)

// Config parameterises the delivery loop.
type Config struct {
	Worker   string
	Interval time.Duration

	// Batch is how many alerts one cycle claims. Small on purpose: delivery is
	// serial inside a batch, and a large claim would hold rows out of reach of
	// another process for as long as the whole batch takes.
	Batch int

	// MaxAttempts before the alert is marked undelivered. It is not infinite,
	// and that is a decision rather than a limitation: a webhook revoked in
	// January must not still be generating traffic in March, and a row that
	// says "gave up, 403" is what tells somebody to fix it.
	MaxAttempts int
	BackoffBase time.Duration

	// Visibility is how long a claimed alert may sit before it counts as
	// orphaned. Short, because delivering one message is an HTTP request --
	// unlike a run, which can legitimately take an hour.
	Visibility       time.Duration
	RecoveryInterval time.Duration
}

func (c *Config) defaults() {
	if c.Worker == "" {
		c.Worker = "alert"
	}
	if c.Interval <= 0 {
		c.Interval = time.Second
	}
	if c.Batch <= 0 {
		c.Batch = 10
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 6
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 5 * time.Second
	}
	if c.Visibility <= 0 {
		c.Visibility = 2 * time.Minute
	}
	if c.RecoveryInterval <= 0 {
		c.RecoveryInterval = time.Minute
	}
}

// Deliverer drains the outbox.
//
// One process, and not one per dispatcher: Slack's rate limit is per workspace,
// so N senders competing for it turns a burst of failures into a second
// incident. This is also why a batch is delivered SERIALLY -- the parallelism
// that would be free here is exactly the parallelism that gets an installation
// throttled.
type Deliverer struct {
	cfg      Config
	outbox   *Outbox
	channels map[string]notify.Notificador
	log      *slog.Logger

	// Metrics is optional, as everywhere else.
	Metrics *metrics.Metrics
}

// NewDeliverer builds the loop. `channels` maps a channel name to what talks to
// it; a name absent from the map is a permanent failure rather than a retry.
func NewDeliverer(cfg Config, o *Outbox, channels map[string]notify.Notificador, log *slog.Logger) *Deliverer {
	cfg.defaults()
	return &Deliverer{cfg: cfg, outbox: o, channels: channels, log: log}
}

// Run drains until the context is cancelled.
func (d *Deliverer) Run(ctx context.Context) error {
	tick := time.NewTicker(d.cfg.Interval)
	defer tick.Stop()
	recovery := time.NewTicker(d.cfg.RecoveryInterval)
	defer recovery.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := d.cycle(ctx); err != nil {
				d.log.Error("delivery cycle", "error", err)
			}
		case <-recovery.C:
			n, err := d.outbox.Recover(ctx, d.cfg.Visibility)
			if err != nil {
				d.log.Error("recovering alerts", "error", err)
			} else if n > 0 {
				d.log.Warn("alerts returned by the visibility sweep", "quantity", n)
			}
		}
	}
}

func (d *Deliverer) cycle(ctx context.Context) error {
	items, err := d.outbox.Claim(ctx, d.cfg.Worker, d.cfg.Batch)
	if err != nil {
		return err
	}
	for _, it := range items {
		if ctx.Err() != nil {
			// Shutting down. The rest stay claimed and the visibility sweep
			// frees them; delivering half a batch against a cancelled context
			// would fail every one of them and burn their attempts.
			return nil
		}
		d.deliver(ctx, it)
	}
	return nil
}

func (d *Deliverer) deliver(ctx context.Context, it Item) {
	channel, ok := d.channels[it.Channel]
	if !ok || channel == nil {
		// A channel this build cannot deliver to is a PERMANENT failure, not a
		// transient one. Retrying it six times over five minutes would produce
		// the same answer six times and delay every alert behind it; the row
		// says what happened and a human fixes the workflow or the
		// installation.
		//
		// It should be unreachable: `brevis publish` refuses an unknown channel.
		// It is handled anyway because a row outlives the binary that wrote it,
		// and a build that dropped a channel would otherwise loop on history.
		err := fmt.Errorf("channel %q is not configured in this installation (valid: %v)",
			it.Channel, Channels())
		d.log.Error("undeliverable alert", "alert", it.ID, "run", it.RunID, "error", err)
		if err := d.outbox.GaveUp(ctx, it.ID, err); err != nil {
			d.log.Error("marking the alert undelivered", "alert", it.ID, "error", err)
		}
		d.Metrics.AlertUndelivered(ctx, it.Channel)
		return
	}

	// A context of its own, and short. The loop's may be cancelled -- a
	// shutdown -- and an alert half-delivered because the process was leaving
	// is one that gets retried against a webhook that already received it.
	send, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	if err := channel.Failed(send, it.Payload); err != nil {
		attempts := it.Attempts + 1
		if attempts >= d.cfg.MaxAttempts {
			d.log.Error("giving up on the alert", "alert", it.ID, "run", it.RunID,
				"attempts", attempts, "error", err)
			if err := d.outbox.GaveUp(ctx, it.ID, err); err != nil {
				d.log.Error("marking the alert undelivered", "alert", it.ID, "error", err)
			}
			d.Metrics.AlertUndelivered(ctx, it.Channel)
			return
		}
		delay := d.cfg.BackoffBase * time.Duration(1<<uint(it.Attempts))
		d.log.Warn("alert not delivered; will retry", "alert", it.ID, "run", it.RunID,
			"attempt", attempts, "delay", delay, "error", err)
		if err := d.outbox.Retry(ctx, it.ID, err, delay); err != nil {
			d.log.Error("handing the alert back", "alert", it.ID, "error", err)
		}
		return
	}

	if err := d.outbox.Delivered(ctx, it.ID); err != nil {
		// The message ARRIVED and the row could not be marked. The visibility
		// sweep will hand it back and it will be sent twice -- which is the
		// right way round: a duplicate alert is noise, a missing one is an
		// outage nobody hears about.
		d.log.Error("the alert was delivered but not marked; it may be sent again",
			"alert", it.ID, "error", err)
		return
	}
	d.Metrics.AlertDelivered(ctx, it.Channel, it.Attempts+1)
}
