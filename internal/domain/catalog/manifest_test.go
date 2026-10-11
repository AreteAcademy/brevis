package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodManifest = `{
  "kind": "gateway-manifest", "version": 1, "gateway": "events_gateway",
  "generated_at": "2026-10-07T18:02:11Z",
  "streams": [
    {"path": "/v1/clicks", "destinations": [
      {"role": "sink", "kind": "pubsub", "target": "pubsub://acme-prod/clicks"},
      {"role": "dead_letter", "kind": "files", "target": null, "note": "relative path"},
      {"role": "archive", "kind": "files", "target": "gs://acme-oversize/clicks/"}
    ]},
    {"path": "/v1/tables", "destinations": [
      {"role": "sink", "kind": "auto_table", "target": "bigquery://acme-prod/landing/*", "routes": true}
    ]}
  ]
}`

func TestAGoodManifestIsRead(t *testing.T) {
	m, err := ParseManifest([]byte(goodManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Gateway != "events_gateway" || len(m.Streams) != 2 || len(m.Streams[0].Destinations) != 3 {
		t.Fatalf("manifest = %+v", m)
	}
	if d := m.Streams[0].Destinations[1]; d.Target != nil || d.Note != "relative path" {
		t.Errorf("an unidentified destination = %+v", d)
	}
}

// Refused whole, naming the stream and the role: a manifest half published
// would be a console that shows half a gateway and says nothing.
func TestABadManifestIsRefusedNamingWhere(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"wrong kind", `"kind": "gateway-manifest"`, `"kind": "workflow"`, "kind"},
		{"wrong version", `"version": 1`, `"version": 3`, "version"},
		{"no gateway name", `"gateway": "events_gateway"`, `"gateway": ""`, "gateway"},
		{"a DSN as a target", `"pubsub://acme-prod/clicks"`, `"postgres://u:p@h:5432/db/public/t"`, "/v1/clicks, sink"},
		{"a pattern that does not route", `"target": "gs://acme-oversize/clicks/"`, `"target": "bigquery://p/d/*"`, "/v1/clicks, archive"},
		{"an unknown role", `"role": "dead_letter"`, `"role": "mirror"`, "/v1/clicks"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(strings.Replace(goodManifest, c.from, c.to, 1)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, c.want)
			}
		})
	}
	if _, err := ParseManifest([]byte(`{not json`)); err == nil {
		t.Fatal("broken JSON was accepted")
	}
}

const v2Manifest = `{
  "kind": "gateway-manifest", "version": 2, "gateway": "events_gateway",
  "generated_at": "2026-10-11T12:00:00Z",
  "streams": [
    {"name": "tables", "path": "/v1/tables",
     "fields": [
       {"path": "format", "value": "json"},
       {"path": "buffer.flush.every", "value": "1m"},
       {"path": "buffer.workers", "value": 4, "default": true},
       {"path": "sink.naming.allow", "value": ["app_", "svc_"]},
       {"path": "sink.metastore.addr_from", "value": "BREVIS_METASTORE_ADDR"},
       {"path": "sink.into.write", "value": "merge"}
     ],
     "notes": {"": "One route, N tables.", "sink.into.write": "APPEND would land twice."},
     "destinations": [
       {"role": "sink", "kind": "auto_table", "target": "bigquery://acme-prod/landing/*", "routes": true}
     ]}
  ]
}`

func TestAVersionTwoManifestCarriesTheConfiguration(t *testing.T) {
	m, err := ParseManifest([]byte(v2Manifest))
	if err != nil {
		t.Fatal(err)
	}
	st := m.Streams[0]
	if st.Name != "tables" || len(st.Fields) != 6 || st.Notes[""] != "One route, N tables." {
		t.Fatalf("stream = %+v", st)
	}
	if f := st.Fields[2]; f.Path != "buffer.workers" || f.Value != float64(4) || !f.Default {
		t.Fatalf("buffer.workers = %+v", f)
	}
}

// The gateway's own describe output, as its golden file has it. Whatever the
// gateway emits, this engine reads -- the two modules share no type, and this
// is where they meet.
func TestTheGatewaysOwnManifestsAreAccepted(t *testing.T) {
	for _, name := range []string{"gateway.yaml", "gateway.local.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "gateway", "testdata", "describe", name+".golden.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseManifest(raw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The gateway keeps secrets out by construction; this is the second wall, for
// a gateway binary somebody else built. Refused whole, naming where.
func TestAConfigurationCarryingASecretIsRefused(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"a field named like one",
			`{"path": "sink.into.write", "value": "merge"}`,
			`{"path": "sink.password", "value": "hunter2"}`, "sink.password"},
		{"a DSN with a password",
			`"value": "BREVIS_METASTORE_ADDR"`,
			`"value": "redis://default:hunter2@cache:6379"`, "sink.metastore.addr_from"},
		{"a key in a query string",
			`{"path": "format", "value": "json"}`,
			`{"path": "format", "value": "https://api.x.com/v1?api_key=abc123"}`, "format"},
		{"a password in a note",
			`"APPEND would land twice."`,
			`"connect with postgres://loader:hunter2@db/shop"`, "notes"},
		{"an object where a value goes",
			`"value": "1m"`, `"value": {"every": "1m"}`, "buffer.flush.every"},
		{"the same path twice",
			`{"path": "format", "value": "json"}`,
			`{"path": "sink.into.write", "value": "json"}`, "sink.into.write"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(strings.Replace(v2Manifest, c.from, c.to, 1)))
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "/v1/tables") {
				t.Fatalf("err = %v, want one naming /v1/tables and %q", err, c.want)
			}
		})
	}
}

// A variable's NAME is the convention, not a leak.
func TestAVariableNameIsNotASecret(t *testing.T) {
	m := strings.Replace(v2Manifest, `{"path": "format", "value": "json"}`,
		`{"path": "sink.dsn_from", "value": "BREVIS_ORDERS_DSN"}`, 1)
	if _, err := ParseManifest([]byte(m)); err != nil {
		t.Fatal(err)
	}
}

// The hash is the configuration's identity: the same settings are the same
// version whatever the comments say, and any setting changed is a new one.
func TestTheConfigHashIsTheSettingsAndNotTheNotes(t *testing.T) {
	base, _ := ParseManifest([]byte(v2Manifest))
	h, body := base.Streams[0].ConfigHash()
	if !strings.HasPrefix(h, "sha256:") || len(body) == 0 {
		t.Fatalf("hash = %q, body = %s", h, body)
	}

	renoted, _ := ParseManifest([]byte(strings.Replace(v2Manifest, "APPEND would land twice.", "Reworded.", 1)))
	if h2, _ := renoted.Streams[0].ConfigHash(); h2 != h {
		t.Fatal("a comment changed the configuration's hash")
	}
	changed, _ := ParseManifest([]byte(strings.Replace(v2Manifest, `"1m"`, `"2m"`, 1)))
	if h3, _ := changed.Streams[0].ConfigHash(); h3 == h {
		t.Fatal("a setting changed and the hash did not")
	}
	v1, _ := ParseManifest([]byte(goodManifest))
	if h4, body := v1.Streams[0].ConfigHash(); h4 != "" || body != nil {
		t.Fatalf("a version-1 stream has no configuration, got %q", h4)
	}
}
