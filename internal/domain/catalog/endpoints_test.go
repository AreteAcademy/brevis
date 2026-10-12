package catalog

import (
	"strings"
	"testing"
)

const goodEndpoints = `{
  "version": 1, "kind": "endpoints", "name": "metrics-api",
  "connection": "warehouse", "dialect": "postgres",
  "cache": {"type": "redis", "addr_from": "CACHE_ADDR", "ttl": "6h0m0s", "max_entry": 4194304},
  "endpoints": [{
    "name": "clicks", "path": "/v1/clicks", "tenant": "key",
    "params": [{"name": "period", "type": "period", "default": "last_7_days"},
               {"name": "order", "type": "enum", "values": ["asc", "desc"]}],
    "limits": {"timeout": "10s", "max_rows": 500, "max_bytes_billed": 1073741824},
    "reads": ["postgres://analytics/marts/clicks_daily"],
    "notes": {"": "Clicks per day."}
  }],
  "notes": {"cache": "Kept until the model is rebuilt."}
}`

func TestAGoodEndpointsManifestIsRead(t *testing.T) {
	m, err := ParseEndpoints([]byte(goodEndpoints))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "metrics-api" || len(m.Endpoints) != 1 || m.Endpoints[0].Reads[0] != "postgres://analytics/marts/clicks_daily" {
		t.Errorf("read %+v", m)
	}
}

// REFUSED WHOLE, naming where, and never repeating a credential: a console
// showing half an API, or a log line holding a password, are both worse
// than a publish that failed.
func TestABadEndpointsManifestIsRefusedWhole(t *testing.T) {
	for name, c := range map[string]struct{ from, to, says string }{
		"another kind":                        {`"kind": "endpoints"`, `"kind": "gateway-manifest"`, "kind"},
		"a version it does not read":          {`"version": 1`, `"version": 2`, "version 2"},
		"no name":                             {`"name": "metrics-api"`, `"name": ""`, "names no API"},
		"an unversioned path":                 {`"path": "/v1/clicks"`, `"path": "clicks"`, "path"},
		"a target with a credential":          {`"postgres://analytics/marts/clicks_daily"`, `"postgres://u:pw@h/analytics/marts/x"`, "target"},
		"a note with a token":                 {`"Clicks per day."`, `"the key is ghp_0123456789abcdefghijklmnopqrstuv"`, "credential"},
		"a default with a token":              {`"default": "last_7_days"`, `"default": "ghp_0123456789abcdefghijklmnopqrstuv"`, "credential"},
		"a tenant it does not know":           {`"tenant": "key"`, `"tenant": "maybe"`, "tenant"},
		"a cache variable holding an address": {`"addr_from": "CACHE_ADDR"`, `"addr_from": "redis://:pw@h:6379"`, "addr_from"},
		"an endpoint twice":                   {`"endpoints": [{`, `"endpoints": [{"name": "clicks", "path": "/v1/other", "tenant": "none", "params": [], "limits": {}, "reads": []}, {`, "twice"},
	} {
		raw := strings.Replace(goodEndpoints, c.from, c.to, 1)
		_, err := ParseEndpoints([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(err.Error(), "ghp_") || strings.Contains(err.Error(), ":pw@") {
			t.Errorf("%s: the error repeats the credential: %v", name, err)
		}
	}
}
