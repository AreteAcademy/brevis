package extract

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// defaultWarnAfter is when an unconfigured WarnAfter starts warning. A week is
// long enough for somebody to notice on a Monday.
const defaultWarnAfter = 7 * 24 * time.Hour

// authenticate resolves the credential and writes it onto the header the
// requests will carry.
//
// It runs before the client is built, so that a secret applied as a cookie is
// seeded into the jar like any other -- and from there the jar is the single
// place a cookie lives, including one the refresh reissues.
func authenticate(ctx context.Context, source *core.Source) error {
	if source.Auth == nil {
		return nil
	}
	if err := source.Auth.Check(); err != nil {
		return err
	}

	secret, err := source.Auth.Get(ctx)
	if err != nil {
		return err
	}

	// The caller's header is theirs; they may reuse the map on another
	// pipeline, and it must not come back carrying a secret.
	h := http.Header(source.Header).Clone()
	if h == nil {
		h = http.Header{}
	}
	source.Auth.Apply(h, secret)
	source.Header = h

	return nil
}

// keep persists the rotated credential, and does not bring the run down when
// it cannot.
//
// The load is going to happen either way -- what is lost is the rotation, and
// the cost of that is somebody pasting the seed back in on the next window.
// Bringing a good extraction down over a write is trading a small problem for a
// large one.
//
// But it shouts: ERROR in the log AND in Stats, because a warning that exists
// only in the log is the silent death with extra steps.
func keep(ctx context.Context, store core.CredentialStore, value string, stats *core.Stats) {
	if store == nil || value == "" {
		return
	}
	if err := store.Save(value); err != nil {
		slog.ErrorContext(ctx, "credential store: the rotated credential was not saved",
			"store", store.Describe(),
			"effect", "the next run falls back to Credential.Value, which expires",
			"error", err)
		if stats != nil {
			stats.CredentialStoreError = err.Error()
		}
		return
	}
	slog.DebugContext(ctx, "credential store: rotated credential saved", "store", store.Describe())
}

// applyRotation rewrites the Cookie header with the reissued values, and says
// whether it rewrote anything.
//
// It rewrites BY NAME, preserving the cookies the refresh did not touch: a
// header with two cookies, one of which the API reissued, has to come out still
// carrying both.
func applyRotation(source *core.Source, rotations map[string]string) bool {
	if len(rotations) == 0 {
		return false
	}

	h := http.Header(source.Header).Clone()
	if h == nil {
		h = http.Header{}
	}
	current, err := http.ParseCookie(h.Get("Cookie"))
	if err != nil {
		// The header was built by the Applier and already went through
		// ParseCookie when the client was assembled; arriving here invalid
		// should not happen, and losing the rotation beats losing the whole
		// credential.
		return false
	}

	var parts []string
	for _, c := range current {
		if fresh, ok := rotations[c.Name]; ok {
			c.Value = fresh
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	h.Set("Cookie", strings.Join(parts, "; "))
	source.Header = h
	return true
}

// renewRequest makes the refresh call, with the same retries the pages get.
//
// Without them the walk is lopsided: a blip on the data endpoint costs a
// retry and a blip on the renewal costs the whole run. Same RetryConfig, same
// backoff, same reading of Retry-After.
func renewRequest(ctx context.Context, client *http.Client, source core.Source, method, rawURL string) ([]byte, error) {
	return request(ctx, client, source, method, rawURL, nil)
}

// request makes ONE request with the walk's guarantees: retry, backoff,
// Retry-After and secret redaction in the message.
//
// It serves both the refresh and the login. Both are "a request outside the
// page flow", and writing the second one again would have given it half the
// guarantees -- which is exactly what item 9 points out happens when the
// consumer writes it by hand.
func request(ctx context.Context, client *http.Client, source core.Source, method, rawURL string, body []byte) ([]byte, error) {
	label := "refresh"
	if body != nil || method == "POST" {
		label = "login"
	}
	fail := func(format string, a ...any) ([]byte, error) {
		return nil, fmt.Errorf(label+" "+redactURL(rawURL)+": "+format, a...)
	}

	attempts := 1
	if source.RetryConfig != nil && source.RetryConfig.MaxAttempts > 0 {
		attempts = source.RetryConfig.MaxAttempts
	}

	for attempt := 0; attempt < attempts; attempt++ {
		var reader io.Reader
		if body != nil {
			// A FRESH reader per attempt: the previous one was consumed, and a
			// retry with an exhausted body sends an empty request -- which the
			// API refuses with a message about the body, and not about the
			// retry.
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
		if err != nil {
			return fail("%w", err)
		}
		// The header goes WHOLE, credential included. This is exactly what §9
		// was missing: the refresh depended on the jar, the jar matched by
		// path, and /api/auth/session did not match a source on /api/proxy.
		req.Header = http.Header(source.Header).Clone()

		resp, err := client.Do(req)
		if err != nil {
			if shouldRetry(err) && attempt < attempts-1 {
				time.Sleep(calculateBackoff(attempt, source.RetryConfig))
				continue
			}
			return fail("after %d attempt(s): %w", attempt+1, err)
		}

		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			if readErr != nil {
				return fail("read response: %w", readErr)
			}
			return payload, nil
		}

		if shouldRetryStatus(resp.StatusCode) && attempt < attempts-1 {
			time.Sleep(retryAfter(resp, attempt, source.RetryConfig))
			continue
		}

		// A refresh that fails is not a warning to move past: every page
		// after it would go out with a credential the API just refused, and
		// the run would fail anyway -- later, and blaming the data endpoint.
		return fail("http %d: %s", resp.StatusCode, string(payload))
	}

	return fail("out of attempts")
}

// renew calls the refresh endpoint before the first page.
//
// It shares the walk's client, so a Set-Cookie in the response lands in the
// jar and applies to every page that follows -- which is the whole mechanism.
// Nothing is written anywhere: the reissued value lives for this run only.
func renew(ctx context.Context, client *http.Client, source *core.Source, jar *credentialJar, stats *core.Stats) error {
	r := source.Auth.Refresh

	method := r.Method
	if method == "" {
		method = "GET"
	}

	body, err := renewRequest(ctx, client, *source, method, r.URL)
	if err != nil {
		return err
	}

	// What the refresh reissued starts applying to the pages, and it applies
	// NOW: without this the refresh refreshes for nobody, because the new value
	// would stay in the jar alone, bound to the refresh URL's directory.
	//
	// Persisting is a different thing, and it comes after. See persist(), below.
	rotated := applyRotation(source, jar.Rotations())

	persist := func() {
		if rotated {
			keep(ctx, r.Store, http.Header(source.Header).Get("Cookie"), stats)
		}
	}

	if r.ExpiresAt == nil {
		// With no validity signal there is nothing to check, and whoever
		// configured it gave that up -- with the warning Credential.Check
		// emits.
		persist()
		return nil
	}

	expires, err := r.ExpiresAt(body)
	if err != nil {
		// Does NOT write. NextAuth answers 200 with a `null` body and a
		// Set-Cookie emptying the values when the session did not
		// authenticate -- so what would reach the store is the credential of a
		// logged-out session.
		//
		// And since the read order is store-before-seed, writing that poisons:
		// next time the dead value wins, swapping the env var for a good
		// credential stops fixing anything, and the only way out becomes
		// deleting the object by hand. The symptom for whoever operates it is
		// a 401 with no explanation, on a pipeline that worked yesterday.
		//
		// The Python vendor this came from already knew the trap: seed_cookie
		// checked before writing, "so that a dead value does not land as the
		// newest row".
		return fmt.Errorf("refresh %s: %w", redactURL(r.URL), err)
	}
	if stats != nil {
		stats.CredentialExpiry = expires
	}
	persist()

	warnAfter := r.WarnAfter
	if warnAfter == 0 {
		warnAfter = defaultWarnAfter
	}
	left := time.Until(expires)

	switch {
	case left <= 0:
		slog.WarnContext(ctx, "credential has expired",
			"expires", expires.Format(time.RFC3339),
			"url", redactURL(r.URL))
	case left < warnAfter:
		slog.WarnContext(ctx, "credential expires soon",
			"expires", expires.Format(time.RFC3339),
			"left", core.RoundDuration(left),
			"url", redactURL(r.URL))
	default:
		slog.DebugContext(ctx, "credential renewed",
			"expires", expires.Format(time.RFC3339),
			"left", core.RoundDuration(left))
	}

	return nil
}

// prepareLogin builds the Secret that performs the login with the walk's
// client.
//
// It runs BEFORE Credential.Get, and the result comes under the TTL and the
// lock Get already has: a login cached for an hour and a login per run are
// different things, and some APIs rate-limit the FREQUENCY of authentication
// rather than that of requests.
//
// The client is the pages' client, and that is the point of the item: retry,
// backoff, Retry-After and secret redaction in the log start applying to the
// request that carries the credentials -- which, written by hand, usually comes
// out on http.DefaultClient with no timeout at all.
func prepareLogin(client *http.Client, source core.Source) {
	l := source.Auth.Login
	if l == nil {
		return
	}

	source.Auth.PrepareLogin(func(ctx context.Context) (string, error) {
		method := l.Method
		if method == "" {
			// A login is a POST. GET would put the secret in the query string,
			// and the query string reaches server and proxy logs.
			method = "POST"
		}

		var contentType string
		var body []byte
		if l.Body != nil {
			var err error
			contentType, body, err = l.Body(ctx)
			if err != nil {
				return "", fmt.Errorf("login %s: building the body: %w", redactURL(l.URL), err)
			}
		}

		src := source
		header := http.Header(l.Header).Clone()
		// The credential does not exist yet: it is what is being obtained.
		// Sending the source's header here would leak whatever is in it to the
		// login endpoint, which may live on another host.
		if header == nil {
			header = http.Header{}
		}
		if contentType != "" {
			header.Set("Content-Type", contentType)
		}
		src.Header = header

		response, err := request(ctx, client, src, method, l.URL, body)
		if err != nil {
			return "", err
		}

		token, err := l.Token(response)
		if err != nil {
			return "", fmt.Errorf("login %s: %w", redactURL(l.URL), err)
		}
		if token == "" {
			return "", fmt.Errorf("login %s: the token came back empty. An empty token "+
				"becomes an empty authorization header and a 401 further down, blaming "+
				"the API", redactURL(l.URL))
		}
		return token, nil
	})
}
