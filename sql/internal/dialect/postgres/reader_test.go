package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
	"github.com/AreteAcademy/brevis/sql/internal/dialect/postgres"
)

// WHAT pgx HANDS OVER IS NOT WHAT A GRID CAN DRAW, and this is the list.
//
// Measured against a real server before it was fixed:
//
//	id   [16]uint8        [80 99 242 242 30 139 76 142 …]
//	d    time.Time        2026-10-09 00:00:00 +0000 UTC
//	num  pgtype.Numeric   {125 -2 false finite true}
//	b    []uint8          [65 66]
//
// A UUID as a byte array, a DATE carrying midnight, a number as a struct.
// The SDK had already learned every one of these in `sdk/from/postgres` --
// its own comments say "pgx hands DATE and TIMESTAMPTZ over as the same Go
// type" and "UUID: pgx hands the raw bytes over". Two readers on pgx in one
// repository, and only one of them knew.
func TestPostgresValuesArriveAsSomethingAGridCanDraw(t *testing.T) {
	dsn := os.Getenv("BREVIS_SQL_IT_DSN")
	if testing.Short() || dsn == "" {
		t.Skip("BREVIS_SQL_IT_DSN not set")
	}
	ctx := context.Background()
	conn, err := postgres.Dialect{}.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	got, err := conn.(dialect.Reader).Read(ctx, dialect.Request{Limit: 5, Query: `
		SELECT '0fb2b8f0-0b2e-4b5e-9c1a-2c3d4e5f6a7b'::uuid AS id,
		       '2026-10-09'::date                            AS d,
		       1.25::numeric                                 AS num,
		       '\x4142'::bytea                               AS b,
		       '{"a":1}'::jsonb                              AS j,
		       NULL::text                                    AS n`})
	if err != nil {
		t.Fatal(err)
	}

	// ASSERTED ON THE JSON, because that is the layer that exists. The
	// result crosses to the console as a JSON body and the template sees
	// what came out of it -- a first version of this test read
	// `fmt.Sprint` of the Go value and failed a bytea that was already
	// correct, because []byte prints as integers and marshals as base64.
	cell := map[string]json.RawMessage{}
	for i, c := range got.Columns {
		raw, err := json.Marshal(got.Rows[0][i])
		if err != nil {
			t.Fatalf("%s does not survive JSON: %v", c, err)
		}
		cell[c] = raw
	}
	for _, c := range []struct{ col, want string }{
		{"id", `"0fb2b8f0-0b2e-4b5e-9c1a-2c3d4e5f6a7b"`},
		{"d", `"2026-10-09"`},
		{"num", `"1.25"`},
		{"b", `"QUI="`}, // base64 of the two bytes, not a list of integers
		// AND NULL IS STILL NULL. Every conversion above is a chance to turn
		// "nothing was recorded" into a zero value, which is the one thing
		// the grid can never undo.
		{"n", `null`},
	} {
		if string(cell[c.col]) != c.want {
			t.Errorf("%s came back as %s, wanted %s", c.col, cell[c.col], c.want)
		}
	}
}
