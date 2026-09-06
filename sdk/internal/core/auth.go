package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Secret produces the value an HTTP source authenticates with.
//
// It is a function rather than a string so that a credential which has to be
// fetched -- a login call, a secret manager -- is expressible without the SDK
// knowing how. FromEnv covers the common case.
type Secret func(ctx context.Context) (string, error)

// Applier puts the secret on the outgoing request.
type Applier func(h http.Header, secret string)

// Credential is how an HTTP source authenticates, and what the SDK does to
// keep the credential alive.
//
// Nothing here is required: a source with a static key in Header needs none
// of it. What it buys is the two things every consumer was writing by hand --
// caching a login so the API is not asked for a token on every page, and
// renewing a session that would otherwise expire in silence.
//
// It holds a cache, so it is used through a pointer.
type Credential struct {
	// Value produces the secret. Required.
	Value Secret

	// Apply puts it on the request. Required. See AsBearer, AsCookie,
	// AsCookieNamed and AsHeader.
	Apply Applier

	// TTL caches the value in memory for this long, so a Value that logs in
	// is not called once per pipeline run when several run in one process.
	// Zero calls Value once per run, which is what a plain env var wants.
	//
	// The cache never reaches disk and never outlives the process. Some APIs
	// rate-limit authentication attempts rather than requests, which is the
	// case this exists for.
	TTL time.Duration

	// Login trades secrets for a token that comes in the response BODY, which
	// is the shape most APIs use. Nil skips it.
	//
	// It exists because the workaround -- putting the login inside Value, which
	// is a func -- works and carries a hidden cost: the login request becomes
	// the ONLY one in the fetcher with no retry, no rate limit, no per-attempt
	// timeout and no secret redaction in the log. And it is the one carrying
	// the credentials.
	//
	// Value still exists and still covers what does not fit here -- a secret
	// manager, a file, an environment variable. Login and Value together is an
	// error: two sources for the same secret, and the one that loses loses in
	// silence.
	Login *Login

	// Refresh optionally calls an endpoint before the first page to renew a
	// session. Nil skips it.
	Refresh *Refresh

	mu       sync.Mutex
	cached   string
	cachedAt time.Time

	// login is the Secret extract builds out of Login, with its own client. It
	// lives here, and not in Login, because Get is what calls it -- and Get is
	// what holds the lock and the TTL.
	login Secret
}

// PrepareLogin installs the Secret that performs the login. Called by extract,
// which is what holds the HTTP client.
func (c *Credential) PrepareLogin(s Secret) { c.login = s }

// Login trades secrets for a token, using the SDK's client.
//
//	Auth: &from.Credential{
//	    Login: &from.Login{
//	        URL:    "https://api.example.com/oauth/token",
//	        Method: "POST",
//	        Body:   from.JSONBody(map[string]any{"client_id": id, "client_secret": secret}),
//	        Token:  from.JSONToken("data.accessToken"),
//	    },
//	    Apply: from.AsBearer,
//	    TTL:   50 * time.Minute,
//	}
//
// What it buys is not convenience: it is the fetcher's most sensitive request
// no longer being the only one with no retry, no rate limit, no timeout and no
// redaction in the log. Written by hand, it usually comes out on
// http.DefaultClient -- which has no timeout at all.
//
// Pair it with TTL: without one the login happens once per run, and some APIs
// rate-limit the FREQUENCY of authentication rather than that of requests.
type Login struct {
	// URL of the login endpoint. Required.
	URL string

	// Method is the verb. Empty uses POST -- which is what a login is.
	Method string

	// Body builds the request body. Nil sends none.
	//
	// It is a func and not bytes because the body carries a secret: it is
	// built at request time and does not sit alive in a struct field that any
	// configuration dump would print.
	Body func(ctx context.Context) (contentType string, body []byte, err error)

	// Header are the login's own headers -- an API key authorizing the trade,
	// for instance.
	Header map[string][]string

	// Token reads the token out of the response BODY. Required: if the token
	// arrived in a cookie, the path would be Refresh.
	Token func(body []byte) (string, error)
}

// Refresh renews a credential that expires, by asking the API to reissue it.
//
// It exists for a session token a human pasted in: the vendor has no
// programmatic login, the token has a sliding expiry, and only the renewal
// endpoint pushes the window forward. Without the call the pipeline dies on
// the day the window closes, with a 401 that says nothing about why.
//
// The reissued cookie is picked up by the same cookie jar the pages use, so
// it applies to this run. It is never written anywhere: a rotated token does
// not invalidate the previous one, so the cost of not storing it is that
// somebody re-pastes the credential once per expiry window -- and ExpiresAt
// plus WarnAfter is how they find out before it is too late.
type Refresh struct {
	// URL of the renewal endpoint. Required.
	URL string

	// Method defaults to GET.
	Method string

	// ExpiresAt reads the new expiry out of the response body. Optional; see
	// JSONField. Without it the SDK renews but cannot say for how long, and
	// WarnAfter has nothing to compare against.
	ExpiresAt func(body []byte) (time.Time, error)

	// Store keeps the rotated credential between runs. Nil keeps today's
	// behaviour: the renewed value lives for this run only.
	//
	// It exists because the alternative is a person re-pasting the credential
	// once per expiry window, forever. With it, the environment variable stops
	// holding the ROTATING value and starts holding a STATIC key -- pasted
	// once, never again. That asymmetry is the whole point; it is not "an
	// environment variable versus a file".
	//
	//	Store: from.FileStore{Name: "gabriel-session"}
	//
	// The read order is store, then Value as the seed, then renew, then save.
	//
	// Last writer wins. Two processes renewing at once write two values, and
	// both work only because rotating does not invalidate the previous token
	// at the vendor this was built for. For a vendor that DOES invalidate the
	// previous one, do not use this without a lock of your own.
	Store CredentialStore

	// WarnAfter warns once the credential has less than this left. Requires
	// ExpiresAt. Zero warns at 7 days.
	//
	// The warning is both a log line and Stats.CredentialExpiry, because a
	// warning nobody reads is how the silent death happens in the first
	// place.
	WarnAfter time.Duration
}

// Get returns the secret, from the cache when TTL still covers it.
//
// The lock serializes concurrent callers so an API that rate-limits logins
// sees one attempt, not one per goroutine.
func (c *Credential) Get(ctx context.Context) (string, error) {
	if c.Value == nil && c.Login == nil {
		return "", fmt.Errorf("Credential needs either Value or Login")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.TTL > 0 && c.cached != "" && time.Since(c.cachedAt) < c.TTL {
		return c.cached, nil
	}

	// The store comes before the seed. A stored value is the result of the
	// last rotation; the seed is what somebody pasted in once, and it may have
	// expired already.
	if c.Refresh != nil && c.Refresh.Store != nil {
		stored, err := c.Refresh.Store.Load()
		if err != nil {
			// The read genuinely failed -- permissions, disk. That is no
			// reason to stop: the seed may still work, and stopping here would
			// trade a possibly stale credential for none at all.
			slog.WarnContext(ctx, "credential store: could not be read",
				"store", c.Refresh.Store.Describe(),
				"falling_back_to", "Credential.Value",
				"error", err)
		} else if stored != "" {
			c.cached, c.cachedAt = stored, time.Now()
			return stored, nil
		}
	}

	produce := c.Value
	if produce == nil {
		// The Login is performed by whoever holds the HTTP client -- extract
		// -- and arrives here as a Secret already closed over it. See
		// extract.prepareLogin.
		produce = c.login
	}
	if produce == nil {
		return "", fmt.Errorf("credential: Login is declared but was never prepared; this " +
			"is a defect in the SDK, not in your configuration")
	}

	v, err := produce(ctx)
	if err != nil {
		return "", fmt.Errorf("credential: %w", err)
	}
	if v == "" {
		return "", fmt.Errorf("credential: Value returned an empty secret")
	}

	c.cached, c.cachedAt = v, time.Now()
	return v, nil
}

// Check reports a Credential that cannot work, at setup rather than as a 401.
func (c *Credential) Check() error {
	if c == nil {
		return nil
	}
	if c.Value == nil && c.Login == nil {
		return fmt.Errorf("Auth.Value and Auth.Login are both nil, and one of the two has " +
			"to exist: Value produces the secret, Login trades for a token. For an " +
			"environment variable, from.FromEnv(\"NAME\")")
	}
	if c.Value != nil && c.Login != nil {
		return fmt.Errorf("Auth.Value and Auth.Login are both set, and both produce the " +
			"same secret -- whichever lost would lose in silence. Login makes the request " +
			"with the SDK's client; Value is for what is not an HTTP request")
	}
	if c.Login != nil {
		if c.Login.URL == "" {
			return fmt.Errorf("Auth.Login.URL is empty: it is the endpoint that trades the " +
				"secrets for the token")
		}
		if c.Login.Token == nil {
			return fmt.Errorf("Auth.Login.Token is nil: it is what says WHERE the token is " +
				"in the response body. Use from.JSONToken(\"data.accessToken\")")
		}
	}
	if c.Apply == nil {
		return fmt.Errorf("Auth.Apply is nil: it is what puts the secret on the " +
			"request. Use from.AsBearer, from.AsCookie or from.AsHeader(name)")
	}
	if c.Refresh == nil {
		return nil
	}
	if c.Refresh.URL == "" {
		return fmt.Errorf("Auth.Refresh.URL is empty: it is the endpoint that reissues " +
			"the credential")
	}
	// The store's refusal happens here, at assembly time: finding out that the
	// credential would not have been stored after the whole load is too late.
	if v, ok := c.Refresh.Store.(CredentialStoreChecker); ok {
		if err := v.CheckStore(); err != nil {
			return err
		}
	}
	if c.Refresh.Store != nil && c.Refresh.ExpiresAt == nil {
		// A warning and not a refusal: there are sources whose refresh returns
		// no validity at all, and for those the store is still worth having.
		// But whoever chooses that has to choose it knowingly.
		slog.Warn("credential store without ExpiresAt: a refresh that did not authenticate will be saved",
			"store", c.Refresh.Store.Describe(),
			"why", "the status is 200 either way, so the body is the only place the difference shows",
			"risk", "a dead credential is read before Value on the next run, and swapping the "+
				"environment variable stops fixing it",
			"fix", "set Refresh.ExpiresAt, for example from.JSONField(\"expires\")")
	}
	if c.Refresh.WarnAfter != 0 && c.Refresh.ExpiresAt == nil {
		return fmt.Errorf("Auth.Refresh.WarnAfter says when to warn and Auth.Refresh." +
			"ExpiresAt says what to compare it against, and ExpiresAt is not set -- " +
			"so the warning could never fire. Use from.JSONField(\"expires\")")
	}
	return nil
}

// FromEnv reads the secret from an environment variable.
//
// An unset or empty variable is an error, named, at setup: the alternative is
// an empty Authorization header and a 401 that blames the API.
func FromEnv(name string) Secret {
	return func(context.Context) (string, error) {
		v := os.Getenv(name)
		if v == "" {
			return "", fmt.Errorf("environment variable %s is unset or empty", name)
		}
		return v, nil
	}
}

// AsBearer sends the secret as "Authorization: Bearer <secret>".
func AsBearer(h http.Header, secret string) {
	h.Set("Authorization", "Bearer "+secret)
}

// AsCookie sends the secret as the whole Cookie header.
//
// The secret is the full "name=value", which is what someone copying a
// session cookie out of a browser has in hand. Use AsCookieNamed when only
// the value is stored.
func AsCookie(h http.Header, secret string) {
	h.Set("Cookie", secret)
}

// AsCookieNamed sends the secret as the value of one named cookie.
func AsCookieNamed(name string) Applier {
	return func(h http.Header, secret string) {
		h.Set("Cookie", name+"="+secret)
	}
}

// AsHeader sends the secret as the whole value of a header of your choosing,
// for the APIs that want X-API-Key or similar.
func AsHeader(name string) Applier {
	return func(h http.Header, secret string) {
		h.Set(name, secret)
	}
}

// JSONField reads an RFC 3339 timestamp from a top-level field of a JSON
// response body -- {"expires": "2026-10-04T22:15:07.197Z"} is JSONField("expires").
func JSONField(name string) func([]byte) (time.Time, error) {
	return func(body []byte) (time.Time, error) {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			return time.Time{}, fmt.Errorf("refresh response is not a JSON object: %w", err)
		}
		raw, ok := doc[name]
		if !ok {
			return time.Time{}, fmt.Errorf("refresh response has no field %q", name)
		}
		s, ok := raw.(string)
		if !ok {
			return time.Time{}, fmt.Errorf("field %q is %T, want an RFC 3339 string", name, raw)
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, fmt.Errorf("field %q = %q is not RFC 3339: %w", name, s, err)
		}
		return t, nil
	}
}

// JSONToken reads the token from a field of the response body, by a
// dot-separated path.
//
//	Token: from.JSONToken("data.accessToken")
//
// The path takes dots because the wide convention puts the token inside an
// envelope, and not at the root.
//
// An absent field is an ERROR naming the path -- and not an empty string. An
// empty token becomes an empty authorization header and a 401 further down,
// blaming the API for a path this side wrote wrong.
func JSONToken(path string) func([]byte) (string, error) {
	return func(body []byte) (string, error) {
		var current any
		if err := json.Unmarshal(body, &current); err != nil {
			return "", fmt.Errorf("the login response is not JSON: %w", err)
		}

		parts := strings.Split(path, ".")
		for i, part := range parts {
			obj, ok := current.(map[string]any)
			if !ok {
				return "", fmt.Errorf("%q: %q is not an object",
					path, strings.Join(parts[:i], "."))
			}
			v, found := obj[part]
			if !found {
				return "", fmt.Errorf("%q: the response has no %q. Check the path -- an "+
					"absent token would become an empty header and a 401 further down, "+
					"blaming the API", path, part)
			}
			current = v
		}

		switch t := current.(type) {
		case string:
			return t, nil
		case json.Number:
			return t.String(), nil
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64), nil
		default:
			return "", fmt.Errorf("%q led to a %T, and a token has to be text", path, current)
		}
	}
}

// JSONBody builds a JSON body for the Login.
//
//	Body: from.JSONBody(map[string]any{"client_id": id, "client_secret": secret})
//
// The serialization happens at request time, and not here: the body carries a
// secret, and a []byte kept in a struct field shows up in any configuration
// dump.
func JSONBody(v any) func(context.Context) (string, []byte, error) {
	return func(context.Context) (string, []byte, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", nil, fmt.Errorf("building the login body: %w", err)
		}
		return "application/json", b, nil
	}
}

// FormBody builds an application/x-www-form-urlencoded body, which is the
// format OAuth2 uses.
func FormBody(fields map[string]string) func(context.Context) (string, []byte, error) {
	return func(context.Context) (string, []byte, error) {
		v := url.Values{}
		for k, value := range fields {
			v.Set(k, value)
		}
		return "application/x-www-form-urlencoded", []byte(v.Encode()), nil
	}
}
