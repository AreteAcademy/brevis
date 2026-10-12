package api_test

import (
	"strings"
	"testing"
)

// THE NAVIGATION IS AT THE TOP, and this asserts WHERE and not whether.
//
// A sidebar and a top bar contain the same links; `strings.Contains` cannot
// tell them apart, and seven tests passed this afternoon on a panel that was
// rendering outside the page. Position is the only kind of assertion that
// sees a layout.
func TestTheNavigationIsAboveTheContentAndNotBesideIt(t *testing.T) {
	body := render(t, dataUI(catalogFake{}), "/data")

	main := strings.Index(body, "<main")
	if main < 0 {
		t.Fatal("the layout no longer has a <main>; this test needs a new anchor")
	}
	nav := strings.Index(body, "<nav")
	if nav < 0 {
		t.Fatal("there is no <nav> at all")
	}
	if nav > main {
		t.Error("the navigation renders inside <main>, which is a sidebar's position")
	}
	// A sidebar is an <aside>. The shell must not have one.
	if i := strings.Index(body, "<aside"); i >= 0 && i < main {
		t.Error("the shell still draws an <aside> beside the content")
	}
}

// EVERY SECTION, AND THE OPEN ONE SAID OUT LOUD. `aria-current` is what tells
// somebody not looking at the underline which section they are in.
func TestTheBarCarriesEverySectionAndMarksTheOpenOne(t *testing.T) {
	body := render(t, dataUI(catalogFake{}), "/data")

	for _, want := range []string{
		`href="/"`, `href="/workflows"`, `href="/runs"`,
		`href="/projects"`, `href="/data"`,
		"Overview", "Workflows", "Runs", "Projects", "Data",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the bar does not carry %q", want)
		}
	}
	if n := strings.Count(body, `aria-current="page"`); n != 1 {
		t.Errorf("%d items claim to be the open section, and one is open", n)
	}
	// And it is the right one: `/data` was asked for.
	i := strings.Index(body, `aria-current="page"`)
	if i < 0 {
		return
	}
	around := body[max(0, i-220):i]
	if !strings.Contains(around, `href="/data"`) {
		t.Errorf("the marked section is not /data: %q", around)
	}
}

// THE ACCOUNT MENU MOVES WITH THE BAR. It lived at the bottom of the sidebar;
// a sidebar that no longer exists cannot hold it, and a console where nobody
// can sign out is a console nobody can leave.
func TestTheAccountMenuIsInTheBar(t *testing.T) {
	body := render(t, dataUI(catalogFake{}), "/data")

	main := strings.Index(body, "<main")
	// The MENU and not the sign-out form: signing out needs a session, and
	// this render has none -- "Sign out depends on there being something to
	// sign out of", which the menu's own comment says. What moved is the
	// menu.
	menu := strings.Index(body, "account-menu")
	if menu < 0 {
		t.Fatal("there is no account menu at all")
	}
	if menu > main {
		t.Error("the account menu is inside <main>, which is where the page's own content goes")
	}
}

// THE BAR FOLLOWS THE DATA'S PATH: what happened (Overview), how it is built
// (Pipelines: Workflows, Runs), where it lives and goes (Data: Catalog,
// Endpoints, SQL), and Projects. Groups are announced as groups, so a screen
// reader hears "Data, group" before "Endpoints" -- the label is not decoration.
func TestTheBarGroupsSectionsByTheDatasPath(t *testing.T) {
	body := render(t, dataUI(catalogFake{}).WithEndpoints(endpointsFake{}), "/data")
	for _, group := range []string{`role="group" aria-label="Pipelines"`, `role="group" aria-label="Data"`} {
		if !strings.Contains(body, group) {
			t.Errorf("the bar has no %s", group)
		}
	}
	order := []string{`href="/"`, `aria-label="Pipelines"`, `href="/workflows"`, `href="/runs"`,
		`aria-label="Data"`, `href="/data"`, `href="/data/endpoints"`, `href="/projects"`}
	last := -1
	for _, want := range order {
		i := strings.Index(body, want)
		if i < 0 {
			t.Fatalf("the bar lacks %s", want)
		}
		if i < last {
			t.Errorf("%s is out of order", want)
		}
		last = i
	}
}

// EACH PAGE MARKS ITS OWN ITEM, and only one: Endpoints is not Catalog
// although both live under Data.
func TestEachPageMarksItsOwnItem(t *testing.T) {
	ui := dataUI(catalogFake{}).WithEndpoints(endpointsFake{})
	for path, href := range map[string]string{
		"/data":           `href="/data"`,
		"/data/endpoints": `href="/data/endpoints"`,
	} {
		body := render(t, ui, path)
		if n := strings.Count(body, `aria-current="page"`); n != 1 {
			t.Errorf("%s: %d items are current", path, n)
			continue
		}
		i := strings.Index(body, `aria-current="page"`)
		tag := body[strings.LastIndex(body[:i], "<a"):i]
		if !strings.Contains(tag, href) {
			t.Errorf("%s marks %q", path, tag)
		}
	}
}
