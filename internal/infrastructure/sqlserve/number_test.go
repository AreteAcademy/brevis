package sqlserve_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

// A GRID THAT CHANGES A NUMBER IS A GRID THAT LIES.
//
// `Rows [][]any` is decoded from JSON, and encoding/json turns every JSON
// number into a float64 unless it is told otherwise. A float64 holds 53 bits
// of mantissa, so an id above 9007199254740992 comes back as a DIFFERENT id
// -- and anything large comes back in scientific notation, which is not what
// the warehouse holds either.
//
// Measured before it was fixed:
//
//	9007199254740993    -> "9.007199254740992e+15"   (the last digit moved)
//	1234567890123456789 -> "1.2345678901234568e+18"
//
// Postgres is where this bites: pgx hands a bigint over as an int64 and the
// service marshals it as a JSON number. BigQuery never does -- its REST API
// returns every scalar as a JSON STRING, measured against the live API -- so
// this was a defect you could only see on one of the two warehouses.
func TestALargeIntegerSurvivesTheWire(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"columns":["id","big","small","ratio"],` +
			`"rows":[[9007199254740993, 1234567890123456789, 42, 1.5]]}`))
	}))
	defer s.Close()

	res, err := sqlserve.New(s.URL, "").Query(
		context.Background(), "postgres://w/s/t", "SELECT 1", 10)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"9007199254740993", "1234567890123456789", "42", "1.5"} {
		if got := fmt.Sprint(res.Rows[0][i]); got != want {
			t.Errorf("column %d came back as %q, the warehouse said %q", i, got, want)
		}
	}
}

// AND NOTHING ELSE CHANGES SHAPE. `Bytes` and `Millis` are typed fields, and
// a decoder told to keep numbers as text must still fill them as numbers.
func TestTheMeasuredFiguresAreStillNumbers(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"columns":["n"],"rows":[["x"]],"bytes":4096,"ms":12,"limit":100}`))
	}))
	defer s.Close()

	res, err := sqlserve.New(s.URL, "").Query(
		context.Background(), "postgres://w/s/t", "SELECT 1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != 4096 || res.Millis != 12 || res.Limit != 100 {
		t.Errorf("the measured figures did not survive: %+v", res)
	}
}
