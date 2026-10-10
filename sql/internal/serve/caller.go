package serve

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Caller is one named bearer.
//
// CHECKPOINT B's F7: the audit line carried connection, hash, rows, bytes,
// duration and outcome, and nothing about WHO asked. With one operator that
// was a note; with two it means the log cannot answer the only question it is
// read for.
//
// IDENTITY IS THE CREDENTIAL, not a header. A caller that announced its own
// name would be making a CLAIM, and an audit of claims is an audit of
// whatever an attacker typed. A token the operator issued is a FACT: the line
// cannot say anything the holder of that token did not actually hold.
//
// The Name is not a secret -- it is printed at boot and written to every
// line. The Token is, and appears in neither.
type Caller struct {
	// Name is what the audit line carries. It reaches a JSON document and
	// somebody's grep, so it is checked at boot rather than escaped later.
	Name string

	// Token is the bearer this caller presents.
	Token string
}

// callerName is what a name may be: something that survives a JSON line, a
// log pipeline and a shell quote without anybody thinking about it.
func validName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// callers normalises Options into the one list the service authenticates
// against, and refuses at boot what cannot be made coherent.
//
// THE UNNAMED TOKEN IS A CALLER WITH NO NAME, folded in here so there is a
// single list rather than two sources that can disagree. Every deployment
// that sets BREVIS_SQL_SERVE_TOKEN keeps working, and its line carries no
// caller -- which is itself the finding: a shared secret with nobody's name
// on it.
func callers(opt Options) ([]Caller, error) {
	var out []Caller
	if opt.Token != "" {
		out = append(out, Caller{Token: opt.Token})
	}
	names := map[string]bool{}
	secrets := map[string]bool{}
	for _, c := range opt.Callers {
		if !validName(c.Name) {
			return nil, fmt.Errorf("serve: caller name %q is not usable: letters, digits, "+
				"'-', '_' and '.', up to 64 of them. It is written into every audit line", c.Name)
		}
		if strings.TrimSpace(c.Token) == "" {
			return nil, fmt.Errorf("serve: caller %q has no token", c.Name)
		}
		if names[c.Name] {
			return nil, fmt.Errorf("serve: caller %q is declared twice", c.Name)
		}
		// TWO NAMES WITH ONE SECRET IS NOT TWO CALLERS. It is one credential
		// and a log that lies about which of them used it, which is worse
		// than no name at all. The refusal NAMES THE NAMES and never the
		// token.
		if secrets[c.Token] {
			return nil, fmt.Errorf("serve: caller %q shares a token with another caller, "+
				"so the audit line could not tell them apart", c.Name)
		}
		names[c.Name], secrets[c.Token] = true, true
		out = append(out, c)
	}
	return out, nil
}

// callerKey carries the authenticated caller from the middleware to the
// handler that writes the line.
type callerKey struct{}

func whoAsked(ctx context.Context) string {
	name, _ := ctx.Value(callerKey{}).(string)
	return name
}

// authenticated refuses anything without a bearer this service issued, and
// hands the handler the name that bearer belongs to.
func (s *Service) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.callers) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		given, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

		// EVERY CANDIDATE, WITHOUT STOPPING EARLY. Each comparison is
		// constant time because its duration is otherwise a measurement of
		// how much of the token is right; returning on the first match would
		// put the list's ORDER back into the timing.
		name, ok := "", false
		for _, c := range s.callers {
			if subtle.ConstantTimeCompare([]byte(given), []byte(c.Token)) == 1 {
				name, ok = c.Name, true
			}
		}
		if !ok {
			// AUDITED, which it was not: the 401 happened before any handler
			// ran and nothing was written, so "somebody is guessing the
			// token" could not be seen. What was PRESENTED is never written
			// down -- a near miss in a log is the token itself, one line
			// later.
			s.audit(record{Event: "request", Outcome: "unauthenticated"}, time.Now())
			refuse(w, http.StatusUnauthorized, "a valid bearer token is required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, name)))
	})
}
