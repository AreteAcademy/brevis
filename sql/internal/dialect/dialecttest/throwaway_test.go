package dialecttest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/dialecttest"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// recorder answers nothing and remembers what it was asked to run.
type recorder struct{ ran []string }

func (r *recorder) Exec(_ context.Context, s string) error { r.ran = append(r.ran, s); return nil }
func (r *recorder) Scalar(context.Context, string) (any, error) {
	return nil, nil
}
func (r *recorder) Target(ref string) string { return "spy://" + ref }
func (r *recorder) Close(context.Context) error {
	return nil
}

// A WAREHOUSE THAT CAN BE TOLD IS TOLD, and immediately -- the expiry applies
// to tables created after it, so a helper that ran it last would leave
// everything the test built behind.
func TestAThrowawaySchemaExpiresWhereItCan(t *testing.T) {
	spy := &recorder{}
	if err := dialecttest.MakeThrowawaySchema(
		context.Background(), bigquery.Dialect{}, spy, "bvs_x"); err != nil {
		t.Fatal(err)
	}
	if len(spy.ran) != 2 {
		t.Fatalf("%d statements, wanted the create and the expiry:\n%s",
			len(spy.ran), strings.Join(spy.ran, "\n"))
	}
	if !strings.HasPrefix(spy.ran[0], "CREATE SCHEMA") {
		t.Errorf("the first statement is not the create:\n%s", spy.ran[0])
	}
	if !strings.Contains(spy.ran[1], "default_table_expiration_days") {
		t.Errorf("the second statement does not expire anything:\n%s", spy.ran[1])
	}
}

// AND ONE THAT CANNOT BE TOLD IS NOT ASKED. The Postgres path has to emit
// exactly what it emitted before this existed: its test warehouse is a
// container, and a statement that did nothing would be a round trip per run
// with a comment explaining that it is pointless.
func TestAWarehouseThatCannotBeToldIsNotAsked(t *testing.T) {
	spy := &recorder{}
	if err := dialecttest.MakeThrowawaySchema(
		context.Background(), postgres.Dialect{}, spy, "bvs_x"); err != nil {
		t.Fatal(err)
	}
	want := postgres.Dialect{}.EnsureSchema("bvs_x")
	if len(spy.ran) != len(want) {
		t.Fatalf("%d statements, wanted %d -- the Postgres path grew one:\n%s",
			len(spy.ran), len(want), strings.Join(spy.ran, "\n"))
	}
	for i := range want {
		if spy.ran[i] != want[i] {
			t.Errorf("statement %d is %q, wanted %q", i, spy.ran[i], want[i])
		}
	}
}

// A FAILED CREATE IS REPORTED AND THE EXPIRY IS NOT ATTEMPTED. Running ALTER
// against a dataset that was never made produces a second error about the
// first one's cause, and the message somebody reads is the wrong one.
func TestTheExpiryIsNotAttemptedWhenTheCreateFailed(t *testing.T) {
	spy := &failing{}
	err := dialecttest.MakeThrowawaySchema(
		context.Background(), bigquery.Dialect{}, spy, "bvs_x")
	if err == nil {
		t.Fatal("a failed create was reported as success")
	}
	if !strings.Contains(err.Error(), "bvs_x") {
		t.Errorf("the error does not name the schema: %v", err)
	}
	if len(spy.ran) != 1 {
		t.Errorf("it went on after the create failed: %v", spy.ran)
	}
}

type failing struct{ ran []string }

func (f *failing) Exec(_ context.Context, s string) error {
	f.ran = append(f.ran, s)
	return errFailed
}
func (f *failing) Scalar(context.Context, string) (any, error) { return nil, nil }
func (f *failing) Target(ref string) string                    { return ref }
func (f *failing) Close(context.Context) error                 { return nil }

var errFailed = &testError{"the warehouse said no"}

type testError struct{ s string }

func (e *testError) Error() string { return e.s }

var _ dialect.Conn = (*recorder)(nil)
var _ dialect.Conn = (*failing)(nil)
