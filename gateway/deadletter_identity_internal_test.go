package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"

	sdk "github.com/AreteAcademy/brevis/sdk"
)

// keyedRefusal refuses every batch and remembers the batch key each write saw
// -- the key a BigQuery sink would have named its job after.
type keyedRefusal struct {
	mu   sync.Mutex
	keys []string
}

func (*keyedRefusal) Describe() string { return "refuses" }
func (k *keyedRefusal) Write(ctx context.Context, _ []sdk.Envelope) (int64, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	key := BatchKeyOf(ctx)
	k.keys = append(k.keys, key)
	return 0, errors.New("the destination is down")
}

func buryOne(t *testing.T, sink *keyedRefusal, dead *recordingDead, payloads ...any) {
	t.Helper()
	p := &pipe{stream: Stream{Name: "clicks", Retry: Retry{Attempts: 1}}, sink: sink, dead: dead, metrics: NewMetrics()}
	batch := make([]sdk.Envelope, len(payloads))
	for i, pl := range payloads {
		batch[i] = sdk.Envelope{Payload: pl}
	}
	_ = p.send(context.Background(), batch)
}

func row(id string) map[string]any { return map[string]any{"id": id} }

// #86: "resend batch X" needs a batch X on the row, and a dead letter shared
// by two streams needs to say which one each row came from.
func TestABuriedRowSaysItsBatchAndItsStream(t *testing.T) {
	sink, dead := &keyedRefusal{}, &recordingDead{}
	buryOne(t, sink, dead, row("first"), row("second"))

	if len(dead.got) != 2 {
		t.Fatalf("%d rows buried, wanted 2", len(dead.got))
	}
	var batch string
	for _, e := range dead.got {
		r := e.Payload.(map[string]any)
		if r[ColumnDeadLetterStream] != "clicks" {
			t.Errorf("stream = %v, wanted clicks", r[ColumnDeadLetterStream])
		}
		b, _ := r[ColumnDeadLetterBatch].(string)
		if b == "" {
			t.Fatal("a buried row has no batch")
		}
		if batch == "" {
			batch = b
		} else if b != batch {
			t.Errorf("two rows of one batch carry %s and %s", batch, b)
		}
	}
}

// The batch on the row is the key the sink wrote under -- the one a BigQuery
// load job was named after -- so a resend under it adopts a job that landed
// instead of writing the rows again.
func TestTheBuriedBatchIsTheKeyTheSinkSaw(t *testing.T) {
	sink, dead := &keyedRefusal{}, &recordingDead{}
	buryOne(t, sink, dead, row("first"))

	got := dead.got[0].Payload.(map[string]any)[ColumnDeadLetterBatch]
	if len(sink.keys) != 1 || got != sink.keys[0] {
		t.Errorf("buried under %v; the sink wrote under %v", got, sink.keys)
	}
}

func TestTwoBuriedBatchesAreTwoBatches(t *testing.T) {
	sink, dead := &keyedRefusal{}, &recordingDead{}
	buryOne(t, sink, dead, row("first"))
	buryOne(t, sink, dead, row("second"))

	a := dead.got[0].Payload.(map[string]any)[ColumnDeadLetterBatch]
	b := dead.got[1].Payload.(map[string]any)[ColumnDeadLetterBatch]
	if a == b {
		t.Errorf("two batches buried under one key, %v", a)
	}
}

// An opaque payload is buried as it came, as it always was.
func TestAnOpaquePayloadIsBuriedUnstamped(t *testing.T) {
	sink, dead := &keyedRefusal{}, &recordingDead{}
	buryOne(t, sink, dead, "not a map")
	if dead.got[0].Payload != "not a map" {
		t.Errorf("payload = %v", dead.got[0].Payload)
	}
}

// A resend sends under the batch's original key, so a BigQuery job that landed
// is adopted, not written twice. send keeps a key it is handed.
func TestSendKeepsTheKeyItIsHanded(t *testing.T) {
	sink, dead := &keyedRefusal{}, &recordingDead{}
	p := &pipe{stream: Stream{Name: "clicks", Retry: Retry{Attempts: 1}}, sink: sink, dead: dead, metrics: NewMetrics()}
	_ = p.send(WithBatchKey(context.Background(), "the-original-key"), []sdk.Envelope{{Payload: row("first")}})

	if len(sink.keys) != 1 || sink.keys[0] != "the-original-key" {
		t.Errorf("the sink wrote under %v; the resend handed it the-original-key", sink.keys)
	}
	if got := dead.got[0].Payload.(map[string]any)[ColumnDeadLetterBatch]; got != "the-original-key" {
		t.Errorf("re-buried under %v", got)
	}
}

// And live traffic, which hands it nothing, still gets a key per batch: two
// batches under one key would adopt each other's BigQuery job and drop rows.
func TestLiveBatchesStillGetAKeyEach(t *testing.T) {
	sink := &keyedRefusal{}
	p := &pipe{stream: Stream{Name: "clicks", Retry: Retry{Attempts: 1}}, sink: sink, dead: &recordingDead{}, metrics: NewMetrics()}
	_ = p.send(context.Background(), []sdk.Envelope{{Payload: row("first")}})
	_ = p.send(context.Background(), []sdk.Envelope{{Payload: row("second")}})

	if len(sink.keys) != 2 || sink.keys[0] == "" || sink.keys[0] == sink.keys[1] {
		t.Errorf("two live batches wrote under %v", sink.keys)
	}
}
