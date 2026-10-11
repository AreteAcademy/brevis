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
