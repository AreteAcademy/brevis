package from

import (
	"fmt"
	"net/url"
	"sort"

	"github.com/AreteAcademy/brevis/sdk/extract"
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

var (
	_ core.Configured = HTTP{}
	_ core.Configured = Files{}
	_ core.Configured = Many{}
)

// Configuration satisfies sdk.Configured: where the source asks, how, and how
// it pages -- with every value that could carry a credential left out.
//
// The URL is its scheme, host and path through core.SafeURL: its user and
// password, its query string and its fragment are never shown, and a host
// label or path segment is shown only when it is plainly safe; the query's
// parameter NAMES and the
// headers' names are, because "which key does this send" is a question worth
// answering and "what is the key" is the one this must never answer. Stricter
// than the log's redaction (sdk/redact.go), which hides the values of the
// names it knows and shows the rest: a page is read by more people than a log.
func (h HTTP) Configuration() []core.Setting {
	where := core.SafeURL(h.URL)
	var params []string
	if u, err := url.Parse(h.URL); err == nil && where != "" {
		for name := range u.Query() {
			params = append(params, name)
		}
		sort.Strings(params)
	}
	headers := make([]string, 0, len(h.Header))
	for name := range h.Header {
		headers = append(headers, name)
	}
	sort.Strings(headers)

	method := h.Method
	if method == "" {
		method = extract.DefaultMethod
	}
	timeout, total := h.Timeout, h.TotalTimeout
	if timeout == 0 {
		timeout = extract.DefaultTimeout
	}
	if total == 0 {
		total = extract.DefaultTotalTimeout
	}

	settings := []core.Setting{
		{Key: "url", Value: where},
		{Key: "query_params", Value: params},
		{Key: "method", Value: method},
		{Key: "headers", Value: headers},
		{Key: "body", Value: core.SetByFunction(h.Body != nil)},
		{Key: "format", Value: string(h.Format)},
		{Key: "records", Value: core.SetByFunction(h.Records != nil)},
		{Key: "stream", Value: core.SetByFunction(h.Stream != nil)},
		{Key: "data_key", Value: h.DataKey},
		{Key: "follow_links", Value: h.FollowLinks},
		{Key: "cursor_key", Value: h.CursorKey},
		{Key: "offset_key", Value: h.OffsetKey},
		{Key: "more_key", Value: h.MoreKey},
		{Key: "page_key", Value: h.PageKey},
		{Key: "first_page", Value: h.FirstPage},
		{Key: "page_size", Value: h.PageSize},
		{Key: "max_pages", Value: h.MaxPages},
		{Key: "timeout", Value: core.DurationText(timeout)},
		{Key: "total_timeout", Value: core.DurationText(total)},
		{Key: "rate_limit", Value: core.SetByFunction(h.RateLimiter != nil)},
		{Key: "delimiter", Value: runeText(h.Delimiter)},
		{Key: "no_header", Value: h.NoHeader},
		{Key: "preserve_numbers", Value: h.PreserveNumbers},
	}
	if r := h.RetryConfig; r != nil {
		settings = append(settings,
			core.Setting{Key: "retry.max_attempts", Value: r.MaxAttempts},
			core.Setting{Key: "retry.initial_backoff", Value: core.DurationText(r.InitialBackoff)},
			core.Setting{Key: "retry.max_backoff", Value: core.DurationText(r.MaxBackoff)},
		)
	}
	settings = append(settings, authSettings(h.Auth)...)
	return core.Keep(settings...)
}

// authSettings says HOW a source authenticates, never with what: a login's
// endpoint is an address, its body and the secret it trades are not.
func authSettings(c *Credential) []core.Setting {
	if c == nil {
		return nil
	}
	s := []core.Setting{{Key: "auth.ttl", Value: core.DurationText(c.TTL)}}
	switch {
	case c.Login != nil:
		s = append(s, core.Setting{Key: "auth", Value: "login"},
			core.Setting{Key: "auth.login", Value: endpoint(c.Login.Method, c.Login.URL)})
	case c.Value != nil:
		s = append(s, core.Setting{Key: "auth", Value: "secret"})
	}
	if c.Refresh != nil {
		s = append(s, core.Setting{Key: "auth.refresh", Value: endpoint(c.Refresh.Method, c.Refresh.URL)})
	}
	return s
}

// endpoint is `POST https://host/path`, scrubbed as any URL here is.
func endpoint(method, raw string) string {
	where := core.SafeURL(raw)
	if where == "" {
		return ""
	}
	if method == "" {
		method = "POST"
	}
	return method + " " + where
}

// Configuration satisfies sdk.Configured: where the files are read from.
func (f Files) Configuration() []core.Setting {
	return core.Keep(
		core.Setting{Key: "path", Value: core.SafeURL(f.Path)},
		core.Setting{Key: "format", Value: string(f.Format)},
		core.Setting{Key: "delimiter", Value: runeText(f.Delimiter)},
		core.Setting{Key: "no_header", Value: f.NoHeader},
		core.Setting{Key: "preserve_numbers", Value: f.PreserveNumbers},
		core.Setting{Key: "store", Value: core.TypeName(f.Store)},
	)
}

// Configuration satisfies sdk.Configured: each source, under its position,
// described the way it describes itself.
//
// Sources that Discover finds exist only at run time, so they are named as
// discovered, not listed.
func (m Many) Configuration() []core.Setting {
	s := []core.Setting{
		{Key: "workers", Value: m.Workers},
		{Key: "on_error", Value: failureText(m.OnError)},
		{Key: "discover", Value: core.SetByFunction(m.Discover != nil)},
	}
	for i, src := range m.Sources {
		prefix := fmt.Sprintf("sources.%d.", i+1)
		s = append(s, core.Setting{Key: prefix + "driver", Value: core.TypeName(src)})
		if c, ok := src.(core.Configured); ok {
			for _, child := range c.Configuration() {
				s = append(s, core.Setting{Key: prefix + child.Key, Value: child.Value})
			}
		}
	}
	return core.Keep(s...)
}

// failureText is a policy as a setting: "" for the default, which stops on
// the first failure.
func failureText(p core.FailurePolicy) string {
	if p == core.AbortOnError {
		return ""
	}
	return p.String()
}

func runeText(r rune) string {
	if r == 0 {
		return ""
	}
	return string(r)
}
