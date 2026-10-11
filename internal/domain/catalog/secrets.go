package catalog

import (
	"encoding/base64"
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
	// A setting NAMED like a credential: the credential word is the LAST part
	// of its normalised name (see credentialName). `api_key`, `db_password`,
	// `apiKey`, `x-api-key`, `Password` are; `token_ttl`, `secret_name`,
	// `password_policy`, `source_key` and `dsn_from` -- the convention this
	// exists to protect -- are not: they name something about a credential,
	// or a variable holding one.
	secretName = regexp.MustCompile(`(^|_)(dsn|passwords?|passwd|pwd|pass|pw|passphrase|secrets?|secret_key|signing_key|client_key|tokens?|credentials?|creds|api_?keys?|access_key|private_key(_[a-z0-9]+)?|auth_token|authorization|bearer|cookies?|session_(id|key|token))$`)

	// An acronym before a word (`DBPassword`, `JWTSecret`) first, then a
	// lower-case letter or digit before a capital (`apiKey`).
	acronymBoundary = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	camelBoundary   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	indexSuffix     = regexp.MustCompile(`(\[\d+\])+$`)

	// What a credential looks like in TEXT -- a value, a note -- each with
	// the words the refusal uses.
	//
	// BY ITS SHAPE, NEVER BY THE WORD NEXT TO IT. A rule made of words --
	// "password:", "token=" -- both missed the next spelling (`pass:`,
	// `credentials:`, JSON) and refused prose ("secret: none", "Bearer
	// authentication is used") and failed a deploy on it. The words were
	// never the wall: a description shows only an allowlist (SDK drivers) and
	// the comments an author marked `#:` (a gateway). What this catches is the
	// credential that got into those anyway, by what it looks like.
	secretInText = []struct {
		re   *regexp.Regexp
		what string
		// shaped, when set, must also hold for the match: a word after
		// "Bearer" is prose, a token after it has digits or punctuation.
		shaped func(match string) bool
	}{
		// The password runs to the `@` before the host, whatever it holds:
		// a generated one often has `/`, `#` or `?` in it.
		{re: regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s/@:]*:[^\s@]*@[A-Za-z0-9.\[-]`), what: "a URL with a password in it"},
		// A token as the user, with no password: GitHub's classic tokens,
		// Sentry's DSN. A plain user (`git@`) has no digit and is short.
		{re: regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://([^\s/@:?#]*[0-9][^\s/@:?#]*|[^\s/@:?#]{16,})@`), what: "a URL with a token as its user"},
		{re: regexp.MustCompile(`(^|[\s"'(=])[A-Za-z0-9._%+-]+:[^\s@/:]+@[A-Za-z0-9.-]+`), what: "a user and password before a host"},
		// The name may carry a prefix (`refresh_token`, `X-Amz-Signature`);
		// a pagination token (`next_page_token`, `cursor`) is not a secret.
		{re: regexp.MustCompile(`(?i)(^|[?&;\s])[A-Za-z0-9_-]*(password|passwd|pwd|secret|token|api_?key|signature|sig|security[-_]token|secret_access_key)=[^&\s;]+`), what: "a credential written as key=value", shaped: notPagination},
		{re: regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{12,}`), what: "a bearer token", shaped: tokenShaped},
		{re: regexp.MustCompile(`(?i)\bbasic\s+[A-Za-z0-9+/]{8,}={0,2}`), what: "a basic-auth credential", shaped: basicShaped},
		{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), what: "a JSON web token"},
		{re: regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), what: "an AWS access key"},
		{re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY( BLOCK)?-----`), what: "a private key"},
		{re: regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|xapp-[A-Za-z0-9-]{10,}|[sr]k_(live|test)_[A-Za-z0-9]{10,}|sk-(ant|proj)-[A-Za-z0-9_-]{20,}|AIza[A-Za-z0-9_-]{30,}|SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}|npm_[A-Za-z0-9]{30,}|hf_[A-Za-z0-9]{30,}|shp(at|ss)_[A-Fa-f0-9]{30,}|dop_v1_[a-f0-9]{60,})`), what: "a provider's API token"},
	}
)

// FindSecret walks a value -- a string, a number, a list, an object -- and
// names the first place a credential shows, by path. Nil when there is none.
//
// The NAME is checked before the value's type: a password written as a
// number, or as a list of one, is a password.
func FindSecret(path string, v any) error {
	if name := credentialName(path); secretName.MatchString(name) && hasValue(v) {
		return fmt.Errorf("%s: a setting named like a credential has a value; name the variable that holds it (`%s_from`)", path, lastSegment(path))
	}
	switch v := v.(type) {
	case string:
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

// credentialName is a path's last segment as the name rule reads it: list
// indexes dropped, camelCase and kebab-case to snake_case, lower case --
// `x-api-key`, `apiKey` and `API_KEY` are one name.
func credentialName(path string) string {
	name := indexSuffix.ReplaceAllString(lastSegment(path), "")
	name = acronymBoundary.ReplaceAllString(name, "${1}_${2}")
	name = camelBoundary.ReplaceAllString(name, "${1}_${2}")
	return strings.Trim(strings.ToLower(strings.ReplaceAll(name, "-", "_")), "_")
}

// hasValue says a setting holds something: a bool says nothing secret, and an
// empty string or list holds nothing.
func hasValue(v any) bool {
	switch v := v.(type) {
	case nil, bool:
		return false
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

// FindSecretInText is FindSecret for prose -- a comment, a note -- where only
// what the text SAYS can be a credential: a sentence that mentions dsn_from
// is not a setting named like one.
func FindSecretInText(path, text string) error {
	for _, p := range secretInText {
		for _, m := range p.re.FindAllString(text, -1) {
			if p.shaped == nil || p.shaped(m) {
				return fmt.Errorf("%s: %s", path, p.what)
			}
		}
	}
	return nil
}

// notPagination says a key=value match is not a pagination token: a page or a
// cursor is where a listing resumes, and every paginated API sends one.
func notPagination(match string) bool {
	name, _, _ := strings.Cut(strings.ToLower(match), "=")
	for _, w := range []string{"page", "cursor", "continuation", "sync"} {
		if strings.Contains(name, w) {
			return false
		}
	}
	return true
}

// basicShaped says whether what follows "Basic" is a credential: base64 that
// decodes to `user:password`, or anything token-shaped. "configuration" is
// neither.
func basicShaped(match string) bool {
	fields := strings.Fields(match)
	token := fields[len(fields)-1]
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if raw, err := enc.DecodeString(token); err == nil && strings.Contains(string(raw), ":") {
			return true
		}
	}
	return tokenShaped(match)
}

// tokenShaped says whether the credential after "Bearer" or "Basic" looks
// like one: a digit, or the punctuation tokens and base64 carry. A word --
// "authentication", "configuration" -- has neither.
func tokenShaped(match string) bool {
	fields := strings.Fields(match)
	token := fields[len(fields)-1]
	return strings.ContainsAny(token, "0123456789._~+/=-")
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
