package bigquery_test

import (
	"os"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect/bigquery"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/dialecttest"
)

// THE SAME SUITE, against a real BigQuery project.
//
// The plan's S5 said to say where this cannot be proven here, because the
// project's emulator runs queries on DuckDB and the spike's verdict already
// warned that a MERGE there tests DuckDB. It did not have to be said in the
// end: `table` and `view` are not MERGE, and a real sandbox exists -- so
// this runs against the warehouse itself and proves the thing rather than
// the emulation of it.
//
//	BREVIS_SQL_IT_BQ_PROJECT=my-dev-project go test ./internal/dialect/bigquery/
//
// It CREATES AND DROPS A DATASET of its own, named `bvs_conf_<nanos>`, and
// touches nothing else in the project. It is gated on its own variable
// rather than on the DSN the Postgres suite uses, so pointing a shell at one
// never silently runs the other against a warehouse somebody is paying for.
func TestBigQueryConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped under -short")
	}
	project := os.Getenv("BREVIS_SQL_IT_BQ_PROJECT")
	if project == "" {
		t.Skip("BREVIS_SQL_IT_BQ_PROJECT not set; skipping the conformance suite")
	}
	dialecttest.Run(t, bigquery.Dialect{}, project)
}
