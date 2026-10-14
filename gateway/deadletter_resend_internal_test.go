package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	sdk "github.com/AreteAcademy/brevis/sdk"
)

// takes is the destination of a resend: it records what it was sent and under
// which batch key, and answers what each load job became.
type takes struct {
	mu     sync.Mutex
	got    []map[string]any
	keys   []string
	refuse error
	jobs   map[string]sdk.JobState // job id -> what BigQuery says now
	landed map[string]bool
	asked  []string
}

func (*takes) Describe() string { return "bigquery:p.d" }
func (s *takes) Write(ctx context.Context, b []sdk.Envelope) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, BatchKeyOf(ctx))
	if s.refuse != nil {
		return 0, s.refuse
	}
	for _, e := range b {
		s.got = append(s.got, e.Payload.(map[string]any))
	}
	return int64(len(b)), nil
}
func (s *takes) Reconcile(_ context.Context, cause error) (Reconciliation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	je, ok := cause.(*sdk.LoadJobError)
	if !ok {
		return Reconciliation{Cause: cause}, nil // a refusal names no job
	}
	s.asked = append(s.asked, je.JobID)
	now := *je
	now.State = s.jobs[je.JobID]
	return Reconciliation{Landed: s.landed[je.JobID], Cause: &now}, nil
}

func resendConfig(dir, stream, write string) *Config {
	return &Config{Streams: []Stream{{Name: stream, Retry: Retry{Attempts: 1},
		Sink:       Sink{Type: "bigquery", Write: write},
		DeadLetter: Sink{Type: "files", Path: dir}}}}
}

func resendWith(t *testing.T, cfg *Config, sink Sinker, req resendRequest) (string, error) {
	t.Helper()
	var out bytes.Buffer
	o := options{sinks: map[string]Sinker{cfg.Streams[0].Name: sink}}
	_, err := resendBatch(context.Background(), cfg, o, req, &out)
	return out.String(), err
}

func onlyBatch(t *testing.T, cfg *Config) buriedBatch {
	t.Helper()
	got, err := listBuried(context.Background(), cfg, nil, "")
	if err != nil || len(got) == 0 {
		t.Fatalf("listing: %v, %v", got, err)
	}
	return got[0]
}

// #86, the plain case: no job, `write: merge`. The rows go back through the
// send, without the dead-letter columns, under the batch's own key, and the
// resend leaves a receipt.
func TestResendSendsTheRowsBackUnderTheBatchKey(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "clicks", &keyedRefusal{}, map[string]any{"id": "first", "ingestion_id": "i-1"})
	cfg := resendConfig(dir, "clicks", WriteMerge)
	b := onlyBatch(t, cfg)

	sink := &takes{}
	if _, err := resendWith(t, cfg, sink, resendRequest{ID: b.Batch[:8], By: "oncall"}); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 1 {
		t.Fatalf("the sink got %d rows", len(sink.got))
	}
	r := sink.got[0]
	for k := range r {
		if strings.HasPrefix(k, "_dead_letter_") {
			t.Errorf("the resent row still carries %s", k)
		}
	}
	if r["ingestion_id"] != "i-1" || r["id"] != "first" {
		t.Errorf("the resent row changed: %v", r)
	}
	if len(sink.keys) != 1 || sink.keys[0] != b.Batch {
		t.Errorf("resent under %v; the batch is %s", sink.keys, b.Batch)
	}
	if after := onlyBatch(t, cfg); after.Resent != "delivered" {
		t.Error("no receipt: ls does not show the batch as resent")
	}
	receipts, _ := filepath.Glob(filepath.Join(dir, resentDir, "*.json"))
	if len(receipts) != 1 {
		t.Fatalf("%d receipts", len(receipts))
	}
	raw, _ := os.ReadFile(receipts[0])
	var rc map[string]any
	if err := json.Unmarshal(raw, &rc); err != nil || rc["by"] != "oncall" || rc["outcome"] != "delivered" {
		t.Errorf("receipt = %s (%v)", raw, err)
	}
}

// auto_table, three tables: one whose job landed is skipped, one whose job
// still runs is refused, one BigQuery never had goes back.
func TestResendDecidesEachJob(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "orders", jobRefusal{state: sdk.JobRunning},
		map[string]any{"id": "o-1", "table": "landed"},
		map[string]any{"id": "o-2", "table": "running"},
		map[string]any{"id": "o-3", "table": "absent"})
	cfg := resendConfig(dir, "orders", WriteAppend)
	b := onlyBatch(t, cfg)

	sink := &takes{
		jobs:   map[string]sdk.JobState{"job_landed": sdk.JobDone, "job_running": sdk.JobRunning, "job_absent": sdk.JobAbsent},
		landed: map[string]bool{"job_landed": true},
	}
	out, err := resendWith(t, cfg, sink, resendRequest{ID: b.Batch})
	if err == nil {
		t.Error("a refused job did not make the resend fail")
	}
	if len(sink.got) != 1 || sink.got[0]["id"] != "o-3" {
		t.Errorf("the sink got %v; only the absent job's row goes back", sink.got)
	}
	for _, want := range []string{"job_landed", "landed", "job_running", "still running", "bq show"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not say %q:\n%s", want, out)
		}
	}
}

// No job and `write: append`: a resend may write twice, and nobody decided
// that. Refused unless asked for.
func TestResendRefusesAppendWithoutAJob(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "clicks", &keyedRefusal{}, row("first"))
	cfg := resendConfig(dir, "clicks", WriteAppend)
	b := onlyBatch(t, cfg)

	sink := &takes{}
	out, err := resendWith(t, cfg, sink, resendRequest{ID: b.Batch})
	if err == nil || len(sink.got) != 0 {
		t.Fatalf("append without a job was resent (err %v, %d rows)", err, len(sink.got))
	}
	if !strings.Contains(out, "--allow-duplicates") {
		t.Errorf("the refusal does not say how to override it:\n%s", out)
	}
	if _, err := resendWith(t, cfg, sink, resendRequest{ID: b.Batch, AllowDuplicates: true}); err != nil || len(sink.got) != 1 {
		t.Errorf("with --allow-duplicates: err %v, %d rows", err, len(sink.got))
	}
}

func TestResendDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "clicks", &keyedRefusal{}, row("first"))
	cfg := resendConfig(dir, "clicks", WriteMerge)
	b := onlyBatch(t, cfg)

	sink := &takes{}
	out, err := resendWith(t, cfg, sink, resendRequest{ID: b.Batch, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 0 || onlyBatch(t, cfg).Resent != "" {
		t.Error("a dry run wrote rows or a receipt")
	}
	if !strings.Contains(out, "1 row") {
		t.Errorf("the dry run does not say what it would send:\n%s", out)
	}
}

// Refused again: the resend fails, the receipt says so, and the batch is still
// ONE object. Burying it a second time made a second object with the same key
// and rows, and resending both would write them twice (found on the local
// stack).
func TestAResendTheSinkRefusesStaysOneBatch(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "clicks", &keyedRefusal{}, row("first"))
	cfg := resendConfig(dir, "clicks", WriteMerge)
	b := onlyBatch(t, cfg)

	sink := &takes{refuse: os.ErrDeadlineExceeded}
	if _, err := resendWith(t, cfg, sink, resendRequest{ID: b.Batch}); err == nil {
		t.Error("a refused resend reported success")
	}
	all, _ := listBuried(context.Background(), cfg, nil, "")
	if len(all) != 1 || all[0].Object != b.Object || all[0].Resent != "failed" {
		t.Errorf("after a refused resend: %+v; wanted the one batch, marked failed", all)
	}
	// And it can be tried again without --again: nothing was delivered.
	sink.refuse = nil
	if _, err := resendWith(t, cfg, sink, resendRequest{ID: b.Batch}); err != nil || len(sink.got) != 1 {
		t.Errorf("the retry after a failure: err %v, %d rows", err, len(sink.got))
	}
}

func TestResendNamesTheCandidatesOfAnAmbiguousPrefix(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "clicks", &keyedRefusal{}, row("first"))
	buryInto(t, dir, "clicks", &keyedRefusal{}, row("second"))
	cfg := resendConfig(dir, "clicks", WriteMerge)

	_, err := resendWith(t, cfg, &takes{}, resendRequest{ID: ""})
	if err == nil || !strings.Contains(err.Error(), "matches 2") {
		t.Errorf("err = %v", err)
	}
}

// A second resend of a batch with a receipt needs saying so.
func TestResendAgainNeedsAgain(t *testing.T) {
	dir := t.TempDir()
	buryInto(t, dir, "clicks", &keyedRefusal{}, row("first"))
	cfg := resendConfig(dir, "clicks", WriteMerge)
	b := onlyBatch(t, cfg)

	sink := &takes{}
	if _, err := resendWith(t, cfg, sink, resendRequest{ID: b.Batch}); err != nil {
		t.Fatal(err)
	}
	if _, err := resendWith(t, cfg, sink, resendRequest{ID: b.Batch}); err == nil || !strings.Contains(err.Error(), "--again") {
		t.Errorf("a second resend went through without --again: %v", err)
	}
	if _, err := resendWith(t, cfg, sink, resendRequest{ID: b.Batch, Again: true}); err != nil {
		t.Errorf("with --again: %v", err)
	}
}
