package core

import "testing"

// SafeURL shows what is KNOWN to be safe and hides the rest. Every "hidden"
// case is a shape a review got past the earlier rule, which listed what looked
// suspicious instead.
func TestSafeURLHidesWhatIsNotKnownToBeSafe(t *testing.T) {
	cases := map[string]string{
		// Tokens in the path, of every shape: no digit, only digits, base64url
		// split by separators, just under a length limit, encoded slashes.
		"https://hooks.slack.com/services/T0ABCDEFG/B0ABCDEFG/XxkLmNpQrStUvWxYzAbCd":          "https://hooks.slack.com/services/…/…/…",
		"https://api.example.com/hooks/ABCDEFGHIJKLMNOPQRSTUVWXYZabcde/x":                     "https://api.example.com/hooks/…/x",
		"https://api.example.com/hooks/1234567890123456789012345678901/x":                     "https://api.example.com/hooks/…/x",
		"https://api.example.com/k/AbCd-EfGh_IjKl-MnOp/x":                                     "https://api.example.com/k/…/x",
		"https://api.example.com/hooks/abcdef012345678/x":                                     "https://api.example.com/hooks/…/x",
		"https://api.example.com/x/Ab3dE%2FfG7hIjK%2BlMn0pQrS/y":                              "https://api.example.com/x/…/y",
		"https://api.telegram.org/bot123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw/getUpdates": "https://api.telegram.org/…/getUpdates",
		"https://api.example.com/v1;jsessionid=A1B2C3D4E5F6A7B8C9D0E1F2/items":                "https://api.example.com/v1/items",
		// A token as a host label.
		"https://SEKRETTOKEN123456.ngrok.io/x":                "https://….ngrok.io/x",
		"https://abcdef0123456789abcdef0123456789.ngrok.io/x": "https://….ngrok.io/x",
		// Credentials in the authority, however they are written.
		"https://loader:pw@api.example.com/v1":              "https://api.example.com/v1",
		"https://loader:12345#x@api.example.com/v1":         "",
		"s3://AKIAIOSFODNN7EXAMPLE:secret@acme-raw/orders/": "s3://acme-raw/orders/",
		// No scheme: a `@` is an authority nobody parsed.
		"u:p/w@host/x":      "",
		"loader:pw@host/x":  "",
		"/data/token:abc/x": "/data/…/x",
	}
	for in, want := range cases {
		if got := SafeURL(in); got != want {
			t.Errorf("SafeURL(%q)\n  got  %q\n  want %q", in, got, want)
		}
	}
}

// And what an address is made of, every day, stays readable.
func TestSafeURLKeepsWhatIsKnownToBeSafe(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.example.com/v1/items":                                   "https://api.example.com/v1/items",
		"https://api.example.com/v2.1/orders/123456/lines":                   "https://api.example.com/v2.1/orders/123456/lines",
		"https://api.example.com/items/550e8400-e29b-41d4-a716-446655440000": "https://api.example.com/items/550e8400-e29b-41d4-a716-446655440000",
		"https://api.example.com/reports/2024-01-01":                         "https://api.example.com/reports/2024-01-01",
		"https://api.example.com/x?email=a@b.com":                            "https://api.example.com/x",
		"https://api.telegram.org/getUpdates":                                "https://api.telegram.org/getUpdates",
		"gs://acme-landing/orders/":                                          "gs://acme-landing/orders/",
		"s3://zarv-development-94b6-brevis-staging/extracts/":                "s3://zarv-development-94b6-brevis-staging/extracts/",
		"file:///tmp/x.csv":                                                  "file:///tmp/x.csv",
		"./dead-letter/clicks/":                                              "./dead-letter/clicks/",
		"entrada.ndjson":                                                     "entrada.ndjson",
		"http://[::1]:8080/v1/items":                                         "http://[::1]:8080/v1/items",
		"https://my-repository-name/x":                                       "https://my-repository-name/x",
	} {
		if got := SafeURL(in); got != want {
			t.Errorf("SafeURL(%q)\n  got  %q\n  want %q", in, got, want)
		}
	}
}
