package api_test

import (
	"strings"
	"testing"
)

// THE NAVIGATION IS A SIDEBAR, and this asserts WHERE and not whether.
//
// It was a top bar, and this test pinned that: the sections were few and
// one level deep, and a column of them spent width on screens whose job is a
// wide table. The owner reversed it on 2026-10-14 with the console redesign
// (G1): three groups now -- Pipelines, Data, Workspace -- each with a label
// and an icon, which a single row cannot carry without crowding, and a fixed
// frame that holds still while the page beside it changes. The width is paid
// knowingly; below 960px the sidebar becomes a drawer.
//
// A sidebar and a top bar contain the same links; `strings.Contains` cannot
// tell them apart, and seven tests once passed on a panel that rendered
// outside the page. Position is the only kind of assertion that sees a
// layout.
func TestTheNavigationIsASidebarBesideTheContent(t *testing.T) {
	body := render(t, dataUI(catalogFake{}), "/ingest/loads")

	aside, end := strings.Index(body, "<aside"), strings.Index(body, "</aside>")
	main := strings.Index(body, "<main")
	if aside < 0 || end < 0 || main < 0 {
		t.Fatalf("the shell has no <aside> and <main> to place (aside %d, main %d)", aside, main)
	}
	nav := strings.Index(body, `<nav aria-label="Sections"`)
	if nav < 0 {
		t.Fatal("there is no section navigation at all")
	}
	if nav < aside || nav > end {
		t.Error("the section navigation is not inside the sidebar")
	}
	if main < end {
		t.Error("<main> opens inside the sidebar, which is the page drawn inside its own frame")
	}
}

// THE SIDEBAR HOLDS STILL WHILE THE PAGE CHANGES. Every navigation crossfades
// (transitions_test.go); a named element is captured on its own and, being
// identical on both pages, does not visibly fade -- which is the point of a
// fixed frame. Named in the SERVED sheet, because a class with no rule behind
// it is a layout that silently does nothing.
func TestTheSidebarHoldsStillAcrossANavigation(t *testing.T) {
	_, ui := browsing(t)
	_, body := get(t, ui, "/sql")
	if !strings.Contains(body, `class="app-sidebar`) {
		t.Error("the sidebar does not carry the class the transition is named on")
	}
	_, css := get(t, ui, "/assets/app.css")
	if !strings.Contains(css, "view-transition-name:sidebar") && !strings.Contains(css, "view-transition-name: sidebar") {
		t.Error("the served sheet does not name the sidebar's transition, so it fades with the page")
	}
}

// EVERY SECTION, AND THE OPEN ONE SAID OUT LOUD. `aria-current` is what tells
// somebody not looking at the highlight which section they are in.
func TestTheSidebarCarriesEverySectionAndMarksTheOpenOne(t *testing.T) {
	body := render(t, dataUI(catalogFake{}), "/ingest/loads")

	for _, want := range []string{
		`href="/"`, `href="/workflows"`, `href="/runs"`,
		`href="/projects"`, `href="/ingest/streams"`, `href="/ingest/loads"`,
		"Overview", "Workflows", "Runs", "Projects", "Streams", "Loads",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the sidebar does not carry %q", want)
		}
	}
	if n := strings.Count(body, `aria-current="page"`); n != 1 {
		t.Errorf("%d items claim to be the open section, and one is open", n)
	}
	i := strings.Index(body, `aria-current="page"`)
	if i < 0 {
		return
	}
	tag := body[strings.LastIndex(body[:i], "<a"):i]
	if !strings.Contains(tag, `href="/ingest/loads"`) {
		t.Errorf("the marked section is not /ingest/loads: %q", tag)
	}
}

// THE ACCOUNT MENU IS AT THE FOOT OF THE SIDEBAR, where a console's "who am
// I and how do I leave" lives. A console nobody can sign out of is one nobody
// can leave.
func TestTheAccountMenuIsInTheSidebar(t *testing.T) {
	body := render(t, dataUI(catalogFake{}), "/ingest/loads")

	aside, end := strings.Index(body, "<aside"), strings.Index(body, "</aside>")
	// The MENU and not the sign-out form: signing out needs a session, and
	// this render has none.
	menu := strings.Index(body, "account-menu")
	if menu < 0 {
		t.Fatal("there is no account menu at all")
	}
	if menu < aside || menu > end {
		t.Error("the account menu is not in the sidebar")
	}
	if nav := strings.Index(body, `<nav aria-label="Sections"`); menu < nav {
		t.Error("the account menu sits above the sections, where the eye looks for where to go")
	}
}

// THE SIDEBAR FOLLOWS THE DATA'S PATH: how it is built and what happened
// (Pipelines: Overview, Workflows, Runs), what comes in (Ingest: Streams,
// Loads), what reads it (Consume: Endpoints, SQL), and the Workspace. Groups
// are announced as groups, so a screen reader hears "Ingest, group" before
// "Streams".
func TestTheSidebarGroupsSectionsByTheDatasPath(t *testing.T) {
	body := render(t, dataUI(catalogFake{}).WithEndpoints(endpointsFake{}), "/ingest/loads")
	for _, group := range []string{
		`role="group" aria-label="Pipelines"`, `role="group" aria-label="Ingest"`,
		`role="group" aria-label="Consume"`, `role="group" aria-label="Workspace"`,
	} {
		if !strings.Contains(body, group) {
			t.Errorf("the sidebar has no %s", group)
		}
	}
	// Inside the section navigation only: the logo also links to `/`, above it.
	if at := strings.Index(body, `<nav aria-label="Sections"`); at >= 0 {
		body = body[at:]
	}
	order := []string{`aria-label="Pipelines"`, `href="/"`, `href="/workflows"`, `href="/runs"`,
		`aria-label="Ingest"`, `href="/ingest/streams"`, `href="/ingest/loads"`,
		`aria-label="Consume"`, `href="/data/endpoints"`,
		`aria-label="Workspace"`, `href="/projects"`}
	last := -1
	for _, want := range order {
		i := strings.Index(body, want)
		if i < 0 {
			t.Fatalf("the sidebar lacks %s", want)
		}
		if i < last {
			t.Errorf("%s is out of order", want)
		}
		last = i
	}
}

// EACH PAGE MARKS ITS OWN ITEM, and only one: Streams is not Loads although
// both live under Ingest.
func TestEachPageMarksItsOwnItem(t *testing.T) {
	ui := dataUI(catalogFake{}).WithEndpoints(endpointsFake{})
	for path, href := range map[string]string{
		"/ingest/loads":   `href="/ingest/loads"`,
		"/ingest/streams": `href="/ingest/streams"`,
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

// THE TOP BAR SAYS WHERE YOU ARE: the section's group, then the section, read
// from the same list the sidebar is drawn from -- two lists would disagree the
// first time a section moved. It is not a second `aria-current`: the sidebar
// already says which section is open, and saying it twice makes a screen
// reader count two.
func TestTheTopBarSaysWhereYouAre(t *testing.T) {
	ui := dataUI(catalogFake{}).WithEndpoints(endpointsFake{})
	for path, want := range map[string][2]string{
		"/ingest/loads":   {"Ingest", "Loads"},
		"/ingest/streams": {"Ingest", "Streams"},
		"/data/endpoints": {"Consume", "Endpoints"},
	} {
		body := render(t, ui, path)
		at := strings.Index(body, `aria-label="Breadcrumb"`)
		if at < 0 {
			t.Errorf("%s: the top bar has no breadcrumb", path)
			continue
		}
		crumb := body[at:]
		crumb = crumb[:strings.Index(crumb, "</nav>")]
		g, s := strings.Index(crumb, ">"+want[0]+"<"), strings.Index(crumb, ">"+want[1]+"<")
		if g < 0 || s < 0 || s < g {
			t.Errorf("%s: the breadcrumb is not %s / %s: %q", path, want[0], want[1], crumb)
		}
		if strings.Contains(crumb, "aria-current") {
			t.Errorf("%s: the breadcrumb claims the current page a second time", path)
		}
	}
}

// BELOW 960PX THE SIDEBAR IS A DRAWER, AND IT OPENS WITHOUT A SCRIPT.
//
// 236px of a 390px phone is most of the screen, so on a narrow one the
// sidebar leaves the flow and a button in the top bar brings it in. The
// button is a LINK to the sidebar's id: `:target` opens it with no script
// at all, and a close link inside clears the hash. With ui.js loaded the
// same controls stop touching the URL, close on Escape and give the focus
// back to the button -- an enhancement, not the mechanism.
func TestBelowTheBreakpointTheSidebarIsADrawer(t *testing.T) {
	body := render(t, dataUI(catalogFake{}), "/ingest/loads")

	if !strings.Contains(body, `<aside id="app-sidebar"`) {
		t.Error("the sidebar has no id for the drawer's controls to name")
	}
	open := strings.Index(body, `data-drawer-open`)
	if open < 0 {
		t.Fatal("the top bar has no control that opens the drawer")
	}
	tag := body[strings.LastIndex(body[:open], "<a"):]
	tag = tag[:strings.Index(tag, ">")]
	for _, want := range []string{`href="#app-sidebar"`, `aria-controls="app-sidebar"`, `aria-expanded="false"`, `aria-label="Open navigation"`} {
		if !strings.Contains(tag, want) {
			t.Errorf("the drawer's opener lacks %s: %s", want, tag)
		}
	}
	aside, end := strings.Index(body, "<aside"), strings.Index(body, "</aside>")
	if c := strings.Index(body, `data-drawer-close`); c < aside || c > end {
		t.Error("the drawer has no way to close from inside it")
	}

	_, ui := browsing(t)
	_, css := get(t, ui, "/assets/app.css")
	if !strings.Contains(css, ".app-sidebar:target") {
		t.Error("the served sheet does not open the drawer on :target, so without a script it never opens")
	}
	_, raw := get(t, ui, "/assets/ui.js")
	js := strip(raw)
	for _, want := range []string{`[data-drawer-open]`, `[data-drawer-close]`, `"Escape"`, `setAttribute("aria-expanded"`, `.focus()`} {
		if !strings.Contains(js, want) {
			t.Errorf("ui.js never uses %s, so that part of the drawer is inert", want)
		}
	}
}

// THE COMMAND PALETTE IS ON EVERY SIGNED-IN PAGE, CLOSED, AND ITS OPENER WAITS
// FOR THE SCRIPT.
//
// A native <dialog>, so focus containment, Escape and the backdrop come with
// the element. The input is a combobox over a listbox and says which option
// is active (aria-activedescendant, set by ui.js), because a screen reader
// cannot see a highlight. Rows and group headings are <template>s -- Tailwind
// reads templates, and the island clones rather than writes markup -- and the
// glyphs it needs are drawn here once, for the same reason.
//
// The sidebar's opener is hidden in the markup: without ui.js it would be a
// button that opens nothing.
func TestThePaletteIsOnEveryPageClosed(t *testing.T) {
	body := render(t, dataUI(catalogFake{}), "/ingest/loads")

	at := strings.Index(body, `<dialog id="palette"`)
	if at < 0 {
		t.Fatal("the page has no palette")
	}
	dialog := body[at : at+strings.Index(body[at:], "</dialog>")]
	if strings.Contains(dialog[:strings.Index(dialog, ">")], " open") {
		t.Error("the palette is drawn open")
	}
	for _, want := range []string{
		`role="combobox"`, `aria-controls="palette-list"`, `aria-autocomplete="list"`, `aria-expanded="false"`,
		`id="palette-list"`, `role="listbox"`,
		"<template data-palette-row>", `role="option"`, "<template data-palette-group>",
		`data-palette-icon-set`, `data-icon="workflow"`, `data-icon="database"`,
	} {
		if !strings.Contains(dialog, want) {
			t.Errorf("the palette lacks %s", want)
		}
	}

	aside, end := strings.Index(body, "<aside"), strings.Index(body, "</aside>")
	open := strings.Index(body, "data-palette-open")
	if open < aside || open > end {
		t.Fatal("the sidebar has no palette opener")
	}
	// data-js: shown only where a script runs, from the first paint
	// (nav-latency N6) -- not hidden and revealed after it.
	if wrap := body[strings.LastIndex(body[:open], "<span"):open]; !strings.Contains(wrap, "data-js") {
		t.Errorf("the opener is shown where no script can drive it: %s", wrap)
	}
	if !strings.Contains(body[open:end], "⌘K") {
		t.Error("the opener does not say its key")
	}
}
