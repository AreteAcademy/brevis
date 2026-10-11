package gateway_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/gateway"
)

func describeExample(t *testing.T, name string) *gateway.Manifest {
	t.Helper()
	t.Setenv("BREVIS_ORDERS_DSN", "postgres://loader:s3cret@db.internal:5432/shop")
	cfg, err := gateway.Load(filepath.Join("example", name))
	if err != nil {
		t.Fatal(err)
	}
	m, err := gateway.Describe(cfg, locators(), "", time.Date(2026, 10, 7, 18, 2, 11, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func streamNamed(t *testing.T, m *gateway.Manifest, name string) gateway.ManifestStream {
	t.Helper()
	for _, st := range m.Streams {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("no stream %q in the manifest", name)
	return gateway.ManifestStream{}
}

func fieldAt(t *testing.T, st gateway.ManifestStream, path string) gateway.ManifestField {
	t.Helper()
	for _, f := range st.Fields {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("stream %s has no field %q", st.Name, path)
	return gateway.ManifestField{}
}

func TestDescribeIsVersionTwo(t *testing.T) {
	if m := describeExample(t, "gateway.yaml"); m.Version != 2 {
		t.Fatalf("version = %d, want 2", m.Version)
	}
}

// The stream as it RUNS, not as it was typed: check() fills the defaults in,
// and a reader asking "how many workers deliver this" wants the 4 nobody wrote.
func TestDescribeCarriesTheResolvedStream(t *testing.T) {
	st := streamNamed(t, describeExample(t, "gateway.yaml"), "tables")

	if f := fieldAt(t, st, "sink.into.write"); f.Value != "merge" || f.Default {
		t.Fatalf("sink.into.write = %+v, want merge as written", f)
	}
	if f := fieldAt(t, st, "buffer.flush.every"); f.Value != "1m" || f.Default {
		t.Fatalf("buffer.flush.every = %+v, want 1m as written", f)
	}
	if f := fieldAt(t, st, "buffer.flush.size"); f.Value != "256MiB" {
		t.Fatalf("buffer.flush.size = %+v, want 256MiB", f)
	}
	if f := fieldAt(t, st, "sink.naming.allow"); !reflect.DeepEqual(f.Value, []any{"app_", "svc_"}) {
		t.Fatalf("sink.naming.allow = %#v", f.Value)
	}
	if f := fieldAt(t, st, "buffer.workers"); f.Value != 4 || !f.Default {
		t.Fatalf("buffer.workers = %+v, want the default 4, marked as one", f)
	}
}

// A setting that is off and was never written says nothing worth a line. One
// that is off because somebody WROTE it off is a decision, and stays.
func TestDescribeLeavesOutZeroesNobodyWrote(t *testing.T) {
	st := streamNamed(t, describeExample(t, "gateway.yaml"), "orders")
	for _, f := range st.Fields {
		if f.Path == "sink.topic" || f.Path == "stamp_loaded_at" || f.Path == "sink.table_from" {
			t.Fatalf("%s is zero and absent from the file, and was described: %+v", f.Path, f)
		}
	}
}

// The comments are where the file says WHY. A parser drops them; describe
// keeps them beside the setting they sit on.
func TestDescribeKeepsTheCommentsBesideTheirSetting(t *testing.T) {
	st := streamNamed(t, describeExample(t, "gateway.yaml"), "tables")

	if n := st.Notes["sink.metastore.ttl"]; !strings.Contains(n, "How long the cache may be WRONG") {
		t.Fatalf("note for sink.metastore.ttl = %q", n)
	}
	// A section's comment belongs to the section.
	if n := st.Notes["sink.shape"]; !strings.Contains(n, "What the RECORD contributes to the row") {
		t.Fatalf("note for sink.shape = %q", n)
	}
	// The comment above `- name:` is about the whole stream -- here, the
	// envelope a producer must send -- and it is kept under the empty path.
	if n := st.Notes[""]; !strings.Contains(n, "One route, N tables, nothing declared") {
		t.Fatalf("the stream's own note = %q", n)
	}
	// A comment at the end of the line is a note too.
	clicks := streamNamed(t, describeExample(t, "gateway.yaml"), "clicks")
	if n := clicks.Notes["format"]; !strings.Contains(n, "declared, never sniffed") {
		t.Fatalf("note for format = %q", n)
	}
	// Notes carry the text, not the YAML's comment markers.
	for path, n := range st.Notes {
		if strings.HasPrefix(n, "#") {
			t.Fatalf("note for %s still starts with #: %q", path, n)
		}
	}
}

// The file names the variable; the value never travels. The golden file shows
// the same thing, and this says it in one line somebody can read.
func TestDescribeNamesTheVariableAndNeverItsValue(t *testing.T) {
	m := describeExample(t, "gateway.yaml")
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "db.internal") {
		t.Fatalf("the DSN's value reached the manifest: %s", raw)
	}
	if f := fieldAt(t, streamNamed(t, m, "orders"), "sink.dsn_from"); f.Value != "BREVIS_ORDERS_DSN" {
		t.Fatalf("sink.dsn_from = %+v", f)
	}
}

// A config built in code has no file behind it: everything set is described,
// nothing is called a default, and there are no notes to keep.
func TestDescribeOfAConfigWithNoFile(t *testing.T) {
	cfg := &gateway.Config{Name: "g", Streams: []gateway.Stream{{
		Name: "x", Path: "/v1/x",
		Sink: gateway.Sink{Type: "pubsub", Project: "p", Topic: "t"},
	}}}
	m, err := gateway.Describe(cfg, locators(), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st := m.Streams[0]
	if f := fieldAt(t, st, "sink.topic"); f.Value != "t" || f.Default {
		t.Fatalf("sink.topic = %+v", f)
	}
	if len(st.Notes) != 0 {
		t.Fatalf("notes = %v, want none", st.Notes)
	}
}

// describe serializes every field of a stream by its yaml name, so a field
// added tomorrow is described with no change here. That is also the risk:
// the day somebody adds `password:` to a sink, it goes straight to the
// console. The convention that keeps a secret out of this file is the `_from`
// suffix -- the variable's NAME -- and this holds every field to it.
func TestNoStreamFieldLooksLikeASecret(t *testing.T) {
	secret := regexp.MustCompile(`(^|_)(dsn|passwords?|passwd|pwd|secrets?|tokens?|credentials?|api_?key|access_key|private_key|auth_token|authorization|bearer)($|_)`)
	// Sink holds a *Sink (`into`), so a type is walked once and not forever.
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type, prefix string)
	walk = func(typ reflect.Type, prefix string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || typ.PkgPath() != reflect.TypeOf(gateway.Stream{}).PkgPath() || seen[typ] {
			return
		}
		seen[typ] = true
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if !f.IsExported() || name == "" || name == "-" {
				continue
			}
			if secret.MatchString(name) && !strings.HasSuffix(name, "_from") {
				t.Errorf("%s%s looks like a secret: name the variable that holds it (`%s_from`) instead", prefix, name, name)
			}
			walk(f.Type, prefix+name+".")
		}
	}
	walk(reflect.TypeOf(gateway.Stream{}), "")
}

// A comment is a note only when its author MARKS it one, with `#:`. Every
// other comment stays in the file: a secret left in an ordinary comment --
// a commented-out setting, or a password on the line after "the password
// is" -- never leaves the gateway, and no pattern has to recognise it.
func TestOnlyAMarkedCommentIsANote(t *testing.T) {
	t.Setenv("BREVIS_CLICKS_DSN", "postgres://loader:s3cret@db.internal:5432/shop")
	cfg, err := gateway.Load(filepath.Join("testdata", "describe", "commented-out.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := gateway.Describe(cfg, locators(), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st := m.Streams[0]
	raw, _ := json.Marshal(st.Notes)
	for _, leak := range []string{"hunter2", "password", "dsn:", "old:", "pass:"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("an unmarked comment reached the notes (%q): %s", leak, raw)
		}
	}
	if st.Notes[""] != "Clicks from the web app, one event per click." {
		t.Errorf("the stream's marked note = %q", st.Notes[""])
	}
	if st.Notes["sink.table"] != "The table must exist: this does not create one.\n\nIt is created by the warehouse's own migrations." {
		t.Errorf("the table's marked note = %q", st.Notes["sink.table"])
	}
	if st.Notes["sink.write"] != "append: a redelivery is a second row" {
		t.Errorf("a marked line comment = %q", st.Notes["sink.write"])
	}
}
