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
		{re: regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s@:?#]*:[^/\s@?#]+@`), what: "a URL with a password in it"},
		{re: regexp.MustCompile(`(^|[\s"'(=])[A-Za-z0-9._%+-]+:[^\s@/:]+@[A-Za-z0-9.-]+`), what: "a user and password before a host"},
		{re: regexp.MustCompile(`(?i)(^|[?&;\s])(password|passwd|pwd|secret|token|api_?key|access_token|client_secret|signature)=[^&\s;]+`), what: "a credential written as key=value"},
		{re: regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{12,}`), what: "a bearer token", shaped: tokenShaped},
		{re: regexp.MustCompile(`(?i)\bbasic\s+[A-Za-z0-9+/]{8,}={0,2}`), what: "a basic-auth credential", shaped: tokenShaped},
		{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), what: "a JSON web token"},
		{re: regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), what: "an AWS access key"},
		{re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), what: "a private key"},
		{re: regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|xapp-[A-Za-z0-9-]{10,}|[sr]k_(live|test)_[A-Za-z0-9]{10,}|sk-(ant|proj)-[A-Za-z0-9_-]{20,}|AIza[A-Za-z0-9_-]{30,}|SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,})`), what: "a provider's API token"},
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
		for _, m := range p.re.FindAllString(text, -1) {
			if p.shaped == nil || p.shaped(m) {
				return fmt.Errorf("%s: %s", path, p.what)
			}
		}
	}
	return nil
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
