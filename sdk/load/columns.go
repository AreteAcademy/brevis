package load

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"cloud.google.com/go/bigquery"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
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
	// EvolveAdditiveFromPayload with nothing to complete, refused BEFORE the
	// extract: it costs no query to know. Discovered refuses it again on the
	// write path, for the caller that never comes through here.
	if err := core.CheckDiscoveryHasADeclaration(
		l.cfg.Evolve, columns, nameOf(l.bq.Dataset(l.cfg.Dataset).Table(l.cfg.Table)),
	); err != nil {
		return err
	}
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

	// A table created under a DIFFERENT prefix, refused before the extract.
	// See core.CheckLandingPrefixMatches for why it refuses rather than warns.
	inTable := make([]string, 0, len(meta.Schema))
	for _, f := range meta.Schema {
		inTable = append(inTable, f.Name)
	}
	if err := core.CheckLandingPrefixMatches(columns, inTable, nameOf(table)); err != nil {
		return err
	}

	// Columns BREVIS_NORMALIZE_DATA would abandon. See
	// core.CheckNormalizeRenames.
	// Only JSON matters to the check, and only one direction of it: a
	// column that held objects. Translating the rest would be work nothing
	// reads.
	inTypes := make(map[string]core.ColumnType, len(meta.Schema))
	for _, f := range meta.Schema {
		if f.Type == bigquery.JSONFieldType {
			inTypes[f.Name] = core.TypeJSON
		} else {
			inTypes[f.Name] = core.TypeString
		}
	}
	if err := core.CheckNormalizeRenames(columns, inTypes, nameOf(table)); err != nil {
		return err
	}

	// A column the table lacks is the one difference EvolveAdditive was asked
	// to repair, so refusing it here would make the flag unreachable in its
	// only case: a declaration that adds a column is the only way to ask for
	// one. Issue #41.
	//
	// The columns that ARE there still go through the check, rather than the
	// whole thing being skipped, so anything it learns to refuse about them
	// keeps running before the extract. That is what this method is for: one
	// metadata query against a whole source quota spent to find out.
	//
	// The same check inside Load is deliberately NOT relaxed. It runs after
	// evolveTable, against fresh metadata, which makes it the verification
	// that evolving did what it said.
	declared := columns
	if l.cfg.Evolve.MayAdd() {
		has := make(map[string]bool, len(meta.Schema))
		for _, f := range meta.Schema {
			has[f.Name] = true
		}
		declared = make([]string, 0, len(columns))
		for _, c := range columns {
			if has[c] {
				declared = append(declared, c)
			}
		}
	}

	if err := checkDeclaredAgainstTable(declared, meta.Schema, nameOf(table)); err != nil {
		return fmt.Errorf("%w. Caught before the extract, so no source quota was spent", err)
	}
	return nil
}
