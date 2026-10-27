package components

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func markup(t *testing.T, c templ.Component) string {
	t.Helper()
	var b bytes.Buffer
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// A BUTTON IS `type="button"` UNLESS IT SAYS OTHERWISE. A <button> inside a
// form defaults to submit, and a control that submits the form around it by
// accident is the classic way a filter bar posts a statement.
func TestAButtonDoesNotSubmitUnlessAsked(t *testing.T) {
	got := markup(t, Button(ButtonSecondary, SizeRegular, nil))
	if !strings.Contains(got, `type="button"`) {
		t.Errorf("a plain button is not type=button: %s", got)
	}
	got = markup(t, Button(ButtonPrimary, SizeRegular, templ.Attributes{"type": "submit", "form": "workbench"}))
	if !strings.Contains(got, `type="submit"`) || !strings.Contains(got, `form="workbench"`) {
		t.Errorf("a button asked to submit a form does not: %s", got)
	}
	if strings.Count(got, `type=`) != 1 {
		t.Errorf("the button carries two types: %s", got)
	}
}

// EACH KIND LOOKS LIKE ITSELF. Kinds drawn the same would be one kind with
// several names -- which is what ButtonAccent now openly is: Primary's older
// name, the same constant, not a fourth kind.
func TestEveryButtonKindIsDistinct(t *testing.T) {
	seen := map[string]ButtonKind{}
	for _, k := range []ButtonKind{ButtonPrimary, ButtonSecondary, ButtonDanger} {
		got := markup(t, Button(k, SizeRegular, nil))
		if prev, ok := seen[got]; ok {
			t.Errorf("%v draws exactly like %v", k, prev)
		}
		seen[got] = k
	}
	if markup(t, Button(ButtonPrimary, SizeSmall, nil)) == markup(t, Button(ButtonPrimary, SizeRegular, nil)) {
		t.Error("a small button is the same size as a regular one")
	}
}

// AN ICON BUTTON HAS NO TEXT, SO IT MUST HAVE A NAME. The label is a required
// argument rather than an attribute somebody may remember: an unlabelled icon
// button reads to a screen reader as "button", and to a mouse as a guess.
func TestAnIconButtonIsNamed(t *testing.T) {
	got := markup(t, IconButton("Copy URI", "copy", nil))
	for _, want := range []string{`aria-label="Copy URI"`, `title="Copy URI"`, `type="button"`, `<svg`} {
		if !strings.Contains(got, want) {
			t.Errorf("the icon button lacks %s: %s", want, got)
		}
	}
}

func TestAKeyIsAKbd(t *testing.T) {
	if got := markup(t, Kbd("⌘↵")); !strings.Contains(got, "<kbd") || !strings.Contains(got, "⌘↵") {
		t.Errorf("a key is not drawn as <kbd>: %s", got)
	}
}

// THE SEARCH BOX HAS A NAME, A VALUE AND A KEY. Its name is spoken, because a
// placeholder is not a label; its value survives the round trip, because the
// filter it carries is in the URL; `/` focuses it, which is what every list
// tool binds.
func TestTheSearchBoxIsNamedAndFocusedBySlash(t *testing.T) {
	got := markup(t, SearchInput("q", "orders", "Search destinations", ""))
	for _, want := range []string{`type="search"`, `name="q"`, `value="orders"`,
		`aria-label="Search destinations"`, `placeholder="Search destinations"`, `data-hotkey="/"`, "<kbd"} {
		if !strings.Contains(got, want) {
			t.Errorf("the search box lacks %s: %s", want, got)
		}
	}
}

// A SEGMENTED FILTER IS LINKS, so it works with no script and a filter is a
// URL somebody can send. The chosen one says so with `aria-current="true"` --
// `aria-pressed` belongs to buttons, and on a link it is a promise the
// element cannot keep. Not "page": the sidebar owns that one.
func TestASegmentedFilterIsLinksAndMarksTheChosenOne(t *testing.T) {
	got := markup(t, Segmented("Status", []SegOption{
		{Label: "All", Href: "/data"},
		{Label: "Late", Href: "/data?status=late", Count: "3", Current: true},
	}))
	for _, want := range []string{`role="group"`, `aria-label="Status"`, `href="/data?status=late"`, ">3<"} {
		if !strings.Contains(got, want) {
			t.Errorf("the filter lacks %s: %s", want, got)
		}
	}
	if n := strings.Count(got, `aria-current="true"`); n != 1 {
		t.Errorf("%d options are current, and one was chosen", n)
	}
	i := strings.Index(got, `aria-current="true"`)
	if tag := got[strings.LastIndex(got[:i], "<a"):i]; !strings.Contains(tag, `status=late`) {
		t.Errorf("the wrong option is current: %s", tag)
	}
	if strings.Contains(got, "aria-pressed") || strings.Contains(got, `aria-current="page"`) {
		t.Error("the filter claims a state that is not its own")
	}
}

// TABS ARE A TABLIST WITH EXACTLY ONE SELECTED. The markup is the contract;
// the island that owns the panels drives it.
func TestTabsAreATablistWithOneSelected(t *testing.T) {
	got := markup(t, Tabs("How to read the answer", []Tab{
		{Key: "results", Label: "Results", Selected: true},
		{Key: "json", Label: "JSON"},
	}))
	for _, want := range []string{`role="tablist"`, `aria-label="How to read the answer"`, `role="tab"`, `data-tab="results"`, `data-tab="json"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the tabs lack %s: %s", want, got)
		}
	}
	if n := strings.Count(got, `aria-selected="true"`); n != 1 {
		t.Errorf("%d tabs are selected", n)
	}
	if n := strings.Count(got, `type="button"`); n != 2 {
		t.Errorf("a tab inside a form would submit it: %d of 2 are type=button", n)
	}
	// A label with its count ("Sources 3") never breaks in two on a phone:
	// the strip scrolls instead (screen-load-v2).
	if !strings.Contains(got, "whitespace-nowrap") || !strings.Contains(got, "overflow-x-auto") {
		t.Error("a tab's label can wrap, or the strip cannot scroll")
	}
}

// `/` IS WIRED ONCE, AND NEVER WHILE SOMEBODY IS TYPING. A slash typed into a
// query, a filter or the editor is a character, not a shortcut.
func TestSlashFocusesTheSearchExceptWhileTyping(t *testing.T) {
	raw, err := os.ReadFile("../assets/ui.js")
	if err != nil {
		t.Fatal(err)
	}
	js := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(string(raw), "")
	for _, want := range []string{`e.key !== "/"`, `[data-hotkey="/"]`, "isContentEditable", "CodeMirror"} {
		if !strings.Contains(js, want) {
			t.Errorf("ui.js's slash handler never uses %s", want)
		}
	}
}

// THE PRIMARY ACTION IS THE ACCENT, and the label on it is the ground
// colour that reads on it (on-accent, measured in contrast_test.go). The
// workbench's Run was the only Accent button and is the SQL screen's primary
// action, so the two kinds are one look.
func TestThePrimaryActionIsTheAccent(t *testing.T) {
	primary := buttonClass(ButtonPrimary, SizeRegular)
	for _, want := range []string{"bg-accent-strong", "text-on-accent"} {
		if !strings.Contains(primary, want) {
			t.Errorf("the primary button lacks %s: %s", want, primary)
		}
	}
	if ButtonAccent != ButtonPrimary {
		t.Error("ButtonAccent is a kind of its own again, beside the primary action")
	}
}

// 32px, and 26px where a row or a toolbar is dense: the design's two heights.
func TestButtonsAreThirtyTwoOrTwentySixPixelsTall(t *testing.T) {
	if c := buttonClass(ButtonSecondary, SizeRegular); !strings.Contains(c, "h-8 ") {
		t.Errorf("a regular button is not 32px: %s", c)
	}
	if c := buttonClass(ButtonSecondary, SizeSmall); !strings.Contains(c, "h-[26px] ") {
		t.Errorf("a small button is not 26px: %s", c)
	}
}
