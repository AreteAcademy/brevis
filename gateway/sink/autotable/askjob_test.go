package autotable

import (
	"context"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
)

// #86: a resend asks what a buried row's load job became before sending it
// again. The row names the job, not the table; the router reads the table off
// the row and asks that table's sink.
func TestAResendCanAskTheJobOfABuriedRow(t *testing.T) {
	r := routerOverJobs(t, map[string]jobSink{
		"tbl_a": {table: "tbl_a", landed: true},
		"tbl_b": {table: "tbl_b", landed: false},
	})
	rows := batchFor("tbl_a", "tbl_b")

	for i, want := range []bool{true, false} {
		row := rows[i].Payload.(map[string]any)
		je := &sdk.LoadJobError{JobID: "job_x", State: sdk.JobRunning}
		rec, err := gateway.AskJob(context.Background(), r, je, row)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Landed != want {
			t.Errorf("row %d: landed = %v, wanted %v", i, rec.Landed, want)
		}
	}
}
