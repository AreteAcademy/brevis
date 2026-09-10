// Package pubsub publishes records to a Google Cloud Pub/Sub topic.
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

	// OrderingKey names the field whose value orders the messages, or is empty
	// for no ordering.
	//
	// Empty is the default because ordering costs throughput and constrains
	// publishing to one region's worth of ordering guarantees. It is also what
	// every other destination here gives: a table load has no order either.
	//
	// The value is looked up on the ENVELOPE first -- "source_key", "provider",
	// "entity" -- and then in the payload, so the common case needs no
	// knowledge of the record's shape.
	OrderingKey string

	// Attributes are extra attributes on every message, fixed at construction.
	// The ingestion ones are always present; these are for whatever a
	// particular subscription filters on.
	Attributes map[string]string

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
		real, err := newTopic(ctx, t.Project, t.Name, t.OrderingKey != "")
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
			"this step re-delivers them, and the subscriber has to be idempotent on "+
			"`ingestion_id`. First failures: %s",
			res.RowsLoaded, len(handles), t.Describe(), failed, res.RowsLoaded,
			strings.Join(failures, "; "))
	}
	return res, nil
}

// maxReportedFailures caps the error text. Every failure is still COUNTED.
const maxReportedFailures = 5

// message turns one record into one message.
//
// The ingestion metadata goes in ATTRIBUTES and not into the payload, for two
// reasons. A subscription filters on attributes without parsing the body, which
// is the whole point of having them. And the payload belongs to the producer's
// schema -- adding keys to it makes every downstream reader parse a shape Brevis
// invented.
func (t Topic) message(e *core.Envelope) (Message, error) {
	data, err := json.Marshal(e.Payload)
	if err != nil {
		return Message{}, fmt.Errorf("the payload is not JSON: %w", err)
	}

	// The deterministic UUID v5 the SDK already computes from
	// provider|entity|source_key|record_ts. It costs nothing here because it
	// exists already, and it is exactly the key a subscriber needs to be
	// idempotent -- which the error above tells them to be.
	id, err := e.IngestionID()
	if err != nil {
		return Message{}, err
	}

	attrs := map[string]string{
		"ingestion_id": id,
		"provider":     e.Provider,
		"entity":       e.Entity,
		"source_key":   e.SourceKey,
	}
	if e.RecordTS != "" {
		attrs["record_ts"] = e.RecordTS
	}
	for k, v := range t.Attributes {
		attrs[k] = v
	}
	// Empty attributes are dropped rather than sent blank. A subscription
	// filtering on `attributes:provider` should not match a message whose
	// provider nobody set.
	for k, v := range attrs {
		if v == "" {
			delete(attrs, k)
		}
	}

	msg := Message{Data: data, Attributes: attrs}
	if t.OrderingKey != "" {
		key, err := orderingKey(t.OrderingKey, e, data)
		if err != nil {
			return Message{}, err
		}
		msg.OrderingKey = key
	}
	return msg, nil
}

// orderingKey resolves the configured field, envelope first.
//
// The envelope is checked first because that is where the useful keys are and
// it needs no knowledge of the record's shape. Falling through to the payload
// covers the case where the ordering is by something only the producer knows.
func orderingKey(field string, e *core.Envelope, data []byte) (string, error) {
	switch field {
	case "source_key":
		return e.SourceKey, nil
	case "provider":
		return e.Provider, nil
	case "entity":
		return e.Entity, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("OrderingKey %q is not an envelope field and the payload is "+
			"not an object, so there is nowhere to look it up", field)
	}
	v, ok := payload[field]
	if !ok {
		return "", fmt.Errorf("OrderingKey %q is neither an envelope field nor a key of the "+
			"payload. Ordering by a field that is not there would silently put every "+
			"message in one order group", field)
	}
	// An ordering key is a string to Pub/Sub whatever it is here.
	if s, ok := v.(string); ok {
		return s, nil
	}
	return fmt.Sprint(v), nil
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
			"on and no row to replace. Pub/Sub is at-least-once by design, and the " +
			"subscriber is where idempotency lives -- every message carries " +
			"`ingestion_id` for exactly that")
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
