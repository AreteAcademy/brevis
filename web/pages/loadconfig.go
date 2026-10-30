package pages

import (
	"context"
	"github.com/AreteAcademy/brevis/web/i18n"
	"slices"
	"strconv"
	"strings"

	"github.com/AreteAcademy/brevis/web/components"
)

// LoadTabs is a step's configuration as a load's page shows it, in tabs
// (screen-load-v2 LV3): BuildConfig's rows, regrouped and never dropped --
// RowCount says so.
type LoadTabs struct {
	// SourceHead are the source's own settings: its driver, and for a
	// from.Many its workers and policies. For a single source, all its rows.
	SourceHead []ConfigRow

	// SourceCount is how many sources a from.Many lists; 0 for one source.
	SourceCount int
	// Common are the settings every source has with the same value, default
	// flag and note, said once, labelled by their key ("timeout").
	Common []ConfigRow
	// Varying are the other keys, in the order they first appear, and
	// Sources one line per source with a cell per key (nil where a source
	// does not have it).
	Varying []string
	Sources []SourceLine

	// Transform are the stages in order, each its rows.
	Transform []TransformOp

	// Destination are the target's settings, without what it declares.
	Destination []ConfigRow

	// Schema is the declared columns with their role; SchemaRows the
	// `declared.*` rows they come from, which keep their notes.
	Schema     []SchemaColumn
	SchemaRows []ConfigRow

	// Other are sections none of the tabs is about, shown on Summary.
	Other []ConfigSection
}

// SourceLine is one source of a from.Many: its position and its varying
// settings, aligned with LoadTabs.Varying.
type SourceLine struct {
	Index int
	Cells []*ConfigRow
}

// TransformOp is one stage of the transform: "1", and its rows.
type TransformOp struct {
	Index string
	Rows  []ConfigRow
}

// SchemaColumn is one declared column, its type when declared, and its role
// when a setting names it: "partition", "cluster".
type SchemaColumn struct{ Name, Type, Role string }

// RowCount is how many of the configuration's rows the tabs hold: a common
// setting stands for one row per source.
func (t LoadTabs) RowCount() int {
	n := len(t.SourceHead) + len(t.Common)*t.SourceCount + len(t.Destination) + len(t.SchemaRows)
	for _, s := range t.Sources {
		for _, c := range s.Cells {
			if c != nil {
				n++
			}
		}
	}
	for _, op := range t.Transform {
		n += len(op.Rows)
	}
	for _, s := range t.Other {
		n += len(s.Rows)
	}
	return n
}

// LoadConfigTabs regroups a step's configuration for its load's tabs.
func LoadConfigTabs(v ConfigView) LoadTabs {
	var t LoadTabs
	for _, s := range v.Sections {
		switch s.Key {
		case "source":
			t.splitSources(s.Rows)
		case "transform":
			for _, r := range s.Rows {
				index, _, _ := strings.Cut(r.Label, ".")
				if n := len(t.Transform); n > 0 && t.Transform[n-1].Index == index {
					t.Transform[n-1].Rows = append(t.Transform[n-1].Rows, r)
				} else {
					t.Transform = append(t.Transform, TransformOp{Index: index, Rows: []ConfigRow{r}})
				}
			}
		case "target":
			for _, r := range s.Rows {
				if strings.HasPrefix(r.Label, "declared.") {
					t.SchemaRows = append(t.SchemaRows, r)
				} else {
					t.Destination = append(t.Destination, r)
				}
			}
		default:
			t.Other = append(t.Other, s)
		}
	}
	t.Schema = schemaOf(t.SchemaRows, t.Destination)
	return t
}

// splitSources says once what every source of a from.Many shares, and lays
// the rest out source by source. A key is common only when EVERY source has
// it with the same value, default flag and note: a value that differs in one
// source, or is missing from one, is a difference, and a difference is the
// thing this tab exists to show.
func (t *LoadTabs) splitSources(rows []ConfigRow) {
	type keyed struct {
		index int
		key   string
	}
	per := map[keyed]ConfigRow{}
	var indexes []int
	var keys []string
	for _, r := range rows {
		rest, ok := strings.CutPrefix(r.Label, "sources.")
		if !ok {
			t.SourceHead = append(t.SourceHead, r)
			continue
		}
		num, key, ok := strings.Cut(rest, ".")
		i, err := strconv.Atoi(num)
		if !ok || err != nil {
			t.SourceHead = append(t.SourceHead, r)
			continue
		}
		if !slices.Contains(indexes, i) {
			indexes = append(indexes, i)
		}
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
		per[keyed{i, key}] = r
	}
	if len(indexes) == 0 {
		return
	}
	slices.Sort(indexes)
	t.SourceCount = len(indexes)
	for _, key := range keys {
		first, common := per[keyed{indexes[0], key}], true
		for _, i := range indexes {
			r, ok := per[keyed{i, key}]
			if !ok || r.Value != first.Value || r.Default != first.Default || r.Note != first.Note {
				common = false
				break
			}
		}
		if common {
			first.Label = key
			t.Common = append(t.Common, first)
		} else {
			t.Varying = append(t.Varying, key)
		}
	}
	for _, i := range indexes {
		line := SourceLine{Index: i, Cells: make([]*ConfigRow, len(t.Varying))}
		for j, key := range t.Varying {
			if r, ok := per[keyed{i, key}]; ok {
				line.Cells[j] = &r
			}
		}
		t.Sources = append(t.Sources, line)
	}
}

// schemaOf reads the declared columns -- `name TYPE` from declared.schema, or
// the names in declared.columns -- and gives each the role a setting names it
// in: the declared partition, or a destination's partition_by / cluster_by.
func schemaOf(declared, destination []ConfigRow) []SchemaColumn {
	var cols []SchemaColumn
	roles := map[string][]string{}
	mark := func(r ConfigRow, role string) {
		names := r.List
		if names == nil && r.Value != "" {
			names = strings.Split(r.Value, ", ")
		}
		for _, n := range names {
			if !slices.Contains(roles[n], role) {
				roles[n] = append(roles[n], role)
			}
		}
	}
	for _, r := range declared {
		switch r.Label {
		case "declared.schema":
			for _, c := range r.List {
				name, typ, _ := strings.Cut(c, " ")
				cols = append(cols, SchemaColumn{Name: name, Type: typ})
			}
		case "declared.columns":
			if cols == nil {
				for _, c := range r.List {
					cols = append(cols, SchemaColumn{Name: c})
				}
			}
		case "declared.partition_by":
			mark(r, "partition")
		}
	}
	for _, r := range destination {
		switch {
		case strings.HasSuffix(r.Label, "partition_by"):
			mark(r, "partition")
		case strings.HasSuffix(r.Label, "cluster_by"):
			mark(r, "cluster")
		}
	}
	for i := range cols {
		cols[i].Role = strings.Join(roles[cols[i].Name], ", ")
	}
	return cols
}

// StepConfig is the step's own configuration on its load's page.
func (v LoadView) StepConfig() (WriterConfigView, bool) {
	for _, c := range v.Configs {
		if c.Gateway == "" && c.Workflow == v.Workflow && c.Node == v.Step {
			return c, true
		}
	}
	return WriterConfigView{}, false
}

// loadTabs are the tabs a configuration has: Summary and Destination always,
// the others only with something in them, each with its count.
func loadTabs(ctx context.Context, t LoadTabs) []components.Tab {
	tabs := []components.Tab{{Key: "summary", Label: i18n.T(ctx, "config.tab.summary"), Selected: true}}
	if len(t.SourceHead) > 0 || t.SourceCount > 0 {
		label := i18n.T(ctx, "config.tab.sources")
		if t.SourceCount > 0 {
			label += " " + strconv.Itoa(t.SourceCount)
		}
		tabs = append(tabs, components.Tab{Key: "sources", Label: label})
	}
	if len(t.Transform) > 0 {
		tabs = append(tabs, components.Tab{Key: "transform", Label: i18n.T(ctx, "config.tab.transform") + " " + strconv.Itoa(len(t.Transform))})
	}
	tabs = append(tabs, components.Tab{Key: "destination", Label: i18n.T(ctx, "config.tab.destination")})
	if len(t.SchemaRows) > 0 {
		label := i18n.T(ctx, "config.tab.schema")
		if len(t.Schema) > 0 {
			label += " " + strconv.Itoa(len(t.Schema))
		}
		tabs = append(tabs, components.Tab{Key: "schema", Label: label})
	}
	return tabs
}

func tabKeys(tabs []components.Tab) []string {
	keys := make([]string, len(tabs))
	for i, t := range tabs {
		keys[i] = t.Key
	}
	return keys
}

func hasTab(tabs []components.Tab, key string) bool {
	return slices.ContainsFunc(tabs, func(t components.Tab) bool { return t.Key == key })
}

// rowValue is the value of the row with this label, "" when there is none.
func rowValue(rows []ConfigRow, label string) string {
	for _, r := range rows {
		if r.Label == label {
			return r.Value
		}
	}
	return ""
}

func transformNames(t LoadTabs) string {
	var names []string
	for _, op := range t.Transform {
		for _, r := range op.Rows {
			if len(r.List) > 0 {
				names = append(names, r.List...)
			} else {
				names = append(names, strings.TrimPrefix(r.Label, op.Index+"."))
			}
		}
	}
	return strings.Join(names, " · ")
}

// cellTitle is a varying cell's whole value and its note.
func cellTitle(r ConfigRow) string {
	if r.Note == "" {
		return r.Value
	}
	return r.Value + "\n" + r.Note
}

// schemaExtra are the declared settings the schema table does not already
// say: everything but the column lists, and those too when they carry a note.
func schemaExtra(rows []ConfigRow) []ConfigRow {
	var out []ConfigRow
	for _, r := range rows {
		if (r.Label == "declared.schema" || r.Label == "declared.columns") && r.Note == "" && !r.Default {
			continue
		}
		out = append(out, r)
	}
	return out
}

// destinationIdentity is what the destination is, where and which object,
// in the Destination tab.
func destinationIdentity(ctx context.Context, v TargetView) []components.Def {
	kind := v.Row.Kind
	if v.Row.Legacy {
		kind = i18n.T(ctx, "config.identity.legacy")
	}
	if kind == "" {
		kind = "—"
	}
	where, what := SplitTarget(v.Row.Target)
	if where == "" {
		where = "—"
	}
	return []components.Def{
		{Term: i18n.T(ctx, "config.identity.kind"), Value: kind, Mono: true},
		{Term: i18n.T(ctx, "config.identity.where"), Value: where, Mono: true},
		{Term: i18n.T(ctx, "config.identity.object"), Value: what, Mono: true},
	}
}

// schemaRole is a column's roles in the reader's language: "partition,
// cluster".
func schemaRole(ctx context.Context, role string) string {
	if role == "" {
		return ""
	}
	parts := strings.Split(role, ", ")
	for i, r := range parts {
		switch r {
		case "partition":
			parts[i] = i18n.T(ctx, "config.role.partition")
		case "cluster":
			parts[i] = i18n.T(ctx, "config.role.cluster")
		}
	}
	return strings.Join(parts, ", ")
}
