package gateway

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// Auth is who may write.
//
// It mirrors what the engine does rather than inventing a second rule:
// BREVIS_ENV=local leaves the endpoint open, because asking for a token on
// every `docker compose up` only teaches a team to turn authentication off --
// and outside local the process REFUSES TO START without one.
//
// An ingestion endpoint is a write endpoint on somebody's topic or table. An
// unauthenticated one on a routable address is not a gateway with a gap, it is
// an open relay.
type Auth struct {
	// Type is `bearer` today. Empty means none, which is only allowed locally.
	Type string `yaml:"type"`

	// KeysFrom names the ENVIRONMENT VARIABLE holding the accepted keys, comma
	// separated. The keys themselves are never in the file, which is the rule
	// `secrets:` follows in a workflow and for the same reason: the file is in
	// git.
	KeysFrom string `yaml:"keys_from"`
}

const AuthBearer = "bearer"

// EnvLocal is the value that leaves the endpoint open, and it is spelled the
// same as the engine's so an operator learns one word.
const EnvLocal = "local"

func (a *Auth) check(env string) error {
	switch a.Type {
	case "":
		if env != EnvLocal {
			return fmt.Errorf("BREVIS_ENV=%s requires `listen.auth`: an ingestion "+
				"endpoint with no authentication is an open relay on somebody's "+
				"topic. Declare `{type: bearer, keys_from: <env var>}`, or run with "+
				"BREVIS_ENV=%s if this is a laptop", env, EnvLocal)
		}
		return nil
	case AuthBearer:
		if strings.TrimSpace(a.KeysFrom) == "" {
			// Half configured is worse than nothing: whoever wrote `bearer`
			// believes they closed the door.
			return fmt.Errorf("`listen.auth.type` is %q and `keys_from` is empty, "+
				"so nothing would be accepted -- and whoever wrote the type believes "+
				"the door is closed", AuthBearer)
		}
		return nil
	}
	return fmt.Errorf("`listen.auth.type` is %q (use %s)", a.Type, AuthBearer)
}

// keys reads the accepted keys out of the environment at START, not per
// request: a key list that changes under a running process is a door that opens
// without anybody deciding it should.
func (a Auth) keys() ([]string, error) {
	if a.Type == "" {
		return nil, nil
	}
	raw, set := os.LookupEnv(a.KeysFrom)
	if !set || strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("`listen.auth.keys_from` names %s and it is empty, so "+
			"every request would be refused", a.KeysFrom)
	}
	var out []string
	for _, k := range strings.Split(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no key", a.KeysFrom)
	}
	return out, nil
}

// guard wraps a handler with the bearer check. A nil key list is open.
func guard(keys []string, next http.Handler) http.Handler {
	if len(keys) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !accepted(keys, strings.TrimSpace(token)) {
			// No detail. "unknown key" and "no key" are the same answer here:
			// the difference is only useful to somebody guessing.
			w.Header().Set("WWW-Authenticate", `Bearer realm="brevis-gateway"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// accepted compares in constant time, and compares against EVERY key rather
// than returning on the first match: an early return leaks which prefix was
// right through timing.
func accepted(keys []string, token string) bool {
	var ok int
	for _, k := range keys {
		ok |= subtle.ConstantTimeCompare([]byte(k), []byte(token))
	}
	return ok == 1
}
