package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	sdk "github.com/AreteAcademy/brevis/sdk"
)

// THE DEAD LETTER, READ BACK (#86).
//
// A buried batch used to be an ERROR line and a file, and getting it back
// meant finding the file, reading _dead_letter_reason and writing a loader for
// the occasion -- which is where duplicates come from (#85). These commands
// run where the destination's credentials already are: this binary, a Job
// with its image, a laptop with ADC. The engine never takes part; the console
// shows the commands and nothing more.
//
// Only a `files` dead letter is read: a directory, gs:// or s3://. It is the
// one the docs and every example use, and the one whose layout is known -- one
// batch per object, one row per line.

// resentDir is where a resend's receipts go, under the dead letter's own path.
const resentDir = "resent"

// buriedBatch is one object of a dead letter: one batch, as bury wrote it.
type buriedBatch struct {
	Object string    `json:"object"`
	Batch  string    `json:"batch,omitempty"`
	Stream string    `json:"stream,omitempty"`
	At     time.Time `json:"buried_at,omitzero"`
	Rows   int       `json:"rows"`
	Reason string    `json:"reason,omitempty"`
	// Jobs counts the load jobs the rows name, by the state they were buried
	// in: one per table under auto_table, one for a BigQuery sink.
	Jobs map[string]int `json:"jobs,omitempty"`
	// Resent is what the latest resend of this batch came to: delivered or
	// failed. Empty when it was never resent.
	Resent string `json:"resent,omitempty"`
}

// ID is what a person types to name this batch: the batch key, or the object
// for a batch buried before rows carried one.
func (b buriedBatch) ID() string {
	if b.Batch != "" {
		return b.Batch
	}
	return b.Object
}

// deadLetter is a dead letter's location, readable whichever store it is in.
type deadLetter struct {
	path   string    // as configured
	store  sdk.Store // nil for a local directory
	bucket string
	prefix string // with a trailing slash, or empty
}

// openDeadLetter resolves a `files` dead letter for reading. The store is the
// one this binary carries for the scheme, so a slim build reading gs:// is
// told it carries no backend for it rather than failing on the first object.
func openDeadLetter(ctx context.Context, stores *Stores, s Sink) (*deadLetter, error) {
	if s.Type != "files" {
		return nil, fmt.Errorf("the dead letter is %q, and only a `files` dead letter "+
			"(a directory, gs:// or s3://) can be read back", s.Type)
	}
	d := &deadLetter{path: s.Path}
	scheme, rest, remote := strings.Cut(s.Path, "://")
	if !remote {
		return d, nil
	}
	store, err := stores.Open(ctx, s.Path)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, fmt.Errorf("this binary carries no store for %s://", scheme)
	}
	d.store = store
	d.bucket, d.prefix, _ = strings.Cut(rest, "/")
	if d.prefix != "" && !strings.HasSuffix(d.prefix, "/") {
		d.prefix += "/"
	}
	return d, nil
}

// objects lists the batches: the .ndjson objects directly under the path,
// not the receipts under resent/.
func (d *deadLetter) objects(ctx context.Context) ([]string, error) {
	var keys []string
	if d.store == nil {
		entries, err := os.ReadDir(d.path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // nothing was ever buried
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".ndjson") {
				keys = append(keys, e.Name())
			}
		}
		return keys, nil
	}
	all, err := d.store.List(ctx, d.bucket, d.prefix)
	if err != nil {
		return nil, err
	}
	for _, k := range all {
		name := strings.TrimPrefix(k, d.prefix)
		if !strings.Contains(name, "/") && strings.HasSuffix(name, ".ndjson") {
			keys = append(keys, name)
		}
	}
	return keys, nil
}

// receipts says, by batch, what its latest resend came to. A receipt is named
// <batch>.<nanos>.json and carries the outcome.
func (d *deadLetter) receipts(ctx context.Context) (map[string]string, error) {
	var names []string
	if d.store == nil {
		entries, err := os.ReadDir(filepath.Join(d.path, resentDir))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, e := range entries {
			names = append(names, e.Name())
		}
	} else {
		// An object store lists an empty prefix as empty, not as missing.
		all, err := d.store.List(ctx, d.bucket, d.prefix+resentDir+"/")
		if err != nil {
			return nil, err
		}
		for _, k := range all {
			names = append(names, path.Base(k))
		}
	}
	// <id>.<nanos>.json, and an id can have dots of its own -- an object name
	// for a batch buried before rows named one -- so it is cut from the right.
	// Sorted, so the newest receipt of a batch is read last and wins.
	sort.Strings(names)
	out := map[string]string{}
	for _, n := range names {
		base, ok := strings.CutSuffix(n, ".json")
		if !ok || strings.HasPrefix(n, ".") {
			continue
		}
		i := strings.LastIndex(base, ".")
		if i <= 0 {
			continue
		}
		outcome, err := d.receiptOutcome(ctx, n)
		if err != nil {
			return nil, err
		}
		out[base[:i]] = outcome
	}
	return out, nil
}

func (d *deadLetter) receiptOutcome(ctx context.Context, name string) (string, error) {
	r, err := d.open(ctx, resentDir+"/"+name)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	var rc struct {
		Outcome string `json:"outcome"`
	}
	if err := json.NewDecoder(r).Decode(&rc); err != nil {
		return "", fmt.Errorf("%s/%s: %w", resentDir, name, err)
	}
	return rc.Outcome, nil
}

func (d *deadLetter) open(ctx context.Context, object string) (io.ReadCloser, error) {
	if d.store == nil {
		return os.Open(filepath.Join(d.path, object)) //nolint:gosec // the dead letter the config names
	}
	return d.store.Open(ctx, d.bucket, d.prefix+object)
}

// rows reads one object's rows, in order.
func (d *deadLetter) rows(ctx context.Context, object string) ([]map[string]any, error) {
	r, err := d.open(ctx, object)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("%s: a line is not a JSON object: %w", object, err)
		}
		out = append(out, row)
	}
	return out, sc.Err()
}

// summarize turns one object's rows into the line ls prints.
func summarize(object string, rows []map[string]any) buriedBatch {
	b := buriedBatch{Object: object, Rows: len(rows)}
	if len(rows) == 0 {
		return b
	}
	first := rows[0]
	b.Batch, _ = first[ColumnDeadLetterBatch].(string)
	b.Stream, _ = first[ColumnDeadLetterStream].(string)
	b.Reason, _ = first["_dead_letter_reason"].(string)
	if at, ok := first["_dead_letter_at"].(string); ok {
		b.At, _ = time.Parse(time.RFC3339, at)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		id, _ := r[ColumnDeadLetterJobID].(string)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if b.Jobs == nil {
			b.Jobs = map[string]int{}
		}
		state, _ := r[ColumnDeadLetterJobState].(string)
		b.Jobs[state]++
	}
	return b
}

// listBuried reads every `files` dead letter of the config -- once per path,
// since streams may share one -- newest first. stream, when set, keeps the
// batches of that stream, and those of its dead letter that name no stream.
//
// A dead letter it cannot read does not hide the others: what was read comes
// back with an error naming what was not, because at two in the morning one
// unreachable bucket is not a reason to see nothing.
func listBuried(ctx context.Context, cfg *Config, stores *Stores, stream string) ([]buriedBatch, error) {
	var (
		out  []buriedBatch
		errs []error
	)
	read := map[string]bool{}
	found := false
	for _, st := range cfg.Streams {
		if stream != "" && st.Name != stream {
			continue
		}
		found = true
		if read[st.DeadLetter.Path] {
			continue
		}
		read[st.DeadLetter.Path] = true
		d, err := openDeadLetter(ctx, stores, st.DeadLetter)
		if err != nil {
			errs = append(errs, fmt.Errorf("stream %s: %w", st.Name, err))
			continue
		}
		objects, err := d.objects(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("stream %s: listing %s: %w", st.Name, d.path, err))
			continue
		}
		resent, err := d.receipts(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("stream %s: listing %s: %w", st.Name, path.Join(d.path, resentDir), err))
			continue
		}
		for _, o := range objects {
			rows, err := d.rows(ctx, o)
			if err != nil {
				errs = append(errs, fmt.Errorf("stream %s: %w", st.Name, err))
				continue
			}
			b := summarize(o, rows)
			if stream != "" && b.Stream != "" && b.Stream != stream {
				continue
			}
			b.Resent = resent[b.ID()]
			out = append(out, b)
		}
	}
	if stream != "" && !found {
		return nil, fmt.Errorf("the config has no stream %q", stream)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].Object > out[j].Object
	})
	return out, errors.Join(errs...)
}

// printBuried writes ls's table: what a person reads at two in the morning.
func printBuried(w io.Writer, batches []buriedBatch) error {
	if len(batches) == 0 {
		_, err := fmt.Fprintln(w, "nothing buried")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "BATCH\tSTREAM\tBURIED\tROWS\tJOBS\tRESENT\tREASON")
	for _, b := range batches {
		at := "-"
		if !b.At.IsZero() {
			at = b.At.UTC().Format("2006-01-02 15:04 UTC")
		}
		resent := dash(b.Resent)
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			short(b.ID()), dash(b.Stream), at, b.Rows, jobsLine(b.Jobs), resent, truncate(b.Reason, 60))
	}
	return tw.Flush()
}

func short(id string) string {
	if len(id) > 12 && !strings.Contains(id, ".") {
		return id[:12]
	}
	return id
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func jobsLine(jobs map[string]int) string {
	if len(jobs) == 0 {
		return "-"
	}
	states := make([]string, 0, len(jobs))
	for s := range jobs {
		states = append(states, s)
	}
	sort.Strings(states)
	parts := make([]string, len(states))
	for i, s := range states {
		parts[i] = fmt.Sprintf("%s:%d", s, jobs[s])
	}
	return strings.Join(parts, " ")
}

// deadletterMain is `gateway deadletter ...`.
func deadletterMain(ctx context.Context, args []string, o options, out io.Writer) error {
	usage := "usage: gateway deadletter ls [--stream <name>] [--json] <config.yaml>\n" +
		"       gateway deadletter resend [--stream <name>] [--dry-run] [--allow-duplicates] [--again] [--by <name>] <config.yaml> <batch|object>"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "ls":
		fs := flag.NewFlagSet("deadletter ls", flag.ContinueOnError)
		stream := fs.String("stream", "", "only this stream's batches")
		asJSON := fs.Bool("json", false, "print JSON, for a script")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New(usage)
		}
		cfg, err := Load(fs.Arg(0))
		if err != nil {
			return err
		}
		// What was read is printed even when something was not; the error
		// comes after it and makes the exit non-zero.
		batches, listErr := listBuried(ctx, cfg, o.stores, *stream)
		if *asJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if batches == nil {
				batches = []buriedBatch{}
			}
			if err := enc.Encode(batches); err != nil {
				return err
			}
		} else if err := printBuried(out, batches); err != nil {
			return err
		}
		return listErr
	case "resend":
		fs := flag.NewFlagSet("deadletter resend", flag.ContinueOnError)
		var req resendRequest
		fs.StringVar(&req.Stream, "stream", "", "the stream, when the batch's rows do not say")
		fs.BoolVar(&req.DryRun, "dry-run", false, "say what would be sent, and send nothing")
		fs.BoolVar(&req.AllowDuplicates, "allow-duplicates", false,
			"resend rows with no load job under write: append, knowing they may land twice")
		fs.BoolVar(&req.Again, "again", false, "resend a batch a previous resend delivered")
		fs.StringVar(&req.By, "by", os.Getenv("USER"), "who is resending, for the receipt")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 2 || strings.TrimSpace(fs.Arg(1)) == "" {
			return errors.New(usage)
		}
		req.ID = fs.Arg(1)
		cfg, err := Load(fs.Arg(0))
		if err != nil {
			return err
		}
		_, err = resendBatch(ctx, cfg, o, req, out)
		return err
	default:
		return errors.New(usage)
	}
}
