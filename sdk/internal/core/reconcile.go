package core

import (
	"fmt"
	"sort"
	"strings"
)

// Reconcile decides which columns a load writes, and refuses the cases where
// writing would lose or misplace data.
//
// The destination is the authority: it is what has to be satisfied, and the
// ORDER comes from it -- which matters because `COPY FROM` and `INSERT ROW`
// match values by POSITION, not by name. That is exactly what cost v0.12.0 on
// BigQuery.
//
// The rule is asymmetric on purpose:
//
//   - a field in the record the destination does not have -> an ERROR naming
//     the field. Carrying on would discard that data with no signal at all,
//     which is the worst way to fail: it vanishes and nothing says so.
//   - a column in the destination the record does not carry -> fine, it stays
//     NULL. A landing table legitimately does that.
//
// It lives here, and not in one destination's package, because the four
// destinations with a schema have the same problem. The TYPE check does not
// come up with it: on BigQuery it is done against the declared schema, and on
// the SQL destinations the server itself refuses the wrong type, at INSERT
// time.
func Reconcile(dest, incoming []string, target string) ([]string, error) {
	inTarget := make(map[string]bool, len(dest))
	for _, c := range dest {
		inTarget[c] = true
	}

	var extra []string
	brought := make(map[string]bool, len(incoming))
	for _, c := range incoming {
		brought[c] = true
		if !inTarget[c] {
			extra = append(extra, c)
		}
	}

	if len(extra) > 0 {
		sort.Strings(extra)
		return nil, fmt.Errorf("the rows carry column(s) %s, which %s does not have. "+
			"They would be silently dropped, so the load stops here: add the column to the "+
			"table, or remove the field in Transform",
			strings.Join(extra, ", "), target)
	}

	cols := make([]string, 0, len(dest))
	for _, c := range dest {
		if brought[c] {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("no column in common between the rows and the destination")
	}
	return cols, nil
}
