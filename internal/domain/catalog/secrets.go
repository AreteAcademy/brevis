package catalog

import (
	"fmt"
	"regexp"
	"strings"
)

// The second wall.
//
// A writer's configuration reaches the console from the writer's own
// description of itself -- `gateway describe`, the SDK's pipeline marker --
// and each keeps credentials out by construction: a variable's NAME under a
// `_from` key, an allowlist per driver. That is the first wall, and it is
// code in another module, in a binary somebody else may have built.
//
// So the engine looks too, and REFUSES rather than redacts, the way
// ValidTarget does: a description carrying a password is a writer with a bug,
// and storing it with the password blanked would hide the bug and keep the
// rest of a description nobody can now trust. Everybody with a console login
// reads /data; a secret stored here is a secret published.
var (
	// A setting NAMED like a credential. `source_key`, `ordering_key` and
	// `dsn_from` are not: the first two are field names, the third is the
	// convention this exists to protect.
	secretName = regexp.MustCompile(`(^|_)(dsn|password|passwd|pwd|secret|token|credentials?|api_?key|access_key|private_key)($|_)`)

	// What a credential looks like in TEXT -- a value, a note -- each with
	// the words the refusal uses. A comment is free prose written by people,
	// and a commented-out old setting is the most common way a secret ends
	// up in a config file, so prose is held to the same wall as values.
	//
	// A placeholder is not a credential: a value starting with `<`, `$`,
	// `*` or `{` -- `<your password>`, `$PG_PASSWORD`, `***`, `${X}` -- is
	// let through, or every example in a comment would fail a deploy.
	secretInText = []struct {
		re   *regexp.Regexp
		what string
	}{
		{regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s@]+@`), "a URL with a user or password in it"},
		{regexp.MustCompile(`[^\s:/@]+:[^\s@]+@(tcp|unix)\(`), "a MySQL DSN with a password in it"},
		{regexp.MustCompile(`(?i)(^|[?&;\s])(key|api_?key|token|access_token|auth|password|passwd|pwd|secret|signature|sig)=[^&\s;]+`), "a credential written as key=value"},
		{regexp.MustCompile(`(?i)[?&][A-Za-z0-9_-]*(signature|token|secret|password|credential|api[-_]?key)=[^&\s]+`), "a credential in a query string"},
		{regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[-_]?key|apikey|access[-_]?key|private[-_]?key)\s*[:=]\s*[^\s<$*{]`), "a credential written as a setting"},
		// An Authorization header is matched by its scheme and not by its
		// name, so `Authorization: Bearer <key>` stays a readable example.
		{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{12,}`), "a bearer token"},
		{regexp.MustCompile(`(?i)\bbasic\s+[A-Za-z0-9+/]{12,}=*`), "a basic-auth credential"},
		{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "a JSON web token"},
		{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), "an AWS access key"},
		{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), "a private key"},
		{regexp.MustCompile(`\b(ghp_[A-Za-z0-9]{20,}|gho_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|sk_live_[A-Za-z0-9]{10,}|AIza[A-Za-z0-9_-]{30,})`), "a provider's API token"},
	}
)

// FindSecret walks a value -- a string, a number, a list, an object -- and
// names the first place a credential shows, by path. Nil when there is none.
func FindSecret(path string, v any) error {
	switch v := v.(type) {
	case string:
		if v != "" && secretName.MatchString(lastSegment(path)) && !strings.HasSuffix(path, "_from") {
			return fmt.Errorf("%s: a setting named like a credential has a value; name the variable that holds it (`%s_from`)", path, lastSegment(path))
		}
		return FindSecretInText(path, v)
	case []any:
		for i, e := range v {
			if err := FindSecret(fmt.Sprintf("%s[%d]", path, i), e); err != nil {
				return err
			}
		}
	case map[string]any:
		for k, e := range v {
			if err := FindSecret(joinPath(path, k), e); err != nil {
				return err
			}
		}
	}
	return nil
}

// FindSecretInText is FindSecret for prose -- a comment, a note -- where only
// what the text SAYS can be a credential: a sentence that mentions dsn_from
// is not a setting named like one.
func FindSecretInText(path, text string) error {
	for _, p := range secretInText {
		if p.re.MatchString(text) {
			return fmt.Errorf("%s: %s", path, p.what)
		}
	}
	return nil
}

func lastSegment(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}
