package layouts

import (
	"context"
	"strings"

	"github.com/a-h/templ"
)

type versionKey struct{}

type releaseKey struct{}

type release struct {
	tag, url, notes string
	newer           bool
}

// WithVersion carries the running Brevis version, for the account menu.
func WithVersion(ctx context.Context, v string) context.Context {
	return context.WithValue(ctx, versionKey{}, v)
}

// WithRelease carries the newest release: its tag, its page, its notes (safe
// HTML) and whether it is newer than the one running.
func WithRelease(ctx context.Context, tag, url, notes string, newer bool) context.Context {
	return context.WithValue(ctx, releaseKey{}, release{tag, url, notes, newer})
}

// versionLabel is "v0.40.0" however the build stamped it ("0.40.0", "v0.40.0"),
// and "dev" for a development build.
func versionLabel(ctx context.Context) string {
	v, _ := ctx.Value(versionKey{}).(string)
	if v == "" {
		return ""
	}
	if v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return strings.TrimSpace(v)
}

func latestRelease(ctx context.Context) (release, bool) {
	r, ok := ctx.Value(releaseKey{}).(release)
	return r, ok
}

// newerRelease is the release to tell about, when it is newer.
func newerRelease(ctx context.Context) (release, bool) {
	r, ok := latestRelease(ctx)
	return r, ok && r.newer
}

// notesHTML is the release notes the updates package already sanitised.
func notesHTML(s string) templ.Component { return templ.Raw(s) }
