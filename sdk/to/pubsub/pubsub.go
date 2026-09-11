// Package pubsub publishes records to a Google Cloud Pub/Sub topic.
//
// # The contract is the client's
//
// The message is the payload, and nothing else. No attribute is added, no key
// is put into the body, and there is no Brevis-shaped envelope anywhere in what
// a subscriber receives.
//
// That is the rule this package is built around, and it is not minimalism. A
// table is created by the pipeline that writes it, so the SDK may decide what
// its columns are. A topic is not: it exists before this pipeline does, its
// subscribers were written first and their filters were written first. An
// attribute Brevis adds on its own is Brevis editing somebody else's contract,
// and the subscriber finds out at three in the morning.
//
// Everything on a message is therefore ASKED FOR, by name, through Attributes
// and OrderingKey. Both default to nothing.
//
// It is the SDK's first destination that is neither a table nor a directory,
// and the difference is not the transport. Half of what a destination is asked
// -- create the table, deduplicate, partition by this column -- has no meaning
// on a topic, and the design decision is to REFUSE those rather than accept and
// ignore them. `to.Files` set that precedent by refusing `DedupMerge` because a
// directory has no key to match on; this refuses three.
//
// # The topic already exists
//
// This never creates one, and that is a rule rather than a missing feature. A
// pipeline that can create a topic can create the wrong one, and unlike a
// mistyped table nobody finds out: the messages go somewhere, and the
// subscriber that should have received them simply stays quiet. Creating a
// topic is an act of infrastructure and it belongs where the rest of the
// infrastructure is declared.
//
// # A publish is not transactional
//
// The property that has no equivalent in any other destination here. Forty-eight
// thousand messages that fail at thirty-one thousand have DELIVERED thirty-one
// thousand, and somebody downstream is already acting on them. So the count
// this reports on the error path is what actually went, and the error says how
// many did and how many did not -- because the operator's next move depends on
// knowing that a re-run is a re-delivery.
package pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gps "cloud.google.com/go/pubsub"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Topic is the destination.
type Topic struct {
	// Project and Name identify the topic. Both are required: a topic is not
	// something to default into, and the cost of guessing wrong is messages
	// arriving somewhere nobody is looking.
	Project string
	Name    string

	// Attributes decides what goes in each message's attributes. NIL MEANS
	// NONE, and that is the default.
	//
	// The topic exists before this pipeline does and its contract belongs to
	// whoever owns it: the subscriber was written first and its filters were
	// written first. An attribute Brevis adds on its own is Brevis editing
	// somebody else's contract -- the same argument this driver makes about the
	// payload, for the same reason.
	//
	// So nothing is added unless it is asked for, by name:
	//
	//	Attributes: func(e sdk.Envelope) map[string]string {
	//		row, _ := e.Payload.(map[string]any)
	//		return map[string]string{"orderId": fmt.Sprint(row["order_id"])}
	//	}
	//
	// `sdk.Envelope` is an alias for the type below, so a consumer writes the
	// public name and never sees an internal package.
	//
	// READ THE PAYLOAD, not the envelope's provenance fields. Provider, Entity,
	// SourceKey and RecordTS are part of Envelope, and NO source in this SDK
	// fills them: from.Files, from.HTTP, from/postgres and from/mysql all yield
	// `{Payload: ...}` and nothing else. They carry values only when a consumer
	// hand-builds envelopes and calls Loader.Load directly.
	//
	// Which means `e.IngestionID()` -- the deterministic UUID v5 over
	// provider|entity|source_key|record_ts -- returns an error on any pipeline
	// with a built-in source, because SourceKey is empty. It is available for
	// the hand-built case, and an example that read it was what found this
	// paragraph missing.
	//
	// A function rather than a map because most of what is worth putting there
	// is per-record. Returning nil is no attributes for that message.
	Attributes func(core.Envelope) map[string]string

	// OrderingKey decides the message's ordering key. Nil means no ordering,
	// and that is the default.
	//
	// A function and not a field name, for the reason above and one more: the
	// field-name version had to invent a lookup -- the envelope first, then the
	// payload -- and a rule the client did not write is a rule the client has
	// to learn. This has none:
	//
	//	OrderingKey: func(e sdk.Envelope) string {
	//		row, _ := e.Payload.(map[string]any)
	//		return fmt.Sprint(row["customer"])
	//	}
	//
	// Ordering costs throughput and constrains publishing, which is why off is
	// the default. It is also what every other destination here gives: a table
	// load has no order either.
	OrderingKey func(core.Envelope) string

	// Client is the publisher, and it exists to be replaced in a test.
	//
	// A publish that fails halfway is the case this driver is built around, and
	// it cannot be produced on demand against a real topic. Nil takes the real
	// one, built from Project on first use.
	Client Publisher
}

// Publisher is the topic as this driver needs it: somewhere to send a message
// and a way to wait for its id.
type Publisher interface {
	// Publish queues a message and returns a handle. It does not block, which
	// is what lets the whole batch go out before any of it is waited on.
	Publish(ctx context.Context, m Message) Handle

	// Stop flushes and releases. Called once, when the batch is done.
	Stop()
}

// Message is one record on its way out.
type Message struct {
	Data        []byte
	Attributes  map[string]string
	OrderingKey string
}

// Handle is a message in flight.
type Handle interface {
	// ID blocks until the server accepted the message, or reports why it did
	// not.
	ID(ctx context.Context) (string, error)
}

// ordered says whether the real publisher must have message ordering switched
// on.
//
// A method rather than the expression inline, because the expression was inline
// and nothing could reach it: every test here uses the fake, and the real
// constructor needs credentials. A mutation that hard-coded this to false
// passed the whole suite -- and in production it would fail every ordered
// publish, because the Google client REFUSES a message with an ordering key on
// a publisher that has ordering off. It does not reorder it; it refuses it.
//
// The honest proof is still the emulator test that is not built yet. This is
// the half that can be checked without one.
func (t Topic) ordered() bool { return t.OrderingKey != nil }

// Describe names the destination for logs and errors.
func (t Topic) Describe() string {
	return "pubsub://" + t.Project + "/" + t.Name
}

// Write publishes one message per record.
//
// One record, one message -- not a batch in one message. A subscriber's unit of
// work is a message, and packing fifty records into one makes every consumer
// downstream unpack a Brevis-shaped envelope it never asked for.
func (t Topic) Write(ctx context.Context, records []core.Envelope, opt core.WriteOptions) (*core.LoadResult, error) {
	start := time.Now()

	if err := t.refuseUnsupported(opt); err != nil {
		return nil, err
	}
	if t.Project == "" || t.Name == "" {
		return nil, fmt.Errorf("to/pubsub.Topic needs Project and Name; a topic is not " +
			"something to default into, and a message that goes to the wrong one is a " +
			"subscriber that stays quiet")
	}
	// The declared columns are checked against what the Transform chain
	// composed, exactly as a table's are: a message missing a field the author
	// declared is the same bug wherever it lands.
	if err := core.CheckRow(opt.Columns, opt.Schema, records); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return &core.LoadResult{Duration: time.Since(start), Strategy: "publish", Format: "json"}, nil
	}

	client := t.Client
	if client == nil {
		real, err := newTopic(ctx, t.Project, t.Name, t.ordered())
		if err != nil {
			return nil, err
		}
		client = real
	}
	defer client.Stop()

	// Every message is queued BEFORE any is waited on. Waiting per message
	// would serialise the whole batch on the round trip and turn a
	// forty-thousand-record load into forty thousand sequential calls.
	handles := make([]Handle, 0, len(records))
	var bytes int64
	for i := range records {
		msg, err := t.message(&records[i])
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", i, err)
		}
		bytes += int64(len(msg.Data))
		handles = append(handles, client.Publish(ctx, msg))
	}

	return t.collect(ctx, handles, bytes, start)
}

// collect waits for every handle and reports what actually went.
//
// EVERY handle is waited on, including after the first failure. Returning early
// would leave the rest in flight with nobody reading their outcome -- and they
// are already on their way, so the count would be a guess in the direction that
// flatters the report.
func (t Topic) collect(ctx context.Context, handles []Handle, bytes int64, start time.Time) (*core.LoadResult, error) {
	res := &core.LoadResult{Strategy: "publish", Format: "json", BytesStaged: bytes}

	var failures []string
	for i, h := range handles {
		if _, err := h.ID(ctx); err != nil {
			// Truncated for the same reason ErrorRows is everywhere else: a
			// batch where everything failed produces one message per record,
			// and a log nobody can read is a log nobody reads.
			if len(failures) < maxReportedFailures {
				failures = append(failures, fmt.Sprintf("message %d: %v", i, err))
			}
			continue
		}
		res.RowsLoaded++
	}
	res.Duration = time.Since(start)
	res.ErrorRows = failures

	if failed := int64(len(handles)) - res.RowsLoaded; failed > 0 {
		// The result comes back WITH the error, which is unusual here and
		// deliberate: the caller needs the count as much as the failure. A
		// publish is not transactional, so the messages that went are
		// delivered, and a re-run is a re-delivery of exactly those.
		return res, fmt.Errorf("published %d of %d messages to %s and %d failed. "+
			"THE %d THAT WENT ARE DELIVERED -- Pub/Sub has no rollback, so re-running "+
			"this step re-delivers them and the subscriber has to be idempotent. "+
			"First failures: %s",
			res.RowsLoaded, len(handles), t.Describe(), failed, res.RowsLoaded,
			strings.Join(failures, "; "))
	}
	return res, nil
}

// maxReportedFailures caps the error text. Every failure is still COUNTED.
const maxReportedFailures = 5

// message turns one record into one message.
//
// THE MESSAGE IS THE PAYLOAD AND NOTHING ELSE, unless the consumer said
// otherwise. No attribute is added on its own and no key is put into the body:
// the topic's contract belongs to whoever owns the topic, and a subscriber
// written before this pipeline existed should not have to learn a shape Brevis
// invented.
func (t Topic) message(e *core.Envelope) (Message, error) {
	data, err := json.Marshal(e.Payload)
	if err != nil {
		return Message{}, fmt.Errorf("the payload is not JSON: %w", err)
	}

	msg := Message{Data: data}
	if t.Attributes != nil {
		// Copied rather than kept. The consumer's function may hand back a map
		// it reuses, and the publisher holds this one for the life of the
		// message -- one buffer reused across records would make every message
		// carry the last one's attributes.
		if given := t.Attributes(*e); len(given) > 0 {
			attrs := make(map[string]string, len(given))
			for k, v := range given {
				attrs[k] = v
			}
			msg.Attributes = attrs
		}
	}
	if t.OrderingKey != nil {
		key := t.OrderingKey(*e)
		if key == "" {
			// Refused rather than sent blank, and this is the one that would be
			// silent otherwise: an empty key is not "no ordering" to Pub/Sub on
			// a publisher that has ordering ON. Every message with an empty key
			// lands in ONE order group, which is a throughput collapse that
			// reads as a slow day.
			return Message{}, fmt.Errorf("OrderingKey returned an empty string for "+
				"source_key %q. On a publisher with ordering enabled that is not "+
				"'no ordering' -- every such message goes into one order group. "+
				"Return a key for every record, or leave OrderingKey nil", e.SourceKey)
		}
		msg.OrderingKey = key
	}
	return msg, nil
}

// refuseUnsupported rejects what a topic cannot honour.
//
// Refused and not ignored, which is the rule this repository already follows.
// An ignored option is worse than an error: the author believes it is being
// enforced, and finds out from the data.
func (t Topic) refuseUnsupported(opt core.WriteOptions) error {
	if len(opt.Schema) > 0 {
		return fmt.Errorf("to/pubsub.Topic cannot use a Schema: a Schema exists so a " +
			"destination can CREATE its table, and a topic exists already. Declare " +
			"Columns instead -- they check the row the Transform chain composed, which " +
			"is as useful here as anywhere")
	}
	if opt.Dedup == core.DedupMerge {
		return fmt.Errorf("to/pubsub.Topic cannot deduplicate: there is no key to match " +
			"on and no row to replace. Pub/Sub is at-least-once by design, so the " +
			"subscriber is where idempotency lives. If it needs a key, put one in the " +
			"attributes yourself, read off the payload -- under whatever name your " +
			"topic's contract already uses")
	}
	if opt.PartitionBy != "" {
		return fmt.Errorf("to/pubsub.Topic has no partitions: PartitionBy is a table's "+
			"idea. The nearest thing here is OrderingKey, which is a different one -- it "+
			"groups messages that must arrive in order, not rows that live together "+
			"(you asked for %q)", opt.PartitionBy)
	}
	return nil
}

// topic is the real Publisher, wrapping the Google client.
//
// Thin on purpose: everything that decides behaviour is above and is tested
// against a fake, because the case that matters -- a publish that fails part of
// the way through -- cannot be produced on demand against a real topic.
type topic struct {
	client *gps.Client
	t      *gps.Topic
}

func newTopic(ctx context.Context, project, name string, ordered bool) (*topic, error) {
	c, err := gps.NewClient(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("connecting to Pub/Sub in project %q: %w", project, err)
	}
	t := c.Topic(name)
	// Ordering is per PUBLISHER and has to be switched on before anything is
	// sent; a message with an ordering key on a publisher that has it off is
	// refused by the client, not reordered.
	t.EnableMessageOrdering = ordered
	return &topic{client: c, t: t}, nil
}

func (p *topic) Publish(ctx context.Context, m Message) Handle {
	return handle{p.t.Publish(ctx, &gps.Message{
		Data:        m.Data,
		Attributes:  m.Attributes,
		OrderingKey: m.OrderingKey,
	})}
}

func (p *topic) Stop() {
	p.t.Stop()
	_ = p.client.Close()
}

type handle struct{ r *gps.PublishResult }

func (h handle) ID(ctx context.Context) (string, error) { return h.r.Get(ctx) }
