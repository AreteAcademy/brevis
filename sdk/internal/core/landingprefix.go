package core

import (
	"fmt"
	"os"
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
