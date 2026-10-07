package core

import (
	"strings"
	"testing"
)

func landingCols(prefix string) []string {
	out := make([]string, 0, len(LandingSuffixes))
	for _, s := range LandingSuffixes {
		out = append(out, prefix+s)
	}
	return out
}

// A table whose control columns were created under another prefix is refused.
//
// Nothing ever DROPS a column -- Plan emits `add` and `widen` and nothing
// else, deliberately -- so switching the prefix on a live table does not
// rename eight columns. It adds eight and abandons eight, and from then on
// the query a landing table exists for partitions on a column nothing
// writes:
//
//	qualify row_number() over (partition by brevis_record_key
//	                           order by brevis_received_at desc) = 1
//
// Rows arrive, the dashboard goes flat, nothing logs anything. That failure
// has no symptom, which is why this refuses instead of warning.
func TestADifferentPrefixOnALiveTableIsRefused(t *testing.T) {
	// Configured is whatever this process resolved -- `brevis_`, since the
	// variable is unset here. The table was created under another one.
	declared := append(landingCols(LandingPrefix()), "source_key")
	inTable := append(landingCols("acme_"), "source_key")

	err := CheckLandingPrefixMatches(declared, inTable, "bronze.orders")
	if err == nil {
		t.Fatal("accepted: the table would grow eight columns and abandon eight")
	}
	for _, want := range []string{"acme_", LandingPrefix(), "bronze.orders"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	// Somebody doing this ON PURPOSE must not be stuck.
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("the refusal names no way out: %v", err)
	}
}

// What it must NOT refuse.
func TestTheLandingPrefixCheckIsNarrow(t *testing.T) {
	declared := landingCols(LandingPrefix())

	for _, c := range []struct {
		name    string
		inTable []string
	}{
		// A first run. The table is not there, or has nothing of ours.
		{"an empty table", nil},
		{"somebody else's table", []string{"id", "total", "created_at"}},

		// The SAME prefix, which is every run after the first.
		{"the same prefix", landingCols(LandingPrefix())},

		// A PARTIAL set under another prefix is not a landing table. A
		// warehouse full of `brevis_stream` columns somebody named by hand
		// must not start failing.
		{"one column that looks like ours", []string{"acme_stream", "id"}},
		{"seven of the eight", landingCols("acme_")[:7]},

		// The eight with NO prefix at all. An empty prefix cannot be
		// configured -- it collides with sdk.IngestionID()'s column -- so a
		// table shaped like this was not created by us, and refusing it
		// would be a false positive on somebody else's schema.
		{"the eight bare suffixes", landingCols("")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := CheckLandingPrefixMatches(declared, c.inTable, "t"); err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}

	// And it only fires when the DECLARATION is this layout. A consumer with
	// their own schema, loading into a table that happens to be a landing
	// table, is not making this mistake.
	if err := CheckLandingPrefixMatches(
		[]string{"id", "total"}, landingCols("acme_"), "t"); err != nil {
		t.Errorf("a declaration that is not the landing layout was refused: %v", err)
	}
}

// The suffixes are the layout's, and `sdk` builds its column names from the
// same eight. A list that drifts makes the check above miss a real case.
func TestTheLandingSuffixesAreTheEight(t *testing.T) {
	want := []string{
		"ingestion_id", "record_key", "operation", "received_at",
		"loaded_at", "stream", "gateway", "received_bytes",
	}
	if len(LandingSuffixes) != len(want) {
		t.Fatalf("%d suffixes, want %d: %v", len(LandingSuffixes), len(want), LandingSuffixes)
	}
	for i, s := range want {
		if LandingSuffixes[i] != s {
			t.Errorf("suffix %d is %q, want %q", i, LandingSuffixes[i], s)
		}
	}
}
