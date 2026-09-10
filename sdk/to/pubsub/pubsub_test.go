package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// fakeTopic is the topic, and it exists because the case this driver is built
// around -- a publish that fails part of the way through -- cannot be produced
// on demand against a real one.
type fakeTopic struct {
	sent    []Message
	failAt  map[int]error // index -> the error that message comes back with
	stopped int
}

func (f *fakeTopic) Publish(_ context.Context, m Message) Handle {
	i := len(f.sent)
	f.sent = append(f.sent, m)
	return fakeHandle{id: "srv-" + itoa(i), err: f.failAt[i]}
}

func (f *fakeTopic) Stop() { f.stopped++ }

type fakeHandle struct {
	id  string
	err error
}

func (h fakeHandle) ID(context.Context) (string, error) {
	if h.err != nil {
		return "", h.err
	}
	return h.id, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func records(n int) []core.Envelope {
	out := make([]core.Envelope, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, core.Envelope{
			Provider: "acme", Entity: "orders",
			SourceKey: "k" + itoa(i), RecordTS: "2026-03-11T04:00:00Z",
			Payload: map[string]any{"id": i, "region": "sa-east-1"},
		})
	}
	return out
}

func topicWith(f *fakeTopic) Topic {
	return Topic{Project: "acme-prod", Name: "orders", Client: f}
}

// One record, one message. Not a batch in one message: a subscriber's unit of
// work is a message, and packing records together makes every consumer
// downstream unpack a Brevis-shaped envelope it never asked for.
func TestOneRecordBecomesOneMessage(t *testing.T) {
	f := &fakeTopic{}
	res, err := topicWith(f).Write(context.Background(), records(3), core.WriteOptions{})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(f.sent) != 3 || res.RowsLoaded != 3 {
		t.Fatalf("sent %d, reported %d", len(f.sent), res.RowsLoaded)
	}
	var payload map[string]any
	if err := json.Unmarshal(f.sent[1].Data, &payload); err != nil {
		t.Fatalf("the message data is not the payload: %v", err)
	}
	if payload["region"] != "sa-east-1" {
		t.Errorf("payload = %v", payload)
	}
	if f.stopped != 1 {
		t.Errorf("the publisher was stopped %d time(s)", f.stopped)
	}
}

// The ingestion metadata goes in ATTRIBUTES, and the payload is left alone.
//
// A subscription filters on attributes without parsing the body, which is what
// they are for. And the payload belongs to the producer's schema -- adding keys
// to it would make every downstream reader parse a shape Brevis invented.
func TestTheMetadataGoesInAttributesAndNotInThePayload(t *testing.T) {
	f := &fakeTopic{}
	tp := topicWith(f)
	tp.Attributes = map[string]string{"pipeline": "orders-nightly"}
	if _, err := tp.Write(context.Background(), records(1), core.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := f.sent[0].Attributes
	for k, want := range map[string]string{
		"provider": "acme", "entity": "orders", "source_key": "k0",
		"record_ts": "2026-03-11T04:00:00Z", "pipeline": "orders-nightly",
	} {
		if got[k] != want {
			t.Errorf("attribute %q = %q, wanted %q", k, got[k], want)
		}
	}
	// The deterministic UUID the SDK already computes. It is the key a
	// subscriber needs to be idempotent, and the partial-failure error tells
	// them to use it.
	if len(got["ingestion_id"]) != 36 {
		t.Errorf("ingestion_id = %q", got["ingestion_id"])
	}

	var payload map[string]any
	if err := json.Unmarshal(f.sent[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"ingestion_id", "provider", "entity", "source_key"} {
		if _, there := payload[absent]; there {
			t.Errorf("%q was added to the payload: %v", absent, payload)
		}
	}
}

// An empty attribute is dropped rather than sent blank: a subscription
// filtering on `attributes:provider` must not match a message whose provider
// nobody set.
func TestAnEmptyAttributeIsDroppedRatherThanSentBlank(t *testing.T) {
	f := &fakeTopic{}
	recs := records(1)
	recs[0].Provider = ""
	if _, err := topicWith(f).Write(context.Background(), recs, core.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if v, there := f.sent[0].Attributes["provider"]; there {
		t.Errorf("an empty provider was sent as %q", v)
	}
}

// THE decision this driver is built around.
//
// A publish is not transactional. Messages that went are DELIVERED, and
// somebody downstream is already acting on them, so the count on the error path
// is what actually went -- and the error says so, because the operator's next
// move depends on knowing a re-run is a re-delivery.
func TestAPartialFailureReportsWhatActuallyWent(t *testing.T) {
	f := &fakeTopic{failAt: map[int]error{
		3: errors.New("deadline exceeded"),
		7: errors.New("deadline exceeded"),
	}}
	res, err := topicWith(f).Write(context.Background(), records(10), core.WriteOptions{})

	if err == nil {
		t.Fatal("a partial failure was reported as success")
	}
	if res == nil {
		t.Fatal("the result was dropped, so the caller cannot know what went")
	}
	if res.RowsLoaded != 8 {
		t.Errorf("RowsLoaded = %d, wanted the 8 that actually went", res.RowsLoaded)
	}
	for _, want := range []string{"8 of 10", "DELIVERED", "re-delivers", "ingestion_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%s", want, err)
		}
	}
}

// Every handle is waited on, INCLUDING after the first failure.
//
// Returning early would leave the rest in flight with nobody reading their
// outcome -- and they are already on their way, so the count would be a guess,
// in the direction that flatters the report.
func TestEveryMessageIsWaitedOnEvenAfterOneFails(t *testing.T) {
	f := &fakeTopic{failAt: map[int]error{0: errors.New("nope")}}
	res, err := topicWith(f).Write(context.Background(), records(5), core.WriteOptions{})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if res.RowsLoaded != 4 {
		t.Errorf("RowsLoaded = %d; the four after the failure were abandoned", res.RowsLoaded)
	}
}

// The failure list is truncated and the COUNT is not. A batch where everything
// failed produces one line per record, and a log nobody can read is a log
// nobody reads.
func TestTheFailureListIsTruncatedButTheCountIsNot(t *testing.T) {
	fail := map[int]error{}
	for i := 0; i < 50; i++ {
		fail[i] = errors.New("nope")
	}
	f := &fakeTopic{failAt: fail}
	res, err := topicWith(f).Write(context.Background(), records(50), core.WriteOptions{})
	if err == nil {
		t.Fatal("expected a failure")
	}
	if res.RowsLoaded != 0 {
		t.Errorf("RowsLoaded = %d", res.RowsLoaded)
	}
	if len(res.ErrorRows) != maxReportedFailures {
		t.Errorf("kept %d failure lines, wanted %d", len(res.ErrorRows), maxReportedFailures)
	}
	if !strings.Contains(err.Error(), "0 of 50") {
		t.Errorf("the count was truncated with the list: %s", err)
	}
}

// Everything a topic cannot honour is REFUSED, not ignored.
//
// An ignored option is worse than an error: the author believes it is being
// enforced and finds out from the data. `to.Files` set the precedent by
// refusing DedupMerge; a topic refuses three.
func TestWhatATopicCannotDoIsRefusedAndNotIgnored(t *testing.T) {
	for name, tc := range map[string]struct {
		opt  core.WriteOptions
		says []string
	}{
		"a Schema, which exists to create a table": {
			opt:  core.WriteOptions{Schema: core.Schema{{Name: "id", Type: core.TypeString}}},
			says: []string{"Schema", "exists already", "Columns"},
		},
		"deduplication, which has no key to match on": {
			opt:  core.WriteOptions{Dedup: core.DedupMerge},
			says: []string{"at-least-once", "subscriber", "ingestion_id"},
		},
		"a partition, which is a table's idea": {
			opt:  core.WriteOptions{PartitionBy: "created_at"},
			says: []string{"OrderingKey", "created_at"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeTopic{}
			_, err := topicWith(f).Write(context.Background(), records(1), tc.opt)
			if err == nil {
				t.Fatal("it was accepted")
			}
			for _, want := range tc.says {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message does not say %q: %v", want, err)
				}
			}
			if len(f.sent) != 0 {
				t.Errorf("%d message(s) went out anyway", len(f.sent))
			}
		})
	}
}

// Declared columns are checked, exactly as they are for a table: a message
// missing a field the author declared is the same bug wherever it lands.
func TestDeclaredColumnsAreStillChecked(t *testing.T) {
	f := &fakeTopic{}
	_, err := topicWith(f).Write(context.Background(), records(1),
		core.WriteOptions{Columns: []string{"id", "region", "absent_column"}})
	if err == nil {
		t.Fatal("a row missing a declared column was accepted")
	}
	if !strings.Contains(err.Error(), "absent_column") {
		t.Errorf("the error does not name the column: %v", err)
	}
}

// Project and Name have no defaults. A topic is not something to default into,
// and the cost of guessing wrong is messages arriving where nobody is looking.
func TestTheTopicMustBeNamedInFull(t *testing.T) {
	for name, tp := range map[string]Topic{
		"no project": {Name: "orders", Client: &fakeTopic{}},
		"no name":    {Project: "acme-prod", Client: &fakeTopic{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tp.Write(context.Background(), records(1), core.WriteOptions{}); err == nil {
				t.Fatal("it was accepted")
			}
		})
	}
}

// The ordering key comes off the envelope without knowing the record's shape,
// and falls through to the payload when the ordering is by something only the
// producer knows.
func TestTheOrderingKeyResolvesFromTheEnvelopeThenThePayload(t *testing.T) {
	for field, want := range map[string]string{
		"source_key": "k0",        // the envelope
		"provider":   "acme",      // the envelope
		"region":     "sa-east-1", // the payload
	} {
		t.Run(field, func(t *testing.T) {
			f := &fakeTopic{}
			tp := topicWith(f)
			tp.OrderingKey = field
			if _, err := tp.Write(context.Background(), records(1), core.WriteOptions{}); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if f.sent[0].OrderingKey != want {
				t.Errorf("ordering key = %q, wanted %q", f.sent[0].OrderingKey, want)
			}
		})
	}
}

// An ordering key that is nowhere is REFUSED, and this is the one that would be
// silent otherwise: an empty key puts every message in one order group, which
// is a throughput collapse that looks like a slow day.
func TestAnOrderingKeyThatIsNowhereIsRefused(t *testing.T) {
	f := &fakeTopic{}
	tp := topicWith(f)
	tp.OrderingKey = "no_such_field"
	_, err := tp.Write(context.Background(), records(1), core.WriteOptions{})
	if err == nil {
		t.Fatal("an ordering key that is nowhere was accepted")
	}
	if !strings.Contains(err.Error(), "no_such_field") {
		t.Errorf("the error does not name the field: %v", err)
	}
	if len(f.sent) != 0 {
		t.Error("a message went out with an empty ordering key")
	}
}

// With no ordering configured, no message carries a key. That is the default,
// and it is what every other destination here gives: a table load has no order.
func TestWithNoOrderingConfiguredNoMessageCarriesAKey(t *testing.T) {
	f := &fakeTopic{}
	if _, err := topicWith(f).Write(context.Background(), records(3), core.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for i, m := range f.sent {
		if m.OrderingKey != "" {
			t.Errorf("message %d carries ordering key %q", i, m.OrderingKey)
		}
	}
}

// An empty batch publishes nothing and is not an error: a run whose source had
// no new records is a normal Tuesday.
func TestAnEmptyBatchPublishesNothing(t *testing.T) {
	f := &fakeTopic{}
	res, err := topicWith(f).Write(context.Background(), nil, core.WriteOptions{})
	if err != nil {
		t.Fatalf("an empty batch failed: %v", err)
	}
	if res.RowsLoaded != 0 || len(f.sent) != 0 {
		t.Errorf("something went out: %+v", f.sent)
	}
}

// Describe names the destination the way a log line needs it.
func TestDescribeNamesTheTopic(t *testing.T) {
	if got := (Topic{Project: "acme-prod", Name: "orders"}).Describe(); got != "pubsub://acme-prod/orders" {
		t.Errorf("Describe = %q", got)
	}
}
