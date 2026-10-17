package pages

import (
	"fmt"
	"slices"
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func mf(path string, v any) catalog.ManifestField { return catalog.ManifestField{Path: path, Value: v} }

// manySources is a from.Many of n HTTP sources: the method, the timeout and
// the format the same everywhere, the URL different in each -- and, in the
// last one, a timeout that differs.
func manySources(n int, lastTimeout string) *postgres.WriterConfig {
	fields := []catalog.ManifestField{mf("source.driver", "from.Many"), mf("source.workers", 2.0)}
	for i := 1; i <= n; i++ {
		p := fmt.Sprintf("source.sources.%d.", i)
		timeout := "1m"
		if i == n && lastTimeout != "" {
			timeout = lastTimeout
		}
		fields = append(fields, mf(p+"driver", "from.HTTP"), mf(p+"method", "GET"),
			mf(p+"url", fmt.Sprintf("https://api/series/%d", i)), mf(p+"timeout", timeout))
	}
	fields = append(fields,
		mf("transform.1.map", []any{"rename(data → date)", "compute(source_key)"}),
		mf("transform.2.map", []any{"skip_without(source_key)"}),
		mf("target.driver", "bigquery.Table"), mf("target.dedup", "merge"),
		mf("target.declared.schema", []any{"date DATE", "value NUMERIC", "series_code STRING"}),
		mf("target.declared.partition_by", "date"),
		mf("target.cluster_by", []any{"series_code"}),
	)
	return &postgres.WriterConfig{Fields: fields, Notes: map[string]string{"target.dedup": "by ingestion_id"}}
}

func keysOf(rows []ConfigRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Label)
	}
	return out
}

// WHAT EVERY SOURCE SHARES IS SAID ONCE (screen-load-v2 LV3): ten sources
// with the same driver, method and timeout are "common to 10" plus a table of
// what differs -- the URL. A key is common only when every source has it with
// the same value, the same default flag and the same note.
func TestASourceSettingSharedByAllIsSaidOnce(t *testing.T) {
	tabs := LoadConfigTabs(BuildConfig(manySources(10, "")))
	if tabs.SourceCount != 10 {
		t.Fatalf("sources = %d", tabs.SourceCount)
	}
	if got := keysOf(tabs.Common); !slices.Equal(got, []string{"driver", "method", "timeout"}) {
		t.Errorf("common = %v", got)
	}
	if !slices.Equal(tabs.Varying, []string{"url"}) || len(tabs.Sources) != 10 {
		t.Errorf("varying = %v over %d sources", tabs.Varying, len(tabs.Sources))
	}
	if c := tabs.Sources[3].Cells[0]; c == nil || c.Value != "https://api/series/4" {
		t.Errorf("source 4's url = %v", c)
	}
	if got := keysOf(tabs.SourceHead); !slices.Equal(got, []string{"driver", "workers"}) {
		t.Errorf("the source's own settings = %v", got)
	}

	one := LoadConfigTabs(BuildConfig(manySources(10, "5m")))
	if slices.Contains(keysOf(one.Common), "timeout") || !slices.Contains(one.Varying, "timeout") {
		t.Error("a timeout that differs in one source was called common")
	}

	cfg := manySources(3, "")
	cfg.Fields = slices.DeleteFunc(cfg.Fields, func(m catalog.ManifestField) bool { return m.Path == "source.sources.2.method" })
	missing := LoadConfigTabs(BuildConfig(cfg))
	if slices.Contains(keysOf(missing.Common), "method") || !slices.Contains(missing.Varying, "method") {
		t.Error("a key one source lacks was called common")
	}
	if missing.Sources[1].Cells[slices.Index(missing.Varying, "method")] != nil {
		t.Error("the source without the key shows a value for it")
	}
}

// EVERY ROW LANDS IN EXACTLY ONE TAB: the config's rows in, the tabs' rows
// out, the same count -- the split may group, it may not drop.
func TestEveryConfigRowIsInOneTab(t *testing.T) {
	v := BuildConfig(manySources(10, "5m"))
	in := 0
	for _, s := range v.Sections {
		in += len(s.Rows)
	}
	tabs := LoadConfigTabs(v)
	if out := tabs.RowCount(); out != in {
		t.Errorf("rows in = %d, rows out = %d", in, out)
	}
	if len(tabs.Transform) != 2 || len(tabs.Transform[0].Rows) != 1 {
		t.Errorf("transform = %+v", tabs.Transform)
	}
	if got := keysOf(tabs.Destination); !slices.Equal(got, []string{"driver", "dedup", "cluster_by"}) {
		t.Errorf("destination = %v", got)
	}
	if tabs.Destination[1].Note != "by ingestion_id" {
		t.Error("a destination row lost its note")
	}
	want := []SchemaColumn{{"date", "DATE", "partition"}, {"value", "NUMERIC", ""}, {"series_code", "STRING", "cluster"}}
	if !slices.Equal(tabs.Schema, want) {
		t.Errorf("schema = %+v", tabs.Schema)
	}
}

// A SINGLE SOURCE IS ITS ROWS: no "common to 1", no table of one.
func TestASingleSourceIsItsRows(t *testing.T) {
	tabs := LoadConfigTabs(BuildConfig(&postgres.WriterConfig{Fields: []catalog.ManifestField{
		mf("source.driver", "from.Files"), mf("source.path", "/data/x.csv"), mf("target.driver", "to.Files"),
	}}))
	if tabs.SourceCount != 0 || len(tabs.Common) != 0 || len(tabs.Sources) != 0 {
		t.Errorf("a single source was split: %+v", tabs)
	}
	if got := keysOf(tabs.SourceHead); !slices.Equal(got, []string{"driver", "path"}) {
		t.Errorf("source rows = %v", got)
	}
	if len(tabs.Schema) != 0 || len(tabs.Transform) != 0 {
		t.Error("tabs with nothing in them were filled")
	}
}
