package core

import (
	"fmt"
	"sort"
	"strings"
)

// CheckNormalizeRenames refuses a table whose columns BREVIS_NORMALIZE_DATA
// would abandon.
//
// Turning flattening on does not only ADD columns. Nothing ever drops one, so
// a column the new naming no longer writes stays where it is, full of the
// rows that landed before — and every row after that has a NULL in it. The
// query somebody wrote against it keeps running and stops seeing new rows.
//
// REFUSED AND NOT WARNED, for the reason the prefix change is: the wrong
// outcome has no symptom. The table looks right and the load succeeds.
//
// TWO KINDS, and the plan only had the first:
//
//	userName → username            a rename by case, whether or not anything
//	                               was nested
//	name     → name_first, ...     a column that held objects, whose fields
//	                               now have columns of their own
//
// The second is the one a consumer actually turns this on for, so a guard
// without it would miss the case it exists to catch.
func CheckNormalizeRenames(declared []string, inTable map[string]ColumnType, table string) error {
	return checkNormalizeRenames(NormalizeData(), declared, inTable, table)
}

// checkNormalizeRenames takes the flag rather than reading it, so this
// package's own tests can exercise both sides without a child process.
func checkNormalizeRenames(on bool, declared []string, inTable map[string]ColumnType, table string) error {
	// Only the flag is a guard. An empty table or an empty declaration falls
	// out of the loop below on its own, and a condition no mutation can kill
	// is a condition that reads like a rule and is not one -- a mutation
	// removing those two passed every test here.
	if !on {
		return nil
	}

	wanted := make(map[string]bool, len(declared))
	for _, c := range declared {
		wanted[c] = true
	}

	// Sorted, so a table with two problems names the same one every run.
	names := make([]string, 0, len(inTable))
	for c := range inTable {
		names = append(names, c)
	}
	sort.Strings(names)

	for _, c := range names {
		if lower := strings.ToLower(c); lower != c && wanted[lower] {
			return fmt.Errorf("%s has the column %q and this load declares "+
				"%q. %s lower-cases every column name, and nothing drops one: "+
				"%q would stay with the rows already in it while everything "+
				"after lands in %q. Point this at a new table, or rename %q "+
				"yourself first",
				table, c, lower, EnvNormalizeData, c, lower, c)
		}

		// A column that held objects, and whose fields now have columns of
		// their own. `name` is only abandoned if the batch declares none of
		// it -- a field that is an object in one record and a scalar in
		// another declares BOTH, and that is the example this feature was
		// asked for.
		if inTable[c] != TypeJSON || wanted[c] {
			continue
		}
		for _, d := range declared {
			if !strings.HasPrefix(d, c+"_") {
				continue
			}
			return fmt.Errorf("%s has the JSON column %q and this load declares "+
				"%q instead. %s gives a nested object's fields columns of their "+
				"own, and nothing drops one: %q would keep the objects already "+
				"landed and nothing would write it again. Point this at a new "+
				"table, or rename %q yourself first",
				table, c, d, EnvNormalizeData, c, c)
		}
	}
	return nil
}
