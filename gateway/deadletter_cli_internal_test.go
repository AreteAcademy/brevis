package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/to"
)

// filesDead is a dead letter the way the files sink writes one: to.Files, one
// object per batch, one row per line.
type filesDead struct{ f to.Files }

func (d filesDead) Describe() string { return "files:" + d.f.Path }
func (d filesDead) Write(ctx context.Context, b []sdk.Envelope) (int64, error) {
	res, err := d.f.Write(ctx, b, sdk.WriteOptions{})
	if res == nil {
		return 0, err
	}
	return res.RowsLoaded, err
}

// jobRefusal refuses every batch naming a load job per row's `table`, as a
// BigQuery or auto_table sink does when the window closes on running jobs.
type jobRefusal struct{ state sdk.JobState }

func (jobRefusal) Describe() string { return "bigquery:p.d" }
func (j jobRefusal) Write(context.Context, []sdk.Envelope) (int64, error) {
	return 0, &sdk.LoadJobError{Table: "events", JobID: "job_events", State: j.state,
		Err: errors.New("wait for load job: deadline exceeded")}
}
func (j jobRefusal) JobOf(cause error, row map[string]any) (*sdk.LoadJobError, bool) {
	t, _ := row["table"].(string)
	return &sdk.LoadJobError{Table: t, JobID: "job_" + t, State: j.state}, true
}

// buryInto runs a real send that fails, so the dead letter on disk is exactly
// what the gateway writes.
func buryInto(t *testing.T, dir, stream string, sink Sinker, payloads ...map[string]any) {
	t.Helper()
	p := &pipe{stream: Stream{Name: stream, Retry: Retry{Attempts: 1}}, sink: sink,
		dead: filesDead{to.Files{Path: dir}}, metrics: NewMetrics()}
	batch := make([]sdk.Envelope, len(payloads))
	for i, pl := range payloads {
		batch[i] = sdk.Envelope{Payload: pl}
	}
	_ = p.send(context.Background(), batch)
	time.Sleep(2 * time.Millisecond) // part-<nanos> names: keep them apart
}

func dlConfig(dir string, streams ...string) *Config {
	c := &Config{}
	for _, s := range streams {
		c.Streams = append(c.Streams, Stream{Name: s, DeadLetter: Sink{Type: "files", Path: dir}})
	}
	return c
}

// #86: one line per buried batch, with what a person needs to decide.
func TestLsListsTheBuriedBatches(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "clicks", &keyedRefusal{}, row("first"), row("second"))
	buryInto(t, dir, "orders", jobRefusal{state: sdk.JobRunning},
		map[string]any{"id": "o-1", "table": "orders_a"}, map[string]any{"id": "o-2", "table": "orders_b"},
		map[string]any{"id": "o-3", "table": "orders_a"})

	got, err := listBuried(context.Background(), dlConfig(dir, "clicks", "orders"), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d batches listed, wanted 2: %+v", len(got), got)
	}
	orders, clicks := got[0], got[1] // newest first
	// Three rows, two tables: two jobs, each counted once.
	if orders.Stream != "orders" || orders.Rows != 3 || orders.Jobs["running"] != 2 {
		t.Errorf("orders batch = %+v; wanted 3 rows and 2 running jobs", orders)
	}
	if clicks.Stream != "clicks" || clicks.Rows != 2 || clicks.Batch == "" || len(clicks.Jobs) != 0 {
		t.Errorf("clicks batch = %+v", clicks)
	}
	if !strings.Contains(clicks.Reason, "the destination is down") || clicks.At.IsZero() {
		t.Errorf("clicks batch has no reason or time: %+v", clicks)
	}
}

// Streams can share a dead letter; --stream keeps one.
func TestLsKeepsOneStream(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "clicks", &keyedRefusal{}, row("first"))
	buryInto(t, dir, "orders", &keyedRefusal{}, row("second"))

	got, err := listBuried(context.Background(), dlConfig(dir, "clicks", "orders"), nil, "clicks")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Stream != "clicks" {
		t.Errorf("--stream clicks listed %+v", got)
	}
	if _, err := listBuried(context.Background(), dlConfig(dir, "clicks"), nil, "nope"); err == nil {
		t.Error("an unknown stream was not refused")
	}
}

// A receipt under resent/ marks its batch, and is not a batch itself.
func TestLsMarksAResentBatch(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "clicks", &keyedRefusal{}, row("first"))
	got, _ := listBuried(context.Background(), dlConfig(dir, "clicks"), nil, "")
	if err := os.MkdirAll(filepath.Join(dir, resentDir), 0o755); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(dir, resentDir, got[0].Batch+".1.json")
	if err := os.WriteFile(receipt, []byte(`{"outcome":"delivered"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := listBuried(context.Background(), dlConfig(dir, "clicks"), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Resent != "delivered" {
		t.Errorf("after a receipt: %+v", got)
	}
}

func TestLsReadsOnlyAFilesDeadLetter(t *testing.T) {
	c := &Config{Streams: []Stream{{Name: "clicks", DeadLetter: Sink{Type: "pubsub"}}}}
	if _, err := listBuried(context.Background(), c, nil, ""); err == nil || !strings.Contains(err.Error(), "files") {
		t.Errorf("err = %v; a pubsub dead letter cannot be read back, and it has to say so", err)
	}
}

// A dead letter nothing was ever buried in is an empty answer, not an error.
func TestLsOfAnEmptyDeadLetter(t *testing.T) {
	got, err := listBuried(context.Background(), dlConfig(filepath.Join(t.TempDir(), "never"), "clicks"), nil, "")
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestLsPrintsATableAndJSON(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "clicks", &keyedRefusal{}, row("first"))
	var b bytes.Buffer
	batches, _ := listBuried(context.Background(), dlConfig(dir, "clicks"), nil, "")
	if err := printBuried(&b, batches); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BATCH", "clicks", short(batches[0].Batch), "the destination is down"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the table does not show %q:\n%s", want, b.String())
		}
	}
	var back []buriedBatch
	raw, _ := json.Marshal(batches)
	if err := json.Unmarshal(raw, &back); err != nil || len(back) != 1 {
		t.Errorf("the JSON does not round-trip: %v", err)
	}
}

// One dead letter that cannot be read does not hide the others.
func TestLsShowsWhatItCouldRead(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "clicks", &keyedRefusal{}, row("first"))
	c := dlConfig(dir, "clicks")
	c.Streams = append(c.Streams, Stream{Name: "events", DeadLetter: Sink{Type: "pubsub"}})

	got, err := listBuried(context.Background(), c, nil, "")
	if err == nil || !strings.Contains(err.Error(), "events") {
		t.Errorf("err = %v; the unreadable dead letter has to be named", err)
	}
	if len(got) != 1 || got[0].Stream != "clicks" {
		t.Errorf("listed %+v; the readable one has to be there", got)
	}
}
