package load

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"cloud.google.com/go/bigquery"
)

// checkDeclaredAgainstTable confirms the declaration describes the table that
// is actually there.
//
// Asymmetric, the same way reconcile is and for the same reason: a declared
// column the table lacks is a load that cannot work, while a table column the
// declaration omits stays NULL, which a landing table legitimately does.
func checkDeclaredAgainstTable(declared []string, schema bigquery.Schema, table string) error {
	if len(declared) == 0 {
		return nil
	}

	has := make(map[string]bool, len(schema))
	for _, f := range schema {
		has[f.Name] = true
	}

	var absent []string
	for _, c := range declared {
		if !has[c] {
			absent = append(absent, c)
		}
	}
	if len(absent) == 0 {
		return nil
	}

	sort.Strings(absent)
	return fmt.Errorf("the Columns declaration lists %s, which %s does not have. The table has: %s",
		strings.Join(absent, ", "), table, namesOf(schema))
}

// CheckDestination checks the declaration against the real table, without
// loading anything.
//
// The same check already runs in Load. What changes is the TIMING: called
// before the extraction it costs one metadata query; called in Load it costs
// the vendor's whole quota window -- which is invariant I3 of
// plan/2026-09-03-sdk-schema-declarado.md.
//
// A table that does not exist yet is not an error: creating a table is Load's
// decision, and refusing here would take CreateTable out of the path.
func (l *Loader) CheckDestination(ctx context.Context, columns []string) error {
	if len(columns) == 0 {
		return nil
	}

	table := l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)
	meta, err := table.Metadata(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("checking %s before the extract: %w", nameOf(table), err)
	}

	if err := checkDeclaredAgainstTable(columns, meta.Schema, nameOf(table)); err != nil {
		return fmt.Errorf("%w. Caught before the extract, so no source quota was spent", err)
	}
	return nil
}
