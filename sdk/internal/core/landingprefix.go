package core

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// EnvLandingPrefix replaces the prefix on the landing layout's own columns.
//
// A variable on the process, and not a field in a YAML, and that is the whole
// safety of it. Two sinks in one gateway CANNOT disagree, and neither can the
// schema that declares a table and the transformer that fills it -- with two
// doors to one decision, a row carrying `acme_ingestion_id` lands in a table
// declaring `brevis_ingestion_id`. One answer per deployment, said once.
//
//	BREVIS_LANDING_PREFIX=acme
//
// PICK IT WHEN THE TABLE IS CREATED. Changing it later is not a rename:
// nothing drops, ever, so the eight old columns stay and eight new ones are
// added. Every row after that has NULLs in the old set, and the query a
// landing table exists for --
//
//	qualify row_number() over (partition by brevis_record_key
//	                           order by brevis_received_at desc) = 1
//
// -- partitions on a column nothing writes any more. Rows arrive, the
// dashboard goes flat, and nothing logs anything.
const EnvLandingPrefix = "BREVIS_LANDING_PREFIX"

// DefaultLandingPrefix is what every table created so far carries.
const DefaultLandingPrefix = "brevis_"

// NormalizeLandingPrefix turns whatever was configured into a prefix.
//
// Trim `_` from both ends, lower-case, append exactly one:
//
//	unset, blank → brevis_
//	_NAME        → name_
//	_NAME_       → name_
//	NAME         → name_
//
// A PURE function, and that is what lets the whole table of cases be a test.
// Reading the environment in here is what would make it untestable, which is
// the shape this SDK has paid for before -- see CreationPlan.
func NormalizeLandingPrefix(raw string) (string, error) {
	// Blank is NOT a request for no prefix. `BREVIS_LANDING_PREFIX=` is a
	// line people write in a compose file meaning "not configured", and
	// reading it as "I want none" would hand them the collision below.
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DefaultLandingPrefix, nil
	}

	name := strings.ToLower(strings.Trim(trimmed, "_"))
	if name == "" {
		// Underscores and nothing else. They typed characters, so this is a
		// request, and the request cannot be honoured.
		return "", fmt.Errorf("%q leaves no prefix at all, and a landing table "+
			"without one would call its identity column `ingestion_id` -- which "+
			"is exactly the column sdk.IngestionID() writes for every ordinary "+
			"pipeline. Two different things with one name in one table", raw)
	}

	prefix := name + "_"
	// Checked as a WHOLE column name and not as a prefix on its own. The
	// difference is length and only length -- a leading digit is already
	// caught by the bare name, and a mutation validating just the prefix
	// passed every other case here. ColumnName allows 128 characters and
	// `<name>_ingestion_id` adds 13, so a 120-character prefix is legal
	// alone and makes a column that BigQuery would refuse after the extract.
	if !ColumnName.MatchString(prefix + "ingestion_id") {
		return "", fmt.Errorf("%q cannot begin a column name: %q has to match %s. "+
			"That is BigQuery's rule and it is the narrowest of the four, so a "+
			"prefix that passes works everywhere", raw, prefix+"ingestion_id", ColumnName)
	}
	return prefix, nil
}

// landingPrefix is resolved once, when the package loads.
var landingPrefix = resolveLandingPrefix()

// LandingPrefix is the prefix this process gives the landing layout's columns.
func LandingPrefix() string { return landingPrefix }

// resolveLandingPrefix reads the environment, and PANICS when the variable is
// set and cannot be used.
//
// A function of its own rather than an expression in the var, so this
// package's own tests can exercise both outcomes -- the same seam
// sdk/context uses for its sync.Once.
//
// The panic is narrow: a process that never sets the variable can never reach
// it. And it is deliberate rather than this repo's habit -- autoparams.go and
// runcontext.go both log "ignoring malformed..." and carry on, which is right
// for a param and a date because ignoring those degrades gracefully. Ignoring
// this one creates tables named after the prefix the operator was replacing,
// and they find out from the column names, months later.
func resolveLandingPrefix() string {
	raw, set := os.LookupEnv(EnvLandingPrefix)
	if !set {
		return DefaultLandingPrefix
	}
	prefix, err := NormalizeLandingPrefix(raw)
	if err != nil {
		panic(fmt.Sprintf("%s=%q: %v", EnvLandingPrefix, raw, err))
	}
	return prefix
}

// LandingSuffixes is what the landing layout calls its own eight columns,
// after the prefix.
//
// Here and not in `sdk` because the check below needs them, and two lists of
// the same eight is a list that drifts: the day one gains a column, a
// prefix change stops being detected and nobody notices until a dashboard
// goes flat. A test in `sdk` pins its column names against these.
var LandingSuffixes = []string{
	"ingestion_id", "record_key", "operation", "received_at",
	"loaded_at", "stream", "gateway", "received_bytes",
}

// CheckLandingPrefixMatches refuses a table whose control columns were
// created under a different prefix.
//
// Nothing ever DROPS a column: Plan emits `add` and `widen` and nothing else,
// deliberately. So switching BREVIS_LANDING_PREFIX on a live table does not
// rename eight columns -- it adds eight and abandons eight. Every row after
// that has NULLs in the old set, and the query a landing table exists for
//
//	qualify row_number() over (partition by brevis_record_key
//	                           order by brevis_received_at desc) = 1
//
// partitions on a column nothing writes any more. Rows arrive, the dashboard
// goes flat, and nothing logs anything.
//
// REFUSED AND NOT WARNED, because that failure has no symptom. A warning is
// the right shape when the wrong outcome announces itself; this one looks
// like success for as long as nobody runs the query.
//
// NARROW ON BOTH SIDES. It fires only when the DECLARATION is this layout --
// a consumer with their own schema is not making this mistake -- and only
// when the table carries the FULL EIGHT under one other prefix. A warehouse
// where somebody named a column `brevis_stream` by hand must not start
// failing.
func CheckLandingPrefixMatches(declared, inTable []string, table string) error {
	mine := LandingPrefix()

	declaring := false
	for _, c := range declared {
		if c == mine+LandingSuffixes[0] {
			declaring = true
			break
		}
	}
	if !declaring || len(inTable) == 0 {
		return nil
	}

	// NOT FOLDED. [#43] This is a heuristic, not a comparison the load
	// depends on: it fires only when a table carries the FULL EIGHT suffixes
	// under one other prefix. Folding it would widen a heuristic -- a table
	// with `BREVIS_stream` would start being refused -- and it would fix no
	// load, because the prefix and the suffixes are the SDK's own constants
	// and neither side of this is a name a consumer chose.
	//
	// Group the table's columns by the prefix they would have, if they were
	// ours. Eight hits on one prefix is a landing table; fewer is a
	// coincidence.
	seen := map[string]int{}
	for _, c := range inTable {
		for _, suffix := range LandingSuffixes {
			if p, ok := strings.CutSuffix(c, suffix); ok && p != "" && p != mine {
				seen[p]++
			}
		}
	}
	for prefix, n := range seen {
		if n < len(LandingSuffixes) {
			continue
		}
		return fmt.Errorf("%s already carries the landing layout under %q and "+
			"this load declares %q. Nothing drops a column, so the eight under "+
			"%q would stay and eight more would be added -- every row after "+
			"that with NULLs in the old set, and `partition by %srecord_key` "+
			"reading a column nothing writes any more. Point %s at a new "+
			"table, or rename the eight columns yourself and then change the "+
			"prefix",
			table, prefix, mine, prefix, prefix, EnvLandingPrefix)
	}
	return nil
}

// EnvNormalizeData flattens a nested object one level, giving its fields
// columns of their own.
//
//	BREVIS_NORMALIZE_DATA=true
//
// A variable on the process, beside EnvLandingPrefix and for the reason that
// one is: two sinks in one gateway cannot disagree, and neither can the
// schema that declares a table and the transformer that fills it.
//
// It applies to the `columns` shape only. Under `document` the record goes
// whole into one JSON column and there is nothing to flatten into.
//
// PICK IT BEFORE THE FIRST TABLE. It does not only ADD columns, it RENAMES
// them: `userName` becomes `username` whether or not anything is nested. On a
// live table nothing drops, so the old name stays, the new one is added, and
// every row after that has NULLs in one of each pair.
const EnvNormalizeData = "BREVIS_NORMALIZE_DATA"

var normalizeData = resolveNormalizeData()

// NormalizeData reports whether this process flattens a nested object one
// level.
func NormalizeData() bool { return normalizeData }

// resolveNormalizeData reads the environment, and PANICS when the variable is
// set to something that is not a bool.
//
// `yes` reading as false would flatten nothing while the operator believes it
// is flattening, and they would find out from a table that never grew the
// columns. A function of its own so this package's own tests can exercise
// both outcomes.
func resolveNormalizeData() bool {
	raw := strings.TrimSpace(os.Getenv(EnvNormalizeData))
	if raw == "" {
		return false
	}
	on, err := strconv.ParseBool(raw)
	if err != nil {
		panic(fmt.Sprintf("%s=%q: not a true/false value (try true, false, 1 or 0)",
			EnvNormalizeData, raw))
	}
	return on
}
