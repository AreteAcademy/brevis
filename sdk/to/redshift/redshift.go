// Package redshift writes records into Amazon Redshift.
//
// # What this driver does NOT have, and why you need to know
//
// There is no Redshift image to run locally. So, unlike every other destination
// in this SDK, it ships with **partial verification**:
//
//	tested without a cluster   the SQL generation (COPY and MERGE), as a pure
//	                           function, and writing the staging file to S3
//	NOT tested                 that the cluster accepts that SQL
//
// This is here, and not in a footnote, because it is the information that
// changes the decision of whoever is about to use it. What can be tested without
// a cluster is exactly what mergeSQL and BigQuery's reconcile have already
// proven worth testing: SQL assembled inside a method holding a client had never
// been seen by a test, and that is how v0.12.0 shipped with positional
// matching.
package redshift

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Table loads records into a Redshift table, via COPY from S3.
//
//	To: redshift.Table{
//	    DSN:     os.Getenv("RS_DSN"),
//	    Name:    "landing.orders",
//	    Staging: "s3://my-bucket/stage/",
//	    IAMRole: "arn:aws:iam::123456789012:role/redshift-copy",
//	    Store:   s3.New(client),
//	}
//
// Row-by-row INSERT into Redshift is unworkable: it is a columnar database, and
// each INSERT pays the cost of a block. The right load is COPY from S3, which is
// why the files driver comes first in the roadmap -- the staging layer is the
// same one.
type Table struct {
	// DSN is the connection to the cluster, in Postgres' dialect. Required.
	DSN string

	// Name is the table, schema included. Required.
	Name string

	// Staging is the S3 prefix the batch is written to before the COPY.
	// Required: there is no inline path into Redshift.
	Staging string

	// IAMRole is the role the cluster assumes to read S3. Required.
	//
	// A role, and not an access key, on purpose: a key in the COPY's URL ends up
	// in the cluster's query log, which plenty of people read. This driver does
	// not accept a key -- if you need one, its place is behind the role.
	IAMRole string

	// Store writes the staging file. Required; use store/s3.
	Store core.Store

	// KeepStagedFile leaves the file in S3 after the COPY, for inspection.
	KeepStagedFile bool

	// Executor runs the SQL on the cluster. Nil opens a connection from the DSN.
	//
	// It exists so the SQL generation is testable without a cluster, which is
	// the only part of this driver that can be tested without one.
	Executor SQLExecutor
}

// SQLExecutor is the minimum this driver needs from a connection.
type SQLExecutor interface {
	Exec(ctx context.Context, sql string) error
}

// Describe satisfies core.Writer. It names the table, never the DSN nor the
// role.
func (t Table) Describe() string { return "redshift:" + t.Name }

// Write satisfaz core.Writer.
func (t Table) Write(ctx context.Context, envelopes []core.Envelope, opt core.WriteOptions) (*core.LoadResult, error) {
	res := &core.LoadResult{Dedup: opt.Dedup, Strategy: "copy", Format: string(core.FormatNDJSON)}
	if opt.Dedup == "" {
		res.Dedup = core.DedupNone
	}
	start := time.Now()
	fail := func(err error) (*core.LoadResult, error) {
		res.Duration = time.Since(start)
		return res, err
	}

	if err := t.check(); err != nil {
		return fail(err)
	}
	if len(envelopes) == 0 {
		return fail(nil)
	}
	if err := core.CheckColumns(opt.Columns, envelopes); err != nil {
		return fail(err)
	}

	// The columns come from the declaration and not from the batch: on Redshift
	// the SDK does not read the schema before loading, so the declaration IS the
	// contract -- and CheckColumns has already checked it against the whole
	// batch.
	columns := opt.Columns
	if len(columns) == 0 {
		columns = fieldsOf(envelopes)
	}

	payload, err := EncodeNDJSON(envelopes, columns)
	if err != nil {
		return fail(err)
	}
	res.BytesStaged = int64(len(payload))

	loc, err := core.ParseLocation(t.Staging)
	if err != nil {
		return fail(fmt.Errorf("redshift: Staging: %w", err))
	}
	key := strings.TrimSuffix(loc.Prefix, "/") + "/" +
		fmt.Sprintf("brevis-%d.ndjson", time.Now().UnixNano())
	key = strings.TrimPrefix(key, "/")

	if err := t.Store.Create(ctx, loc.Bucket, key, bytes.NewReader(payload)); err != nil {
		return fail(fmt.Errorf("redshift: staging to s3://%s/%s: %w", loc.Bucket, key, err))
	}
	uri := "s3://" + loc.Bucket + "/" + key

	if t.KeepStagedFile {
		// Only when it stays. Reporting a path the cleanup will delete would be
		// worse than reporting none: somebody would try to read it.
		res.Objects = []string{uri}
	} else {
		defer func() {
			// The staging file stays if the cleanup fails: losing the load over a
			// DELETE would trade a small problem for a big one. The warning is
			// what says it stayed.
			if err := t.remove(ctx, loc.Bucket, key); err != nil {
				avisarSobra(ctx, uri, err)
			}
		}()
	}

	exec, closeIt, err := t.executor(ctx)
	if err != nil {
		return fail(err)
	}
	defer closeIt()

	commands := []string{CopySQL(t.copyTarget(res.Dedup), uri, t.IAMRole)}
	if res.Dedup == core.DedupMerge {
		commands = append([]string{StagingTableSQL(t.Name, tempName)},
			commands...)
		commands = append(commands, MergeSQL(t.Name, tempName, columns), DropSQL(tempName))
	}

	for _, sql := range commands {
		if err := exec.Exec(ctx, sql); err != nil {
			return fail(fmt.Errorf("redshift: %w", err))
		}
	}

	res.RowsLoaded = int64(len(envelopes))
	return fail(nil)
}

const tempName = "brevis_stage"

func (t Table) copyTarget(d core.Dedup) string {
	if d == core.DedupMerge {
		return tempName
	}
	return t.Name
}

func (t Table) check() error {
	missing := []string{}
	if t.DSN == "" && t.Executor == nil {
		missing = append(missing, "DSN")
	}
	if t.Name == "" {
		missing = append(missing, "Name")
	}
	if t.Staging == "" {
		missing = append(missing, "Staging")
	}
	if t.IAMRole == "" {
		missing = append(missing, "IAMRole")
	}
	if t.Store == nil {
		missing = append(missing, "Store")
	}
	if len(missing) > 0 {
		return fmt.Errorf("redshift.Table needs %s. There is no inline path on Redshift: "+
			"the batch goes to S3 and the cluster COPYs it, which is why Staging and IAMRole "+
			"are not optional", strings.Join(missing, ", "))
	}
	if strings.Contains(t.IAMRole, "aws_access_key_id") ||
		strings.Contains(t.IAMRole, "ACCESS_KEY") {
		return fmt.Errorf("redshift.Table.IAMRole looks like an access key. This driver takes " +
			"a role ARN only: a key in the COPY statement lands in the cluster's query log, " +
			"which many people can read")
	}
	return nil
}

// EncodeNDJSON serialises the batch into the format COPY reads.
//
// Exported so it is testable without a cluster, and written with a single
// growing buffer: a batch of hundreds of thousands of rows must not allocate a
// buffer per record.
func EncodeNDJSON(envelopes []core.Envelope, columns []string) ([]byte, error) {
	var buf bytes.Buffer
	// A rough estimate, only to avoid the first few growths.
	buf.Grow(len(envelopes) * 128)

	// The keys are the same on every row, so they are serialised ONCE.
	// Handing a map[string]any to json.Encoder per record cost five allocations
	// per row -- the encoder sorts the keys and boxes every value, and none of
	// that changes between records.
	keys := make([][]byte, len(columns))
	for i, c := range columns {
		b, err := json.Marshal(c)
		if err != nil {
			return nil, fmt.Errorf("redshift: column %q: %w", c, err)
		}
		keys[i] = append(b, ':')
	}

	enc := json.NewEncoder(&buf)
	// COPY reads JSON, not HTML: escaping < and > would only make the file bigger.
	enc.SetEscapeHTML(false)

	for i, e := range envelopes {
		obj, err := core.AsObject(e.Payload)
		if err != nil {
			return nil, fmt.Errorf("redshift: row %d: %w", i+1, err)
		}

		buf.WriteByte('{')
		first := true
		for j, c := range columns {
			v, tem := obj[c]
			if !tem {
				// A missing column does not become null: with FORMAT AS JSON
				// 'auto' the absence leaves the column NULL, and writing an
				// explicit null would cost bytes without changing anything.
				continue
			}
			if !first {
				buf.WriteByte(',')
			}
			first = false
			buf.Write(keys[j])
			if !writeScalar(&buf, v) {
				// Composite: the encoder handles it, and pays one allocation.
				if err := enc.Encode(v); err != nil {
					return nil, fmt.Errorf("redshift: row %d, column %q: %w", i+1, c, err)
				}
				// Encode ends in \n, which here is a ROW separator and must not
				// appear in the middle of the object.
				buf.Truncate(buf.Len() - 1)
			}
		}
		buf.WriteString("}\n")
	}
	return buf.Bytes(), nil
}

// CopySQL builds the COPY.
//
// FORMAT AS JSON 'auto' matches by field NAME, which is the opposite of what
// BigQuery's INSERT ROW does -- and that is why the risk that cost v0.12.0 does
// not exist here. What does exist is the role: an access key in this string would
// end up in the cluster's query log.
func CopySQL(target, uri, role string) string {
	return fmt.Sprintf("COPY %s FROM '%s' IAM_ROLE '%s' FORMAT AS JSON 'auto' TIMEFORMAT 'auto'",
		target, uri, role)
}

// StagingTableSQL creates the temporary table with the SAME shape as the
// destination.
//
// LIKE, and not a hand-written column list: a temporary table that does not
// follow the destination is the one that makes the MERGE fail months later, when
// somebody adds a column.
func StagingTableSQL(target, temp string) string {
	return fmt.Sprintf("CREATE TEMP TABLE %s (LIKE %s)", temp, target)
}

// MergeSQL builds the dedup's MERGE, with the column list NAMED.
//
// Always named, and the comment exists because the alternative already
// happened: BigQuery's `INSERT ROW` matches by POSITION, and v0.12.0 shipped
// with the columns swapped because nobody had seen the generated SQL.
func MergeSQL(target, source string, columns []string) string {
	names := make([]string, len(columns))
	values := make([]string, len(columns))
	for i, c := range columns {
		names[i] = quote(c)
		values[i] = source + "." + quote(c)
	}
	return fmt.Sprintf(
		"MERGE INTO %s USING %s ON %s.%s = %s.%s "+
			"WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)",
		target, source,
		target, quote(core.MetadataID), source, quote(core.MetadataID),
		strings.Join(names, ", "), strings.Join(values, ", "))
}

// DropSQL apaga a temporaria.
func DropSQL(temp string) string { return "DROP TABLE IF EXISTS " + temp }

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func fieldsOf(envelopes []core.Envelope) []string {
	vistos := map[string]bool{}
	for _, e := range envelopes {
		obj, err := core.AsObject(e.Payload)
		if err != nil {
			continue
		}
		for k := range obj {
			vistos[k] = true
		}
	}
	out := make([]string, 0, len(vistos))
	for k := range vistos {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
