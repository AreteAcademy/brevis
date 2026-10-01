package sdk

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The layout's own columns follow BREVIS_LANDING_PREFIX.
//
// IN A CHILD PROCESS, and it has to be: these are package variables resolved
// when the package loads, so a test that sets the variable afterwards changes
// nothing. Re-running the test binary with the variable set is the only way
// to assert what a real deployment gets, and it is what makes the mutation
// "LandingPartitionBy left on the default" die — with the variable unset,
// the default and the derived value are the same string.
func TestTheLayoutFollowsTheConfiguredPrefix(t *testing.T) {
	const marker = "BREVIS_TEST_PREFIX_CHILD"

	if os.Getenv(marker) == "1" {
		// Resolved with BREVIS_LANDING_PREFIX=_ACME_, which also exercises
		// the normalisation end to end rather than in a table.
		if LandingPrefix != "acme_" {
			t.Fatalf("LandingPrefix = %q, want acme_", LandingPrefix)
		}
		for _, c := range []struct{ got, want string }{
			{LandingColumnID, "acme_ingestion_id"},
			{LandingColumnRecordKey, "acme_record_key"},
			{LandingColumnOperation, "acme_operation"},
			{LandingColumnReceivedAt, "acme_received_at"},
			{LandingColumnLoadedAt, "acme_loaded_at"},
			{LandingColumnStream, "acme_stream"},
			{LandingColumnGateway, "acme_gateway"},
			{LandingColumnReceivedBytes, "acme_received_bytes"},
		} {
			if c.got != c.want {
				t.Errorf("%q, want %q", c.got, c.want)
			}
		}

		// The two derived from the names, not written again.
		if LandingPartitionBy != "acme_received_at" {
			t.Errorf("LandingPartitionBy = %q: a landing table would be "+
				"partitioned on a column it does not have", LandingPartitionBy)
		}
		if got := LandingClusterBy(); len(got) != 1 || got[0] != "acme_record_key" {
			t.Errorf("LandingClusterBy() = %v", got)
		}

		// The PRODUCER's column carries no prefix, whatever the prefix is.
		if LandingColumnData != "data" {
			t.Errorf("LandingColumnData = %q: it is the one column here that "+
				"is the producer's, and it is not the layout's to rename",
				LandingColumnData)
		}

		// And the whole schema followed, not only the constants.
		for _, col := range LandingControlColumns(LandingOptions{}) {
			if !strings.HasPrefix(col.Name, "acme_") {
				t.Errorf("LandingControlColumns declares %q", col.Name)
			}
		}

		// THE ID DOES NOT MOVE. It identifies the RECORD, not the table's
		// column naming. If it followed the prefix, the same record under two
		// prefixes would get two ids and a merge would duplicate it.
		id, err := LandingID("landing.orders", "A-1", map[string]any{
			"id": "A-1", "total": 15.5,
			"tags": []any{"x", "y"}, "nested": map[string]any{"b": 2, "a": 1},
		})
		if err != nil {
			t.Fatal(err)
		}
		if id != "cf239eb8-b105-5bdd-98a2-7d25cf776170" {
			t.Errorf("LandingID = %s under a configured prefix: the id "+
				"followed the column naming, so every row already written "+
				"has a different one and the next merge duplicates the "+
				"table", id)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestTheLayoutFollowsTheConfiguredPrefix", "-test.v")
	cmd.Env = append(os.Environ(), marker+"=1", "BREVIS_LANDING_PREFIX=_ACME_")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("with BREVIS_LANDING_PREFIX=_ACME_:\n%s", out)
	}
}

// Set and unusable does not start — for real, at init, in a process.
//
// The S1 test exercises the resolver directly; this one proves the variable
// is actually resolved when the package loads, which is the difference
// between a function that would panic and a deployment that does not come up.
func TestAnUnusablePrefixStopsTheProcess(t *testing.T) {
	const marker = "BREVIS_TEST_PREFIX_BAD"

	if os.Getenv(marker) == "1" {
		t.Fatal("this process should not have started")
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestAnUnusablePrefixStopsTheProcess")
	cmd.Env = append(os.Environ(), marker+"=1", "BREVIS_LANDING_PREFIX=9x")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the process started with an unusable prefix:\n%s", out)
	}
	if !strings.Contains(string(out), "BREVIS_LANDING_PREFIX") {
		t.Errorf("the failure does not name the variable:\n%s", out)
	}
	if !strings.Contains(string(out), "9x") {
		t.Errorf("the failure does not show the value:\n%s", out)
	}
}
