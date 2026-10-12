package bigquery

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sql/internal/dialect"
)

// ARGUMENTS TRAVEL NAMED AND TYPED, and the type is the half that is easy to
// get wrong. BigQuery reads an untyped parameter as STRING, and a STRING
// beside a DATE column is an error -- or, beside a STRING column holding
// '42', a match on the wrong thing. Each of the four Go types a parameter can
// be has exactly one BigQuery type, pinned here, and every value travels as a
// string because that is how the REST API carries scalars.
func TestArgumentsTravelNamedAndTyped(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":true,"schema":{"fields":[]},"rows":[],"totalRows":"0"}`)}
	c := dial(t, f)

	_, err := c.Read(context.Background(), dialect.Request{
		Query: "SELECT 1 WHERE @s = 'x'",
		Limit: 10,
		Args: []dialect.Arg{
			{Name: "s", Value: "it's"},
			{Name: "n", Value: int64(42)},
			{Name: "d", Value: dialect.Date{Year: 2026, Month: time.October, Day: 5}},
			{Name: "b", Value: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	sent := f.posts[0]
	if sent["parameterMode"] != "NAMED" {
		t.Errorf("parameterMode is %v, wanted NAMED", sent["parameterMode"])
	}
	got, _ := json.Marshal(sent["queryParameters"])
	want := `[{"name":"s","parameterType":{"type":"STRING"},"parameterValue":{"value":"it's"}},` +
		`{"name":"n","parameterType":{"type":"INT64"},"parameterValue":{"value":"42"}},` +
		`{"name":"d","parameterType":{"type":"DATE"},"parameterValue":{"value":"2026-10-05"}},` +
		`{"name":"b","parameterType":{"type":"BOOL"},"parameterValue":{"value":"true"}}]`
	if string(got) != want {
		t.Errorf("queryParameters are\n%s\nwanted\n%s", got, want)
	}
}

// NO ARGUMENTS, NO FIELDS. Every statement before the API was typed by
// somebody, and a `parameterMode` on it would make BigQuery read a literal
// `@` inside it as a parameter it then cannot find.
func TestNoArgumentsSendNoParameterFields(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":true,"schema":{"fields":[]},"rows":[],"totalRows":"0"}`)}
	if _, err := dial(t, f).Read(context.Background(), dialect.Request{Query: "SELECT 1", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"parameterMode", "queryParameters"} {
		if _, there := f.posts[0][k]; there {
			t.Errorf("%s was sent with no arguments", k)
		}
	}
}

// A TYPE NOBODY TAUGHT IT IS AN ERROR, not a STRING. Sending a float or a
// time.Time as its fmt.Sprint would bind something nobody chose.
func TestAnUntypedArgumentIsRefused(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":true}`)}
	_, err := dial(t, f).Read(context.Background(), dialect.Request{
		Query: "SELECT @x", Limit: 1, Args: []dialect.Arg{{Name: "x", Value: 1.5}},
	})
	if err == nil {
		t.Fatal("a float64 argument was sent")
	}
	if len(f.posts) != 0 {
		t.Error("the query was sent anyway")
	}
}

func TestPlaceholderIsNamed(t *testing.T) {
	if got := (Dialect{}).Placeholder(3, "campaign"); got != "@campaign" {
		t.Errorf("placeholder is %q", got)
	}
}

// THE DRY RUN CARRIES THE ARGUMENTS TOO. A statement naming `@campaign`
// priced without it is refused by BigQuery ("query parameter not found"),
// which would make every endpoint with a parameter unpriceable -- and an
// API that cannot price a query cannot refuse an expensive one.
func TestADryRunCarriesTheArgumentsAndAnswersKindAndBytes(t *testing.T) {
	f := &fake{t: t, reply: ok(`{"jobComplete":true,"totalBytesProcessed":"2048","statementType":"SELECT"}`)}
	p, err := dial(t, f).PlanArgs(context.Background(), "SELECT @s",
		[]dialect.Arg{{Name: "s", Value: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Bytes != 2048 || !p.Reads {
		t.Errorf("plan %+v", p)
	}
	sent := f.posts[0]
	if sent["dryRun"] != true || sent["parameterMode"] != "NAMED" || sent["queryParameters"] == nil {
		t.Errorf("the dry run sent %v", sent)
	}
}
