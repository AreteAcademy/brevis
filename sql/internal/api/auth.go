package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// Auth is who may read, and as which tenant.
//
// THE GATEWAY'S RULE, AND ONE MORE. BREVIS_ENV=local leaves the API open and
// outside it the process refuses to start without a key list -- spelled the
// way the gateway and the engine spell it, so an operator learns one word.
// The addition is the TENANT: every key belongs to one, and that tenant is
// bound into every query as `{{ tenant }}`. A caller never says which tenant
// it is, so it can never say a different one.
type Auth struct {
	// Type is `bearer`.
	Type string `yaml:"type"`

	// KeysFrom names the ENVIRONMENT VARIABLE holding the key list -- one
	// `<sha256 of the key, hex>=<tenant>` per line. The file is in git, and
	// the variable holds no key either: a hash is what lets an environment
	// dump, a CI log or a `kubectl describe` show this variable without
	// handing anybody a way in.
	KeysFrom string `yaml:"keys_from"`
}

const (
	authBearer = "bearer"

	// EnvLocal is the value that leaves the API open. The engine's word.
	EnvLocal = "local"

	// LocalTenant is the tenant every request is, locally and without auth.
	LocalTenant = "local"

	// minKeyLen is the shortest key `key hash` will hash: 32 characters of
	// whatever `openssl rand -hex 16` gives is 128 bits.
	minKeyLen = 32
)

// tenantShape is what a tenant may look like: an id, a slug or a UUID, never
// something that needs quoting in a log line.
var tenantShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func (a *Auth) check() error {
	switch a.Type {
	case authBearer:
		if strings.TrimSpace(a.KeysFrom) == "" {
			// Half configured is worse than nothing: whoever wrote `bearer`
			// believes they closed the door.
			return fmt.Errorf("`listen.auth.type` is %q and `keys_from` is empty, so nothing "+
				"would be accepted", authBearer)
		}
		return nil
	}
	return fmt.Errorf("`listen.auth.type` is %q (use %s)", a.Type, authBearer)
}

// keyring is the accepted keys' hashes and the tenant each belongs to.
type keyring struct {
	hashes  [][sha256.Size]byte
	tenants []string
}

// loadKeys reads the key list at START, never per request: a list that
// changes under a running process is a door that opens without anybody
// deciding it should.
//
// AN ERROR NAMES THE LINE AND NEVER QUOTES IT. A line is a credential's
// hash, and an error ends up in a log.
func loadKeys(variable string) (*keyring, error) {
	raw := os.Getenv(variable)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("`listen.auth.keys_from` names %s and it is empty, so every "+
			"request would be refused", variable)
	}
	k := &keyring{}
	seen := map[[sha256.Size]byte]bool{}
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		hash, tenant, found := strings.Cut(line, "=")
		sum, err := hex.DecodeString(hash)
		switch {
		case !found || err != nil || len(sum) != sha256.Size:
			return nil, fmt.Errorf("%s, line %d: not `<sha256 hex>=<tenant>`; "+
				"`brevis-sql-api key hash --tenant <t>` prints one", variable, i+1)
		case !tenantShape.MatchString(tenant):
			return nil, fmt.Errorf("%s, line %d: the tenant is not an id "+
				"(letters, digits and _.:- , at most 128)", variable, i+1)
		}
		var h [sha256.Size]byte
		copy(h[:], sum)
		if seen[h] {
			return nil, fmt.Errorf("%s, line %d: that key is listed twice, and a key "+
				"belongs to one tenant", variable, i+1)
		}
		seen[h] = true
		k.hashes = append(k.hashes, h)
		k.tenants = append(k.tenants, tenant)
	}
	return k, nil
}

// tenant finds the tenant a presented key belongs to.
//
// EVERY HASH IS COMPARED, in constant time, with no early return -- the
// gateway's reason: an early return says through timing how far along the
// list the right one was.
func (k *keyring) tenant(key string) (string, bool) {
	sum := sha256.Sum256([]byte(key))
	found := -1
	for i, h := range k.hashes {
		if subtle.ConstantTimeCompare(h[:], sum[:]) == 1 {
			found = i
		}
	}
	if found < 0 {
		return "", false
	}
	return k.tenants[found], true
}

// count is how many keys and tenants there are, for the start-up banner.
func (k *keyring) count() (keys, tenants int) {
	distinct := map[string]bool{}
	for _, t := range k.tenants {
		distinct[t] = true
	}
	return len(k.hashes), len(distinct)
}

// guard turns away a request with no valid key, and hands the next handler
// the tenant of the one it had.
//
// EVERY PATH BUT /health, unknown ones included: a 404 for a path that is
// not there and a 401 for one that is would draw the API's map in status
// codes for whoever has no key. /health stays outside, because a readiness
// probe that needed a token would report the API down whenever the token
// was wrong -- a different outage from the one it exists to see.
func guard(k *keyring, next func(http.ResponseWriter, *http.Request, string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k == nil {
			next(w, r, LocalTenant)
			return
		}
		key, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		tenant, known := "", false
		if bearer {
			tenant, known = k.tenant(strings.TrimSpace(key))
		}
		if !known {
			// No detail. "unknown key" and "no key" are the same answer: the
			// difference is only useful to somebody guessing.
			w.Header().Set("WWW-Authenticate", `Bearer realm="brevis-sql-api"`)
			refuse(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r, tenant)
	})
}

// KeyLine is the line a key list holds for one key: what `brevis-sql-api key
// hash` prints, so nobody hand-rolls the hash and gets the encoding wrong.
func KeyLine(key, tenant string) (string, error) {
	if len(key) < minKeyLen {
		return "", fmt.Errorf("the key is %d characters; at least %d (`openssl rand -hex 16` gives 32)",
			len(key), minKeyLen)
	}
	if !tenantShape.MatchString(tenant) {
		return "", fmt.Errorf("the tenant %q is not an id (letters, digits and _.:- , at most 128)", tenant)
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:]) + "=" + tenant, nil
}
