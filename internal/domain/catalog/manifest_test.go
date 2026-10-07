package catalog

import (
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
		{"wrong version", `"version": 1`, `"version": 2`, "version"},
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
