package catalog

import "testing"

// The engine's wall in text recognises a credential by its SHAPE -- what a
// password in a URL, a token, a key look like -- never by the word written
// next to it. Each of these is refused.
func TestCredentialsInTextAreRefusedByTheirShape(t *testing.T) {
	for _, text := range []string{
		"postgres://loader:hunter2@db/shop",
		"redis://:hunter2@cache:6379",
		"https://user:pw@host/x",
		"mongodb+srv://u:p@cluster0.example.net/db",
		"old: app:hunter2@tcp(mysql:3306)/events",
		"user:pass@db.internal:5432",
		"host=db user=u password=p",
		"https://api.example.com/v1?api_key=abc123",
		"https://api.example.com/v1?token=abc123",
		"Authorization: Bearer abcdef1234567890",
		"Authorization: Basic bG9hZGVyOmh1bnRlcjI=",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NSJ9.SflKxwRJSMeKKF2QT4fwpM",
		"AKIAIOSFODNN7EXAMPLE",
		"-----BEGIN RSA PRIVATE KEY-----",
		"https://ghp_abcdefghijklmnopqrstuvwxyz0123@github.com/acme/repo",
		"ghs_abcdefghijklmnopqrstuvwxyz0123",
		"github_pat_11ABCDEFG0abcdefghijklmnop",
		"glpat-abcdefghij0123456789",
		"xoxb-1234567890-abcdefghijkl",
		"xapp-1-A0123456789-abcdef",
		"sk_live_abcdefghij1234",
		"sk_test_abcdefghij1234",
		"rk_live_abcdefghij1234",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"sk-proj-abcdefghijklmnopqrstuvwxyz",
		"AIzaSyA-abcdefghijklmnopqrstuvwxyz01234",
		"SG.abcdefghijklmnopqrst.abcdefghijklmnopqrstuvwxyz",
	} {
		if FindSecretInText("note", text) == nil {
			t.Errorf("accepted: %q", text)
		}
	}
}

// What a config's prose says, which a wall made of WORDS refused and a
// deploy failed on. Each is accepted.
func TestOrdinaryProseIsAccepted(t *testing.T) {
	for _, text := range []string{
		"The NAME of the environment variable holding the connection string, never the string.",
		"`keys_from` names the ENVIRONMENT VARIABLE holding the accepted keys.",
		"Authorization: Bearer <key>",
		"Bearer authentication is used",
		"Basic configuration follows",
		"basic authentication over TLS",
		"password: <your password here>",
		"password: required",
		"secret: none",
		"the token: see docs",
		"access_key: from the pod's role",
		"next_page_token=abc",
		"partition key=user_id",
		"token_url: where the login posts",
		"max_tokens: 20",
		"secret_name: the Kubernetes Secret to mount",
		"https://git@github.com/acme/repo",
		"https://example.com/x?email=a@b.com",
		"Sixty seconds is 1,440 a day: inside the quota with room for a retry.",
		"https://api.example.com/v1/items",
	} {
		if err := FindSecretInText("note", text); err != nil {
			t.Errorf("refused %q: %v", text, err)
		}
	}
}

// A setting NAMED like a credential with a value is refused however the name
// is spelled, and whatever the value's type.
func TestASettingNamedLikeACredentialIsRefusedInAnySpelling(t *testing.T) {
	cases := []struct {
		path  string
		value any
	}{
		{"sink.password", "hunter2"},
		{"sink.Password", "hunter2"},
		{"sink.PASSWORD", "hunter2"},
		{"sink.DSN", "postgres://h/db"},
		{"sink.apiKey", "abc"},
		{"sink.x-api-key", "abc"},
		{"sink.db_password", "hunter2"},
		{"sink.passwords", "hunter2"},
		{"sink.authorization", "Bearer abc"},
		{"sink.password", float64(123456)},
		{"sink.api_key", []any{"abc"}},
		{"source.headers.Authorization", "x"},
	}
	for _, c := range cases {
		if FindSecret(c.path, c.value) == nil {
			t.Errorf("accepted %s = %#v", c.path, c.value)
		}
	}
}

// And a name that only CONTAINS one of those words names something else.
func TestASettingThatOnlyMentionsACredentialIsAccepted(t *testing.T) {
	for path, value := range map[string]any{
		"sink.dsn_from":         "BREVIS_ORDERS_DSN",
		"listen.auth.keys_from": "BREVIS_INGEST_KEYS",
		"auth.token_ttl":        "30s",
		"sink.secret_name":      "projects/x/secrets/y",
		"sink.password_policy":  "strict",
		"sink.credentials_file": "/etc/x.json",
		"source.query_params":   []any{"api_key", "page"},
		"source.headers":        []any{"Authorization", "X-Tenant"},
		"identity.source_key":   "table_name",
		"sink.ordering_key":     "tenant",
		"sink.topic":            "orders",
		"target.dedup":          "merge",
		"source.auth":           "login",
	} {
		if err := FindSecret(path, value); err != nil {
			t.Errorf("refused %s = %#v: %v", path, value, err)
		}
	}
}
