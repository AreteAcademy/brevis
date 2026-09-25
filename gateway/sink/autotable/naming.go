package autotable

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultPattern is what a table name must match when the config names no
// other, and it is deliberately narrow.
//
// Lowercase, starting with a letter, three to forty-nine characters. It is the
// intersection of what Postgres, MySQL, BigQuery and Redshift all accept
// unquoted, which means a name that passes here needs no quoting anywhere and
// cannot carry an injection: there is no path from these characters to SQL the
// gateway did not write.
const DefaultPattern = `^[a-z][a-z0-9_]{2,48}$`

// DefaultMaxNewPerHour bounds creations when the config names no number.
//
// Twenty is enough for a team onboarding a service and far under any provider's
// quota, which matters because that quota is shared with everything else in the
// project: a runaway producer here would take the rest of the platform with it.
//
// PER REPLICA, and that has to be said out loud. The budget lives in this
// process, so a deployment of four replicas admits four times this number.
// Sharing it needs a shared metastore, which is not built -- `metastore.type`
// refuses `redis` by name rather than letting somebody believe otherwise. Set
// this to the deployment's budget divided by the replica count, or treat it as
// the circuit breaker it is rather than a quota.
const DefaultMaxNewPerHour = 20

// names decides whether a producer may write to a name, and whether a new
// table may be created for it.
type names struct {
	pattern *regexp.Regexp
	allow   []string
	max     int

	mu      sync.Mutex
	created []time.Time
}

func newNames(pattern string, allow []string, max int) (*names, error) {
	if strings.TrimSpace(pattern) == "" {
		pattern = DefaultPattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("`naming.pattern` is not a regular expression: %w", err)
	}
	if max == 0 {
		max = DefaultMaxNewPerHour
	}
	return &names{pattern: re, allow: allow, max: max}, nil
}

// refund gives back one creation from the budget, for the startup probe: a
// table that was never created must not spend a producer's hourly allowance.
func (n *names) refund() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.created) > 0 {
		n.created = n.created[:len(n.created)-1]
	}
}

// check refuses a name a producer may not use. It says which rule refused it,
// because "invalid table name" sends somebody to read the gateway's source.
func (n *names) check(table string) error {
	if table == "" {
		return fmt.Errorf("no table name: the event has no %q, and that field is "+
			"what says where it goes", "")
	}
	if !n.pattern.MatchString(table) {
		return fmt.Errorf("the table name %q does not match %s", table, n.pattern)
	}
	if len(n.allow) == 0 {
		return nil
	}
	for _, p := range n.allow {
		if strings.HasPrefix(table, p) {
			return nil
		}
	}
	return fmt.Errorf("the table name %q starts with none of the allowed prefixes (%s)",
		table, strings.Join(n.allow, ", "))
}

// admit records one creation and refuses past the hour's budget.
//
// It bounds CREATIONS and never writes: a table that already exists is never
// rate limited, so a producer that has settled down is never slowed by a
// producer that has not.
func (n *names) admit(table string, now time.Time) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	cutoff := now.Add(-time.Hour)
	kept := n.created[:0]
	for _, t := range n.created {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	n.created = kept

	if len(n.created) >= n.max {
		return fmt.Errorf("this stream has already created %d tables in the last "+
			"hour, which is its `naming.max_new_per_hour`. %q was not created. "+
			"A limit reached here is a producer to look at, not a number to raise",
			n.max, table)
	}
	n.created = append(n.created, now)
	return nil
}
