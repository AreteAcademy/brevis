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

	// A URL with a password in it: scheme://user:password@host.
	secretUserinfo = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s@:]+:[^/\s@]*@`)

	// A credential as a key=value pair: in a query string, in a libpq DSN, in a
	// sentence. The names are the SDK's querySecrets (sdk/redact.go) and the
	// DSN keywords that carry one.
	secretPair = regexp.MustCompile(`(?i)(^|[?&;\s])(key|api_?key|token|access_token|auth|password|passwd|pwd|secret|signature|sig)=[^&\s;]+`)
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
	if secretUserinfo.MatchString(text) {
		return fmt.Errorf("%s: a URL with a password in it", path)
	}
	if secretPair.MatchString(text) {
		return fmt.Errorf("%s: a credential written as key=value", path)
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
