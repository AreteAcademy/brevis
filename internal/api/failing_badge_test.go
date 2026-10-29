package api_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/api"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

type failingFake struct {
	n   int
	err error
}

func (f failingFake) FailureStreak(context.Context, string) (int, error) { return 0, nil }

func (f failingFake) FailingWorkflows(context.Context) ([]postgres.FailingWorkflow, error) {
	return make([]postgres.FailingWorkflow, f.n), f.err
}

// runsLink is the sidebar's Runs link, badge and all.
func runsLink(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `href="/runs"`)
	if i < 0 {
		t.Fatal("no Runs link in the sidebar")
	}
	return body[i : i+strings.Index(body[i:], "</a>")]
}

// THE RUNS SECTION SAYS HOW MANY WORKFLOWS ARE FAILING, so a broken one is
// seen from any page -- in the reader's language, with its plural, and read
// out as a sentence rather than a bare number.
func TestTheRunsSectionCountsTheWorkflowsFailing(t *testing.T) {
	for _, c := range []struct {
		lang string
		n    int
		say  string
	}{
		{"", 2, "2 workflows failing"},
		{"", 1, "1 workflow failing"},
		{"pt-BR", 3, "3 workflows falhando"},
		{"es", 1, "1 workflow fallando"},
	} {
		ui := dataUI(catalogFake{}).WithEndpoints(endpointsFake{}).WithFailing(failingFake{n: c.n})
		link := runsLink(t, pageIn(t, ui, "/ingest/loads", c.lang, "", false))
		if !strings.Contains(link, "nav-badge-fail") || !strings.Contains(link, c.say) {
			t.Errorf("%s with %d failing: the Runs link = %s; want a badge saying %q", c.lang, c.n, link, c.say)
		}
	}
}

// NOTHING FAILING, NO BADGE -- and none on a console with no reader at all.
func TestNoBadgeWhenNothingFailsOrNothingCanTell(t *testing.T) {
	for name, ui := range map[string]*api.UI{
		"zero":      dataUI(catalogFake{}).WithEndpoints(endpointsFake{}).WithFailing(failingFake{}),
		"no reader": dataUI(catalogFake{}).WithEndpoints(endpointsFake{}),
	} {
		if link := runsLink(t, pageIn(t, ui, "/ingest/loads", "", "", false)); strings.Contains(link, "nav-badge") {
			t.Errorf("%s: the Runs link has a badge: %s", name, link)
		}
	}
}

// A NUMBER FOR DECORATION CANNOT TAKE A PAGE DOWN: when the count cannot be
// read, the error goes to the log and the page is drawn without it.
func TestABadgeThatCannotBeReadIsLoggedAndLeftOut(t *testing.T) {
	var logged bytes.Buffer
	ui := api.NewUI(nil, nil, nil, nil, nil, catalogFake{}, branding.Default(), slog.New(slog.NewTextHandler(&logged, nil))).
		WithEndpoints(endpointsFake{}).WithFailing(failingFake{n: 2, err: errors.New("connection refused")})
	code, body := get(t, ui, "/ingest/loads")
	if code != 200 || strings.Contains(runsLink(t, body), "nav-badge") {
		t.Errorf("the page = %d with Runs link %s; want 200 and no badge", code, runsLink(t, body))
	}
	if !strings.Contains(logged.String(), "connection refused") {
		t.Errorf("the error was not logged: %q", logged.String())
	}
}

type countingFailing struct {
	reads *int
	err   error
}

func (c countingFailing) FailureStreak(context.Context, string) (int, error) { return 0, nil }

func (c countingFailing) FailingWorkflows(context.Context) ([]postgres.FailingWorkflow, error) {
	*c.reads++
	return make([]postgres.FailingWorkflow, *c.reads), c.err
}

// THE COUNT IS READ AT MOST ONCE PER TTL: it costs ~6 ms with a thousand
// workflows, and every page draws it. An answer is kept for the TTL; an
// error is not, so the next page tries again.
func TestTheFailingCountIsKeptForItsTTL(t *testing.T) {
	reads, now := 0, time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	cached := api.CachedFailing(countingFailing{reads: &reads}, 15*time.Second, func() time.Time { return now })
	ctx := context.Background()

	a, _ := cached.FailingWorkflows(ctx)
	now = now.Add(14 * time.Second)
	b, _ := cached.FailingWorkflows(ctx)
	if reads != 1 || len(a) != 1 || len(b) != 1 {
		t.Errorf("within the TTL: %d reads, answers %d and %d; want 1 read", reads, len(a), len(b))
	}
	now = now.Add(2 * time.Second)
	if c, _ := cached.FailingWorkflows(ctx); reads != 2 || len(c) != 2 {
		t.Errorf("after the TTL: %d reads, answer %d; want a second read", reads, len(c))
	}

	failing := 0
	broken := api.CachedFailing(countingFailing{reads: &failing, err: errors.New("down")}, 15*time.Second, func() time.Time { return now })
	_, _ = broken.FailingWorkflows(ctx)
	if _, err := broken.FailingWorkflows(ctx); err == nil || failing != 2 {
		t.Errorf("an error was kept: %d reads, err %v", failing, err)
	}
}
