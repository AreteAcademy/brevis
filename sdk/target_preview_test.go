package sdk_test

import (
	"bytes"
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
)

// rowsOf is a source that yields exactly what it was given.
type rowsOf []any

func (rowsOf) Describe() string { return "rows under test" }
func (r rowsOf) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	return func(yield func(sdk.Envelope, error) bool) {
		for _, p := range r {
			if !yield(sdk.Envelope{Payload: p}, nil) {
				return
			}
		}
	}, nil
}

// previewTarget accepts anything; these tests are about what is PRINTED.
type previewTarget struct{}

func (previewTarget) Describe() string { return "target under test" }
func (previewTarget) Write(_ context.Context, envs []sdk.Envelope, _ sdk.WriteOptions) (*sdk.LoadResult, error) {
	return &sdk.LoadResult{RowsLoaded: int64(len(envs))}, nil
}

func extracted(t *testing.T, rows ...any) *sdk.Data {
	t.Helper()
	data, err := sdk.Extract(context.Background(), sdk.Source{From: rowsOf(rows)})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The preview shows the row the DESTINATION will see, not the row the source
// produced. Everything below turns on that difference.
func TestTargetPreviewShowsTheProjectedRow(t *testing.T) {
	var out bytes.Buffer
	data := extracted(t,
		map[string]any{"id": "A1", "keep": 10, "drop_me": "secret"},
		map[string]any{"id": "A2", "keep": 20, "drop_me": "secret"})

	if _, err := sdk.Load(context.Background(), data, sdk.Target{
		To:            previewTarget{},
		Columns:       []string{"id", "keep"},
		Preview:       5,
		PreviewWriter: &out,
	}); err != nil {
		t.Fatal(err)
	}
	got := out.String()

	if !strings.Contains(got, "A1") || !strings.Contains(got, "A2") {
		t.Errorf("the rows are missing:\n%s", got)
	}
	// The column is not declared, so it is not written -- and a preview that
	// showed it would be describing a row that never lands.
	if strings.Contains(got, "drop_me") || strings.Contains(got, "secret") {
		t.Errorf("an undeclared column reached the preview:\n%s", got)
	}
}

// Columns is an ORDER, and it is the table's. Sorting it alphabetically would
// print a table that is not the table.
func TestTargetPreviewKeepsTheDeclaredOrder(t *testing.T) {
	var out bytes.Buffer
	data := extracted(t, map[string]any{"zebra": 1, "alpha": 2, "middle": 3})

	if _, err := sdk.Load(context.Background(), data, sdk.Target{
		To:            previewTarget{},
		Columns:       []string{"zebra", "middle", "alpha"},
		Preview:       1,
		PreviewWriter: &out,
	}); err != nil {
		t.Fatal(err)
	}

	header := strings.SplitN(out.String(), "\n", 2)[0]
	z, m, a := strings.Index(header, "zebra"), strings.Index(header, "middle"), strings.Index(header, "alpha")
	if z >= m || m >= a {
		t.Errorf("declared zebra, middle, alpha and got %q", header)
	}
}

// A declared column no record carries is the single most useful thing this
// feature shows: it is why a load writes NULL, or is refused.
func TestTargetPreviewShowsAColumnNobodyFilled(t *testing.T) {
	var out bytes.Buffer
	data := extracted(t, map[string]any{"id": "A1"})

	_, _ = sdk.Load(context.Background(), data, sdk.Target{
		To:            previewTarget{},
		Columns:       []string{"id", "never_set"},
		Preview:       1,
		PreviewWriter: &out,
	})

	if !strings.Contains(out.String(), "never_set") {
		t.Errorf("the empty column is not on the table:\n%s", out.String())
	}
}

// Zero is off, and off has to mean silent: a preview nobody asked for would
// land in the logs of every production load.
func TestTargetPreviewIsSilentAtZero(t *testing.T) {
	var out bytes.Buffer
	data := extracted(t, map[string]any{"id": "A1"})

	if _, err := sdk.Load(context.Background(), data, sdk.Target{
		To:            previewTarget{},
		Columns:       []string{"id"},
		PreviewWriter: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("Preview is 0 and it printed:\n%s", out.String())
	}
}

// An empty load is exactly when somebody needs to look. A run that reads
// thousands of records and writes none says nothing about why, and a preview
// that stayed quiet on that path would be quiet on the only run that matters.
func TestTargetPreviewPrintsWhenNothingIsGoingToBeWritten(t *testing.T) {
	var out bytes.Buffer
	data := extracted(t)

	if _, err := sdk.Load(context.Background(), data, sdk.Target{
		To:            previewTarget{},
		Columns:       []string{"id"},
		Preview:       5,
		PreviewWriter: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no rows") {
		t.Errorf("an empty load printed %q", out.String())
	}
}

// -preview-target turns it on from the command line, so an operator can look at
// a real load without a rebuild. It must not be -preview: that one samples the
// SOURCE, and the two answer different questions.
func TestThePreviewTargetFlag(t *testing.T) {
	var out bytes.Buffer
	p := &sdk.Pipeline{
		Name:   "flag under test",
		Source: sdk.Source{From: rowsOf{map[string]any{"id": "A1", "keep": 7}}},
		Target: sdk.Target{
			To: previewTarget{}, Columns: []string{"id", "keep"}, PreviewWriter: &out,
		},
	}
	if err := sdk.Execute(context.Background(), p, []string{"-preview-target", "3"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "A1") {
		t.Errorf("-preview-target printed nothing:\n%q", out.String())
	}

	// And -preview must NOT reach the target: it is the source's flag.
	var other bytes.Buffer
	q := &sdk.Pipeline{
		Name:   "flag under test",
		Source: sdk.Source{From: rowsOf{map[string]any{"id": "A1"}}},
		Target: sdk.Target{To: previewTarget{}, Columns: []string{"id"}, PreviewWriter: &other},
	}
	if err := sdk.Execute(context.Background(), q, []string{"-preview", "3"}); err != nil {
		t.Fatal(err)
	}
	if other.Len() != 0 {
		t.Errorf("-preview wrote to the target's preview:\n%q", other.String())
	}
}
