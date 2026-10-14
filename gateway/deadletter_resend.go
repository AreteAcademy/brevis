package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sdk "github.com/AreteAcademy/brevis/sdk"
)

// resendRequest is `gateway deadletter resend`'s arguments.
type resendRequest struct {
	ID              string // a batch key, a unique prefix of one, or an object
	Stream          string // only needed when the rows do not say
	DryRun          bool
	AllowDuplicates bool
	Again           bool
	By              string
}

// receipt is what a resend leaves under resent/: who, when, and what became
// of each part of the batch. The buried object itself is never touched.
type receipt struct {
	Batch      string            `json:"batch"`
	Object     string            `json:"object"`
	Stream     string            `json:"stream"`
	At         time.Time         `json:"at"`
	By         string            `json:"by"`
	Outcome    string            `json:"outcome"`
	ResentRows int               `json:"resent_rows"`
	Skipped    []string          `json:"skipped_jobs,omitempty"`
	Refused    map[string]string `json:"refused,omitempty"`
}

// A resend's outcome, as the receipt names it.
const (
	resendDelivered = "delivered"
	resendFailed    = "failed"
)

// found is one buried batch, located.
type found struct {
	stream Stream
	dl     *deadLetter
	object string
	rows   []map[string]any
	batch  buriedBatch
}

// findBatch locates the batch a person named: an object's name, or a batch
// key or a prefix of one that matches exactly one batch.
func findBatch(ctx context.Context, cfg *Config, stores *Stores, req resendRequest) (*found, error) {
	var hits []*found
	read := map[string]bool{}
	for _, st := range cfg.Streams {
		if req.Stream != "" && st.Name != req.Stream {
			continue
		}
		if read[st.DeadLetter.Path] {
			continue
		}
		read[st.DeadLetter.Path] = true
		d, err := openDeadLetter(ctx, stores, st.DeadLetter)
		if err != nil {
			continue // not a dead letter this can read; ls says why
		}
		objects, err := d.objects(ctx)
		if err != nil {
			return nil, fmt.Errorf("stream %s: listing %s: %w", st.Name, d.path, err)
		}
		for _, o := range objects {
			rows, err := d.rows(ctx, o)
			if err != nil {
				return nil, err
			}
			b := summarize(o, rows)
			if o != req.ID && (b.Batch == "" || !strings.HasPrefix(b.Batch, req.ID)) {
				continue
			}
			hits = append(hits, &found{stream: st, dl: d, object: o, rows: rows, batch: b})
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no buried batch matches %q (gateway deadletter ls lists them)", req.ID)
	case 1:
	default:
		// By object: a resend refused again buries the batch again under the
		// same key, and two objects of one key differ only in their names.
		names := make([]string, len(hits))
		for i, h := range hits {
			names[i] = h.object
		}
		sort.Strings(names)
		return nil, fmt.Errorf("%q matches %d batches: %s", req.ID, len(hits), strings.Join(names, ", "))
	}

	// The stream the rows came in on, when they say; the one whose dead letter
	// this is otherwise -- and when two streams share it, the person says.
	h := hits[0]
	if name := h.batch.Stream; name != "" {
		for _, st := range cfg.Streams {
			if st.Name == name {
				h.stream = st
				return h, nil
			}
		}
		return nil, fmt.Errorf("the batch came in on stream %q, which the config no longer has", name)
	}
	var sharing []string
	for _, st := range cfg.Streams {
		if st.DeadLetter.Path == h.dl.path {
			sharing = append(sharing, st.Name)
		}
	}
	if len(sharing) > 1 && req.Stream == "" {
		return nil, fmt.Errorf("the batch names no stream and %s share its dead letter: pass --stream",
			strings.Join(sharing, ", "))
	}
	return h, nil
}

// writeMode is what a resend's duplicate guard reads: auto_table writes as its
// `into` says, every other sink as its own `write`.
func writeMode(s Sink) string {
	if s.Into != nil {
		return s.Into.Write
	}
	return s.Write
}

// resendBatch is `gateway deadletter resend`: decide each part of the batch,
// send what may go through the stream's own send, and leave a receipt.
//
// It decides by LOAD JOB, not by table: a row names its job, and the job is
// what says whether the row is already in. Rows with no job are one part.
func resendBatch(ctx context.Context, cfg *Config, o options, req resendRequest, out io.Writer) (*receipt, error) {
	h, err := findBatch(ctx, cfg, o.stores, req)
	if err != nil {
		return nil, err
	}
	if !req.Again && !req.DryRun {
		resent, err := h.dl.receipts(ctx)
		if err != nil {
			return nil, err
		}
		// Only a resend that DELIVERED needs saying again: one that failed or
		// was refused left the rows where they were.
		if resent[h.batch.ID()] == resendDelivered {
			return nil, fmt.Errorf("batch %s was resent and delivered before (see %s/): "+
				"pass --again to send it once more", short(h.batch.ID()), resentDir)
		}
	}

	sink, ok := o.sinks[h.stream.Name]
	if !ok {
		if sink, err = o.build(ctx, h.stream.Sink, h.stream.Name, cfg.Name); err != nil {
			return nil, fmt.Errorf("stream %s: %w", h.stream.Name, err)
		}
	}

	rc := &receipt{Batch: h.batch.Batch, Object: h.object, Stream: h.stream.Name,
		At: time.Now().UTC(), By: req.By, Refused: map[string]string{}}
	appending := writeMode(h.stream.Sink) == WriteAppend

	// The parts, in the order their rows came.
	var order []string
	parts := map[string][]map[string]any{}
	for _, r := range h.rows {
		job, _ := r[ColumnDeadLetterJobID].(string)
		if _, seen := parts[job]; !seen {
			order = append(order, job)
		}
		parts[job] = append(parts[job], r)
	}

	var send []sdk.Envelope
	for _, job := range order {
		rows := parts[job]
		if job == "" {
			if appending && !req.AllowDuplicates {
				rc.Refused["(no job)"] = "write: append and no load job to ask"
				_, _ = fmt.Fprintf(out, "refuse  %d row(s) with no load job: under write: append a resend can write "+
					"them twice. Pass --allow-duplicates if they are known not to be in the destination\n", len(rows))
				continue
			}
			_, _ = fmt.Fprintf(out, "resend  %d row(s) with no load job\n", len(rows))
			send = append(send, stripped(rows)...)
			continue
		}

		location, _ := rows[0][ColumnDeadLetterJobLocation].(string)
		state, _ := rows[0][ColumnDeadLetterJobState].(string)
		je := &sdk.LoadJobError{JobID: job, Location: location, State: sdk.JobState(state)}
		rec, err := AskJob(ctx, sink, je, rows[0])
		switch now := stateOf(rec.Cause); {
		case err != nil:
			rc.Refused[job] = "could not ask: " + err.Error()
			_, _ = fmt.Fprintf(out, "refuse  job %s: could not learn what it became: %v\n", job, err)
		case rec.Landed:
			rc.Skipped = append(rc.Skipped, job)
			_, _ = fmt.Fprintf(out, "skip    job %s: it landed, %d row(s) already in the destination\n", job, len(rows))
		case now == sdk.JobRunning:
			rc.Refused[job] = "still running"
			_, _ = fmt.Fprintf(out, "refuse  job %s: still running, and it may still write its %d row(s). Wait, "+
				"then check: bq show -j --location=%s %s\n", job, len(rows), location, job)
		default:
			_, _ = fmt.Fprintf(out, "resend  job %s (%s): %d row(s)\n", job, now, len(rows))
			send = append(send, stripped(rows)...)
		}
	}

	if req.DryRun {
		_, _ = fmt.Fprintf(out, "dry run: would send %d row(s) of batch %s to %s; nothing written\n",
			len(send), short(h.batch.ID()), sink.Describe())
		return rc, refusedErr(rc)
	}

	rc.ResentRows = len(send)
	if len(send) == 0 {
		// Nothing left the dead letter, so there is nothing to record: a
		// receipt here would only make the next attempt ask for --again.
		_, _ = fmt.Fprintf(out, "nothing sent\n")
		return rc, refusedErr(rc)
	}
	// The stream's own send: the same retry and the same reconcile, under the
	// batch's own key, so a job that landed meanwhile is adopted, not written
	// twice.
	//
	// NOT the same dead letter. Refused again, the rows are still in the
	// object they came from; burying them a second time made a second object
	// with the same key and the same rows -- two batches in ls, and an
	// operator who resent both wrote them twice. Found running it against the
	// local stack. The receipt says it failed; the object stays the one batch.
	p := &pipe{stream: h.stream, sink: sink, dead: keptIn{object: h.object}, metrics: NewMetrics()}
	sendCtx := ctx
	if h.batch.Batch != "" {
		sendCtx = WithBatchKey(ctx, h.batch.Batch)
	}
	sendErr := p.send(sendCtx, send)
	if sendErr != nil {
		rc.Outcome = resendFailed
		_, _ = fmt.Fprintf(out, "failed  the destination refused the resend too; the rows stay in %s: %v\n", h.object, sendErr)
	} else {
		rc.Outcome = resendDelivered
		_, _ = fmt.Fprintf(out, "sent    %d row(s) to %s\n", len(send), sink.Describe())
	}
	if err := h.dl.writeReceipt(ctx, rc); err != nil {
		return rc, errors.Join(sendErr, fmt.Errorf("the receipt was not written: %w", err))
	}
	if sendErr != nil {
		return rc, sendErr
	}
	return rc, refusedErr(rc)
}

// stateOf reads what a job became off a reconciliation's cause.
func stateOf(cause error) sdk.JobState {
	var je *sdk.LoadJobError
	if errors.As(cause, &je) {
		return je.State
	}
	return sdk.JobUnknown
}

// refusedErr makes a resend that left something behind exit non-zero: a
// script must not read "some of it went" as "it went".
func refusedErr(rc *receipt) error {
	if len(rc.Refused) == 0 {
		return nil
	}
	return fmt.Errorf("%d part(s) of the batch were not resent", len(rc.Refused))
}

// stripped is the rows as they were before bury: no _dead_letter_* column,
// everything else -- ingestion_id included -- as it was.
func stripped(rows []map[string]any) []sdk.Envelope {
	out := make([]sdk.Envelope, 0, len(rows))
	for _, r := range rows {
		clean := make(map[string]any, len(r))
		for k, v := range r {
			if !strings.HasPrefix(k, "_dead_letter_") {
				clean[k] = v
			}
		}
		out = append(out, sdk.Envelope{Payload: clean})
	}
	return out
}

// keptIn is a resend's dead letter: the rows a resend could not deliver are
// already buried, in the object they were read from, so there is nothing to
// write -- only something to say.
type keptIn struct{ object string }

func (k keptIn) Describe() string { return "kept in " + k.object }
func (k keptIn) Write(_ context.Context, b []sdk.Envelope) (int64, error) {
	return int64(len(b)), nil
}

// writeReceipt writes <id>.<nanos>.json under resent/, whole.
func (d *deadLetter) writeReceipt(ctx context.Context, rc *receipt) error {
	id := rc.Batch
	if id == "" {
		id = rc.Object
	}
	raw, err := json.Marshal(rc)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%s.%d.json", id, rc.At.UnixNano())
	if d.store != nil {
		return d.store.Create(ctx, d.bucket, d.prefix+resentDir+"/"+name, bytes.NewReader(raw))
	}
	dir := filepath.Join(d.path, resentDir)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // beside the dead letter it describes
		return err
	}
	tmp := filepath.Join(dir, "."+name)
	if err := os.WriteFile(tmp, raw, 0o644); err != nil { //nolint:gosec // a receipt names no credential
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}
