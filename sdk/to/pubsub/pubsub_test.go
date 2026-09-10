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

// THE RULE THIS PACKAGE IS BUILT AROUND: by default a message is the payload
// and NOTHING else.
//
// The topic exists before this pipeline does. Its subscribers were written
// first and their filters were written first, so an attribute Brevis adds on
// its own is Brevis editing somebody else's contract -- and the subscriber
// finds out at three in the morning.
//
// This driver got that wrong once: it injected ingestion_id, provider, entity,
// source_key and record_ts into every message, which is exactly what it already
// refused to do to the payload.
func TestByDefaultAMessageCarriesNothingButThePayload(t *testing.T) {
	f := &fakeTopic{}
	if _, err := topicWith(f).Write(context.Background(), records(2), core.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for i, m := range f.sent {
		if len(m.Attributes) != 0 {
			t.Errorf("message %d arrived with attributes nobody asked for: %v", i, m.Attributes)
		}
		if m.OrderingKey != "" {
			t.Errorf("message %d arrived with ordering key %q", i, m.OrderingKey)
		}
		var payload map[string]any
		if err := json.Unmarshal(m.Data, &payload); err != nil {
			t.Fatal(err)
		}
		// Every key here was in the record; none was added.
		if len(payload) != 2 || payload["region"] != "sa-east-1" {
			t.Errorf("message %d payload was edited: %v", i, payload)
		}
	}
}

// And what the client DOES ask for arrives, under the client's own names.
//
// The consumer writes `sdk.Envelope` -- an alias for the type below -- so it
// never names an internal package, and calls IngestionID() only if it decides
// it wants an idempotency key.
func TestTheClientNamesEveryAttributeItself(t *testing.T) {
	f := &fakeTopic{}
	tp := topicWith(f)
	tp.Attributes = func(e core.Envelope) map[string]string {
		id, err := e.IngestionID()
		if err != nil {
			return nil
		}
		// Names that are the CLIENT'S, not this SDK's.
		return map[string]string{"eventId": id, "src": e.Provider}
	}
	if _, err := tp.Write(context.Background(), records(1), core.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got := f.sent[0].Attributes
	if len(got) != 2 {
		t.Fatalf("attributes = %v; the driver added to what the client returned", got)
	}
	if len(got["eventId"]) != 36 || got["src"] != "acme" {
		t.Errorf("attributes = %v", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(f.sent[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	if _, there := payload["eventId"]; there {
		t.Errorf("an attribute leaked into the payload: %v", payload)
	}
}

// A returned map is COPIED. A consumer reusing one buffer across records would
// otherwise have every message carry the last record's attributes -- the
// publisher holds the map for the life of the message.
func TestTheAttributeMapIsCopiedAndNotHeld(t *testing.T) {
	f := &fakeTopic{}
	tp := topicWith(f)
	shared := map[string]string{}
	tp.Attributes = func(e core.Envelope) map[string]string {
		// The mistake a consumer makes: one map, refilled.
		for k := range shared {
			delete(shared, k)
		}
		shared["key"] = e.SourceKey
		return shared
	}
	if _, err := tp.Write(context.Background(), records(3), core.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	for i, m := range f.sent {
		want := "k" + itoa(i)
		if m.Attributes["key"] != want {
			t.Errorf("message %d carries %q, wanted %q -- the map was held, not copied",
				i, m.Attributes["key"], want)
		}
	}
}

// Returning nil for one record is no attributes for that record, and not an
// error: a client may want them on some messages and not others.
func TestReturningNilIsNoAttributesForThatMessage(t *testing.T) {
	f := &fakeTopic{}
	tp := topicWith(f)
	tp.Attributes = func(e core.Envelope) map[string]string {
		if e.SourceKey == "k1" {
			return nil
		}
		return map[string]string{"k": e.SourceKey}
	}
	if _, err := tp.Write(context.Background(), records(3), core.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(f.sent[1].Attributes) != 0 {
		t.Errorf("a nil return produced %v", f.sent[1].Attributes)
	}
	if f.sent[0].Attributes["k"] != "k0" || f.sent[2].Attributes["k"] != "k2" {
		t.Errorf("the other messages lost theirs: %v %v", f.sent[0].Attributes, f.sent[2].Attributes)
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
	for _, want := range []string{"8 of 10", "DELIVERED", "re-delivers", "idempotent"} {
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
			says: []string{"at-least-once", "subscriber", "yourself"},
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

// The ordering key is whatever the client returns. There is no lookup rule to
// learn, because the driver invents none.
func TestTheOrderingKeyIsWhateverTheClientReturns(t *testing.T) {
	f := &fakeTopic{}
	tp := topicWith(f)
	tp.OrderingKey = func(e core.Envelope) string { return e.Provider + "/" + e.SourceKey }
	if _, err := tp.Write(context.Background(), records(2), core.WriteOptions{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if f.sent[0].OrderingKey != "acme/k0" || f.sent[1].OrderingKey != "acme/k1" {
		t.Errorf("ordering keys = %q, %q", f.sent[0].OrderingKey, f.sent[1].OrderingKey)
	}
}

// An EMPTY key is refused, and this is the one that would be silent otherwise.
//
// On a publisher with ordering enabled an empty key is not "no ordering": every
// message carrying one lands in a single order group, which is a throughput
// collapse that reads as a slow day.
func TestAnEmptyOrderingKeyIsRefused(t *testing.T) {
	f := &fakeTopic{}
	tp := topicWith(f)
	tp.OrderingKey = func(e core.Envelope) string {
		if e.SourceKey == "k1" {
			return ""
		}
		return e.SourceKey
	}
	_, err := tp.Write(context.Background(), records(3), core.WriteOptions{})
	if err == nil {
		t.Fatal("an empty ordering key was accepted")
	}
	for _, want := range []string{"k1", "one order group"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
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

// The real publisher must have ordering switched ON whenever a key will be set.
//
// Not a tautology: the Google client REFUSES a message carrying an ordering key
// when the publisher has ordering off -- it does not quietly reorder it -- so
// getting this wrong fails every publish on an ordered topic, in production
// only. A mutation hard-coding it to false passed the entire suite, because
// every test here uses the fake and the real constructor needs credentials.
func TestTheRealPublisherEnablesOrderingExactlyWhenAKeyWillBeSet(t *testing.T) {
	if (Topic{}).ordered() {
		t.Error("ordering is on with no OrderingKey, which costs throughput for nothing")
	}
	with := Topic{OrderingKey: func(core.Envelope) string { return "k" }}
	if !with.ordered() {
		t.Error("ordering is off with an OrderingKey; the client would refuse every message")
	}
}
