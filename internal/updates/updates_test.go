package updates

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func checker(current string, rels []listed, err error) *Checker {
	c := New(current, slog.New(slog.DiscardHandler))
	c.fetch = func(context.Context) ([]listed, error) { return rels, err }
	return c
}

// THE NEWEST ENGINE RELEASE WINS, and only an engine release: the SDK's and
// the gateway's tags, a draft and a pre-release are not news of this program.
func TestOnlyTheNewestStableEngineReleaseCounts(t *testing.T) {
	c := checker("0.40.0", []listed{
		{Tag: "sdk/v0.99.0", URL: "sdk"},
		{Tag: "gateway/v0.50.0", URL: "gw"},
		{Tag: "v0.42.0", URL: "draft", Draft: true},
		{Tag: "v0.43.0-rc.1", URL: "rc"},
		{Tag: "v0.44.0", URL: "pre", Prerelease: true},
		{Tag: "v0.41.0", URL: "u41"},
		{Tag: "v0.9.0", URL: "old"},
	}, nil)
	c.Check(context.Background())
	if r, newer, ok := c.Latest(); !ok || !newer || r.Tag != "v0.41.0" || r.URL != "u41" {
		t.Errorf("Latest = %+v %v %v; want v0.41.0 u41, newer", r, newer, ok)
	}
}

// Versions compare as numbers, not text: 0.10.0 is newer than 0.9.0.
func TestVersionsCompareAsNumbers(t *testing.T) {
	c := checker("v0.9.0", []listed{{Tag: "v0.10.0", URL: "u"}}, nil)
	c.Check(context.Background())
	if _, newer, _ := c.Latest(); !newer {
		t.Error("0.10.0 is not newer than 0.9.0")
	}
}

// NOTHING IS OFFERED when the console is current or ahead.
func TestNoUpdateWhenCurrentOrAhead(t *testing.T) {
	for _, cur := range []string{"0.41.0", "0.42.0"} {
		c := checker(cur, []listed{{Tag: "v0.41.0", URL: "u"}}, nil)
		c.Check(context.Background())
		if _, newer, _ := c.Latest(); newer {
			t.Errorf("running %s: an update is offered", cur)
		}
	}
}

// A failed read keeps what was known, and a development build has no checker.
func TestAFailedCheckKeepsTheLastAnswerAndDevHasNoChecker(t *testing.T) {
	c := checker("0.40.0", []listed{{Tag: "v0.41.0", URL: "u"}}, nil)
	c.Check(context.Background())
	c.fetch = func(context.Context) ([]listed, error) { return nil, errors.New("offline") }
	c.Check(context.Background())
	if _, newer, _ := c.Latest(); !newer {
		t.Error("an offline check forgot the update")
	}
	if New("dev", slog.New(slog.DiscardHandler)) != nil {
		t.Error("a development build got a checker")
	}
}

// THE NOTES ARE DRAWN INSIDE THE CONSOLE, so only an allowlist gets through:
// no script, no event handler, no image, no javascript: link.
func TestSanitizeKeepsTheMarkupOfNotesAndNothingElse(t *testing.T) {
	in := `<h3>Added</h3><p onclick="x()">a <code>b</code> &lt;c&gt; <script>alert(1)</script><img src=x onerror=y>` +
		`<a href="javascript:alert(1)">bad</a> <a href="https://x.dev/a?b=1&amp;c=2" onclick="z()">ok</a> 1 < 2</p>`
	got := Sanitize(in)
	for _, bad := range []string{"onclick", "onerror", "<script", "<img", "javascript:"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q survived: %s", bad, got)
		}
	}
	for _, want := range []string{"<h3>Added</h3>", "<code>b</code>", "&lt;c&gt;", `href="https://x.dev/a?b=1&amp;c=2"`, `rel="noopener noreferrer"`, "1 &lt; 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is gone: %s", want, got)
		}
	}
}
