package workflow

import (
	"fmt"
	"sort"
	"strings"
)

// CheckGroups refuses a concurrency group whose members declare different
// limits.
//
// The limit is a snapshot on each run and the claim reads it item by item, so
// a group whose members said 1 and 3 would let whichever run the claim's
// window function numbered first decide how many go -- an accident, not a
// rule. Refused where the files are read, somebody looking at two YAMLs can
// predict what runs without knowing which one ran first (#32).
//
// It is a rule about the SET, so it takes the whole of it: `validate` and
// `publish` pass the files they read, and `publish` adds the members of the
// same groups that are already published and stay so.
func CheckGroups(all []Workflow) error {
	members := map[string][]Workflow{}
	for _, w := range all {
		if w.Group != "" {
			members[w.Group] = append(members[w.Group], w)
		}
	}

	groups := make([]string, 0, len(members))
	for g := range members {
		groups = append(groups, g)
	}
	sort.Strings(groups)

	for _, g := range groups {
		ws := members[g]
		agree := true
		for _, w := range ws[1:] {
			if w.MaxActive != ws[0].MaxActive {
				agree = false
				break
			}
		}
		if agree {
			continue
		}
		sort.Slice(ws, func(i, j int) bool { return ws[i].Slug < ws[j].Slug })
		named := make([]string, len(ws))
		for i, w := range ws {
			named[i] = fmt.Sprintf("%s (%d)", w.Slug, w.MaxActive)
		}
		return fmt.Errorf("concurrency group %q is declared with different limits: %s; "+
			"every workflow in a group has to declare the same limit", g, strings.Join(named, ", "))
	}
	return nil
}
