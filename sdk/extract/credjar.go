package extract

import (
	"net/http"
	"net/url"
	"sync"
)

// credentialJar keeps the credential OUT of the jar and on the header.
//
// Go's jar matches a cookie by path prefix, and a cookie with no Path inherits
// the directory of the URL that issued it. With the source on
// /api/proxy/occurrences the credential was pinned to /api/proxy, and the
// refresh on /api/auth/session went without it, as the first consumer hit it
// on v0.9.x. Marking
// Path=/ on the seed fixes half of it: the cookie REISSUED by the refresh gets
// pinned again, now to /api/auth, and the pages carry on with the old value. A
// refresh that does not reach the pages refreshed nothing.
//
// So the credential stops being a jar cookie and becomes a header, which
// applies to every request regardless of path. The jar goes on existing for the
// other cookies -- and v0.26.0's invariant holds: every cookie lives in one
// place, and no name goes twice.
//
// The useful side effect is what the Store needs: the rotated value is in hand,
// rather than buried in the jar.
type credentialJar struct {
	inner http.CookieJar

	// names are the cookies carrying the credential. Fixed after assembly.
	names map[string]bool

	mu        sync.Mutex
	rotations map[string]string
}

func newCredentialJar(inner http.CookieJar, names []string) *credentialJar {
	j := &credentialJar{inner: inner, names: make(map[string]bool, len(names))}
	for _, n := range names {
		j.names[n] = true
	}
	return j
}

// SetCookies diverts what is a credential and hands the rest to the real jar.
func (j *credentialJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	remaining := cookies[:0:0]
	for _, c := range cookies {
		if j.names[c.Name] {
			j.mu.Lock()
			if j.rotations == nil {
				j.rotations = map[string]string{}
			}
			j.rotations[c.Name] = c.Value
			j.mu.Unlock()
			continue
		}
		remaining = append(remaining, c)
	}
	if len(remaining) > 0 && j.inner != nil {
		j.inner.SetCookies(u, remaining)
	}
}

func (j *credentialJar) Cookies(u *url.URL) []*http.Cookie {
	if j.inner == nil {
		return nil
	}
	return j.inner.Cookies(u)
}

// Rotations returns the values reissued since the last call, or nil.
func (j *credentialJar) Rotations() map[string]string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.rotations) == 0 {
		return nil
	}
	out := j.rotations
	j.rotations = nil
	return out
}
