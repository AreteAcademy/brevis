package catalog

import "testing"

// Every shape a review found getting past the wall, in prose as a comment
// carries it. Each is refused.
func TestCredentialsInProseAreRefused(t *testing.T) {
	for _, text := range []string{
		"password: hunter2",
		"password = hunter2",
		"PG_PASSWORD=hunter2",
		"sslpassword=hunter2",
		"api_key: 9f8e7d6c5b4a",
		"client_secret: 9f8e7d6c5b4a",
		"Authorization: Bearer abcdef1234567890",
		"Authorization: Basic bG9hZGVyOmh1bnRlcjI=",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NSJ9.SflKxwRJSMeKKF2QT4fwpM",
		"AKIAIOSFODNN7EXAMPLE",
		"-----BEGIN RSA PRIVATE KEY-----",
		"redis://:hunter2@cache:6379",
		"https://ghp_abcdefghijklmnopqrstuvwxyz0123@github.com/acme/repo",
		"old: app:hunter2@tcp(mysql:3306)/events",
		"https://bucket.s3.amazonaws.com/x?X-Amz-Signature=abc123",
		"https://api.example.com/v1?api-key=abc123",
		"https://api.example.com/v1?client_secret=abc123",
		"xoxb-1234567890-abcdefghijkl",
		"sk_live_abcdefghij1234",
		"postgres://loader:hunter2@db/shop",
		"https://user:pw@host/x",
	} {
		if FindSecretInText("note", text) == nil {
			t.Errorf("accepted: %q", text)
		}
	}
}

// And what a config's prose says all the time, which must not fail a deploy.
func TestOrdinaryProseIsAccepted(t *testing.T) {
	for _, text := range []string{
		"The NAME of the environment variable holding the connection string, never the string.",
		"`keys_from` names the ENVIRONMENT VARIABLE holding the accepted keys.",
		"Authorization: Bearer <key>",
		"password: <your password here>",
		"password: $PG_PASSWORD",
		"token_url: where the login posts",
		"max_tokens: 20",
		"secret_name: the Kubernetes Secret to mount",
		"Sixty seconds is 1,440 a day: inside the quota with room for a retry.",
		"https://api.example.com/v1/items",
	} {
		if err := FindSecretInText("note", text); err != nil {
			t.Errorf("refused %q: %v", text, err)
		}
	}
}
