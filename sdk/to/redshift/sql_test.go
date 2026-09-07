package redshift

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// This file is what can be tested without a cluster, and it is deliberately the
// bulk of the driver: the SQL generation as a pure function.
//
// The reason is written in v0.12.0 -- SQL built inside a method that held a
// client had never been seen by a test, and shipped with a positional match.

func TestCopySQLUsesTheRoleAndNotAKey(t *testing.T) {
	got := CopySQL("landing.pedidos", "s3://b/k.ndjson", "arn:aws:iam::1:role/r")
	for _, required := range []string{
		"COPY landing.pedidos FROM 's3://b/k.ndjson'",
		"IAM_ROLE 'arn:aws:iam::1:role/r'",
		"FORMAT AS JSON 'auto'",
	} {
		if !strings.Contains(got, required) {
			t.Errorf("missing %q:\n%s", required, got)
		}
	}
}

// TestMergeSQLNamesTheColumns: named, always. The alternative already happened
// -- BigQuery's INSERT ROW matches by position, and v0.12.0 shipped with the
// columns swapped because nobody had seen the generated SQL.
func TestMergeSQLNamesTheColumns(t *testing.T) {
	got := MergeSQL("destino", "brevis_stage", []string{"ingestion_id", "valor"})

	want := `MERGE INTO destino USING brevis_stage ` +
		`ON destino."ingestion_id" = brevis_stage."ingestion_id" ` +
		`WHEN NOT MATCHED THEN INSERT ("ingestion_id", "valor") ` +
		`VALUES (brevis_stage."ingestion_id", brevis_stage."valor")`
	if got != want {
		t.Errorf("SQL:\n  got  %s\n  want %s", got, want)
	}
}

// TestMergeSQLMatchesOnIngestionID: swapping the join column would make the
// dedup match on the wrong thing, in silence.
func TestMergeSQLMatchesOnIngestionID(t *testing.T) {
	got := MergeSQL("d", "s", []string{"a"})
	if !strings.Contains(got, `d."`+core.MetadataID+`" = s."`+core.MetadataID+`"`) {
		t.Errorf("the join is not on %s:\n%s", core.MetadataID, got)
	}
}

// TestMergeSQLQuotesAReservedWord.
func TestMergeSQLQuotesAReservedWord(t *testing.T) {
	got := MergeSQL("d", "s", []string{"order"})
	if strings.Count(got, `"order"`) < 2 {
		t.Errorf("the reserved word is not quoted on both sides:\n%s", got)
	}
}

// TestStagingTableUsesLike: a hand-written column list is what makes the MERGE
// fail months later, when somebody adds a column to the destination.
func TestStagingTableUsesLike(t *testing.T) {
	got := StagingTableSQL("landing.pedidos", "brevis_stage")
	if !strings.Contains(got, "LIKE landing.pedidos") {
		t.Errorf("the temporary table does not follow the destination:\n%s", got)
	}
	if !strings.Contains(got, "TEMP") {
		t.Errorf("the staging table is not temporary:\n%s", got)
	}
}

// TestEncodeNDJSONOneLinePerRecord.
func TestEncodeNDJSONOneLinePerRecord(t *testing.T) {
	envelopes := []core.Envelope{
		{Payload: map[string]any{"a": 1, "b": "x", "sobra": true}},
		{Payload: map[string]any{"a": 2}},
	}
	b, err := EncodeNDJSON(envelopes, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2:\n%s", len(lines), b)
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	// Only the declared columns go in: one extra field would make the COPY with
	// 'auto' try a column that does not exist.
	if _, ok := first["sobra"]; ok {
		t.Errorf("a field outside the declaration reached the file: %v", first)
	}
	if first["a"] != float64(1) || first["b"] != "x" {
		t.Errorf("line 1 = %v", first)
	}

	// A column the record does not carry simply does not appear; the COPY with
	// 'auto' leaves the column NULL, which is legitimate in a landing table.
	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if _, ok := second["b"]; ok {
		t.Errorf("line 2 invented column b: %v", second)
	}
}

// TestEncodeNDJSONDoesNotPayTheMapPath compares BOTH paths in the same process,
// and that is the only form that holds up.
//
// This test has been wrong twice, both times by measuring the wrong thing:
//
//  1. the first version compared 2000 lines against 200 and demanded a ratio
//     below 10 -- which with any linear cost is exactly 10, impossible to pass
//     one way and fail the other;
//  2. the second pinned an absolute per-line ceiling, measured with the local
//     toolchain. CI runs another, and escape analysis changed between them:
//     0.005 per line on 1.25 became 2.00 on 1.27, with nothing in the code
//     changing.
//
// An absolute allocation count is not a property of the code; it is a property
// of the code PLUS the compiler. What belongs to the code is the difference
// between the two strategies -- and measuring both under the same compiler, it
// holds up on any of them.
func TestEncodeNDJSONDoesNotPayTheMapPath(t *testing.T) {
	const lines = 2000
	envelopes := make([]core.Envelope, lines)
	for i := range envelopes {
		envelopes[i] = core.Envelope{Payload: map[string]any{"a": i, "b": "texto"}}
	}
	columns := []string{"a", "b"}

	direct := testing.AllocsPerRun(5, func() {
		if _, err := EncodeNDJSON(envelopes, columns); err != nil {
			t.Fatal(err)
		}
	})
	viaMap := testing.AllocsPerRun(5, func() {
		if _, err := encodeViaMap(envelopes, columns); err != nil {
			t.Fatal(err)
		}
	})

	// The margin is generous on purpose: what is claimed is that the direct path
	// is substantially cheaper, not an exact number the next Go would
	// invalidate.
	if direct*2 > viaMap {
		t.Errorf("the direct path costs %.0f allocations and the map path %.0f for %d lines; "+
			"the advantage is gone -- or EncodeNDJSON went back to building a map per record",
			direct, viaMap, lines)
	}
	t.Logf("direct %.0f, via map %.0f (%.1fx) for %d lines",
		direct, viaMap, viaMap/direct, lines)
}

// encodeViaMap is the path EncodeNDJSON had before: one map[string]any per
// record, handed to the json.Encoder.
//
// It lives in the test, and not in the production code, because it is the
// reference the gain is measured against -- and because a reference that lives
// in the test cannot be used by mistake.
func encodeViaMap(envelopes []core.Envelope, columns []string) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(len(envelopes) * 128)
	enc := json.NewEncoder(&buf)

	for i, e := range envelopes {
		obj, err := core.AsObject(e.Payload)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", i+1, err)
		}
		row := make(map[string]any, len(columns))
		for _, c := range columns {
			if v, ok := obj[c]; ok {
				row[c] = v
			}
		}
		if err := enc.Encode(row); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// fakeExecutor records the SQL, and is how the whole driver is testable without
// a cluster -- which is the only way, because there is no Redshift image.
type fakeExecutor struct{ sqls []string }

func (e *fakeExecutor) Exec(_ context.Context, sql string) error {
	e.sqls = append(e.sqls, sql)
	return nil
}

type fakeStore struct {
	bucket, key string
	deleted     bool
}

func (s *fakeStore) Scheme() string { return "s3" }
func (s *fakeStore) List(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (s *fakeStore) Open(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}
func (s *fakeStore) Create(_ context.Context, bucket, key string, r io.Reader) error {
	s.bucket, s.key = bucket, key
	_, _ = io.ReadAll(r)
	return nil
}
func (s *fakeStore) Delete(context.Context, string, string) error {
	s.deleted = true
	return nil
}

// TestTheOrderOfTheCommands proves the whole sequence without a cluster:
// staging, COPY into the temporary table, MERGE, DROP.
func TestTheOrderOfTheCommands(t *testing.T) {
	exec := &fakeExecutor{}
	store := &fakeStore{}

	table := Table{
		Name: "landing.pedidos", Staging: "s3://b/stage/",
		IAMRole: "arn:aws:iam::1:role/r", Store: store, Executor: exec,
	}
	_, err := table.Write(context.Background(),
		[]core.Envelope{{Payload: map[string]any{core.MetadataID: "x", "a": 1}}},
		core.WriteOptions{Dedup: core.DedupMerge, Columns: []string{core.MetadataID, "a"}})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if len(exec.sqls) != 4 {
		t.Fatalf("%d commands, want 4:\n%s", len(exec.sqls), strings.Join(exec.sqls, "\n"))
	}
	prefixes := []string{"CREATE TEMP TABLE", "COPY brevis_stage", "MERGE INTO", "DROP TABLE"}
	for i, p := range prefixes {
		if !strings.HasPrefix(exec.sqls[i], p) {
			t.Errorf("command %d = %q, want it to start with %q", i, exec.sqls[i], p)
		}
	}
	if !store.deleted {
		t.Error("the staging file was not deleted")
	}
}

// TestWithoutDedupItCopiesStraightIntoTheDestination: with no dedup there is no
// temporary table and no MERGE, and a temporary table created for nothing is
// work nobody asked for.
func TestWithoutDedupItCopiesStraightIntoTheDestination(t *testing.T) {
	exec := &fakeExecutor{}
	table := Table{
		Name: "landing.pedidos", Staging: "s3://b/stage/",
		IAMRole: "arn:aws:iam::1:role/r", Store: &fakeStore{}, Executor: exec,
	}
	if _, err := table.Write(context.Background(),
		[]core.Envelope{{Payload: map[string]any{"a": 1}}},
		core.WriteOptions{Columns: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if len(exec.sqls) != 1 || !strings.HasPrefix(exec.sqls[0], "COPY landing.pedidos") {
		t.Errorf("commands = %v", exec.sqls)
	}
}

// TestKeepStagedFileKeepsIt.
func TestKeepStagedFileKeepsIt(t *testing.T) {
	store := &fakeStore{}
	table := Table{
		Name: "t", Staging: "s3://b/stage/", IAMRole: "arn:aws:iam::1:role/r",
		Store: store, Executor: &fakeExecutor{}, KeepStagedFile: true,
	}
	if _, err := table.Write(context.Background(),
		[]core.Envelope{{Payload: map[string]any{"a": 1}}},
		core.WriteOptions{Columns: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if store.deleted {
		t.Error("it deleted even with KeepStagedFile")
	}
}

// TestAnAccessKeyIsRefused: a key in the COPY's string ends up in the cluster's
// query log, which plenty of people read.
func TestAnAccessKeyIsRefused(t *testing.T) {
	table := Table{
		Name: "t", Staging: "s3://b/s/", Store: &fakeStore{}, Executor: &fakeExecutor{},
		IAMRole: "aws_access_key_id=AKIA;aws_secret_access_key=xyz",
	}
	_, err := table.Write(context.Background(),
		[]core.Envelope{{Payload: map[string]any{"a": 1}}}, core.WriteOptions{})
	if err == nil {
		t.Fatal("an access key got through")
	}
	if !strings.Contains(err.Error(), "query log") {
		t.Errorf("the error does not say why: %v", err)
	}
}

// TestTheRequiredFieldsAreNamed: there is no inline path on Redshift, and the
// error has to say so rather than listing fields with no context.
func TestTheRequiredFieldsAreNamed(t *testing.T) {
	_, err := Table{}.Write(context.Background(),
		[]core.Envelope{{Payload: map[string]any{"a": 1}}}, core.WriteOptions{})
	if err == nil {
		t.Fatal("an empty configuration got through")
	}
	for _, required := range []string{"DSN", "Name", "Staging", "IAMRole", "Store", "no inline path"} {
		if !strings.Contains(err.Error(), required) {
			t.Errorf("the error does not say %q: %v", required, err)
		}
	}
}
