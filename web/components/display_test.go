package components

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A STATS STRIP IS A DEFINITION LIST: each number is the definition of its
// label, which is how a screen reader pairs "Timeout" with "20 s" instead of
// reading two loose lines.
func TestAStatsStripPairsEachLabelWithItsNumber(t *testing.T) {
	got := markup(t, Stats([]Stat{
		{Label: "Timeout", Value: "20", Unit: "s", Icon: "clock"},
		{Label: "Rows", Value: "10,000"},
		{Label: "Bytes billed", Value: "1.0", Unit: "GB", Note: "per request"},
	}))
	if !strings.HasPrefix(strings.TrimSpace(got), "<dl") {
		t.Errorf("the strip is not a <dl>: %.80s", got)
	}
	if n := strings.Count(got, "<dt"); n != 3 {
		t.Errorf("%d labels for 3 numbers", n)
	}
	for _, want := range []string{">Timeout<", ">20<", ">s<", ">10,000<", ">GB<", ">per request<"} {
		if !strings.Contains(got, want) {
			t.Errorf("the strip lacks %s", want)
		}
	}
	dt, dd := strings.Index(got, ">Timeout<"), strings.Index(got, ">20<")
	if dd < dt {
		t.Error("a number comes before its label")
	}
}

// THE METHOD IS PRINTED AS THE PROTOCOL WRITES IT.
func TestAMethodPillSaysTheMethod(t *testing.T) {
	if got := markup(t, MethodPill("GET")); !strings.Contains(got, ">GET<") {
		t.Errorf("the pill does not say GET: %s", got)
	}
}

// THE FOOT SAYS HOW MUCH OF THE LIST IS SHOWN, and only says "of" when a
// filter is hiding something -- "15 of 15" is a sentence about nothing.
func TestATableFootSaysHowMuchIsShown(t *testing.T) {
	if got := markup(t, TableFoot(11, 15, "destinations")); !strings.Contains(got, "Showing 11 of 15 destinations") {
		t.Errorf("a filtered list says %q", got)
	}
	got := markup(t, TableFoot(15, 15, "destinations"))
	if !strings.Contains(got, "15 destinations") || strings.Contains(got, " of ") {
		t.Errorf("an unfiltered list says %q", got)
	}
}

// A DEFINITION LIST IS A <dl>, AND A MACHINE'S VALUE IS MONO: a URI, a cron,
// a key read in the same face as the prose around them are read as prose.
func TestADefinitionListIsADlAndMachineValuesAreMono(t *testing.T) {
	got := markup(t, DefinitionList([]Def{
		{Term: "Stream", Value: "orders", Mono: true},
		{Term: "Identity", Value: "by key"},
	}))
	if n := strings.Count(got, "<dt"); n != 2 || strings.Count(got, "<dd") != 2 {
		t.Errorf("the list is not two terms with two values: %s", got)
	}
	mono := strings.Index(got, ">orders<")
	if tag := got[strings.LastIndex(got[:mono], "<dd"):mono]; !strings.Contains(tag, "font-mono") {
		t.Errorf("a machine value is not mono: %s", tag)
	}
	plain := strings.Index(got, ">by key<")
	if tag := got[strings.LastIndex(got[:plain], "<dd"):plain]; strings.Contains(tag, "font-mono") {
		t.Errorf("prose is drawn mono: %s", tag)
	}
}

// A CODE BLOCK'S TEXT IS TEXT, AND ITS COPY BUTTON EXISTS ONLY WITH A SCRIPT.
//
// The code is escaped like any value -- a curl carrying `<` is still a curl.
// The button is `hidden` in the markup and ui.js reveals it: without the
// script it would be a button that does nothing, which is the shape this
// console refuses. The text stays selectable either way.
func TestACodeBlockIsEscapedAndItsCopyNeedsAScript(t *testing.T) {
	got := markup(t, CodeBlock("curl-orders", `curl "https://api/v1/orders?a=1&b=<2>"`))
	for _, want := range []string{`id="curl-orders"`, "&lt;2&gt;", `data-copy="#curl-orders"`, `aria-label="Copy"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the block lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, "<2>") {
		t.Error("the code is written into the page unescaped")
	}
	i := strings.Index(got, "data-copy")
	wrapper := got[strings.LastIndex(got[:i], "<span"):i]
	if !strings.Contains(wrapper, "hidden") {
		t.Errorf("the copy button is visible before a script can drive it: %s", wrapper)
	}

	raw, err := os.ReadFile("../assets/ui.js")
	if err != nil {
		t.Fatal(err)
	}
	js := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(string(raw), "")
	for _, want := range []string{`[data-copy]`, "navigator.clipboard", `removeAttribute("hidden")`} {
		if !strings.Contains(js, want) {
			t.Errorf("ui.js's copy never uses %s", want)
		}
	}
}

// A BADGE SAYS ITS STATE IN WORDS, not only in colour.
func TestABadgeSaysItsStateInWords(t *testing.T) {
	for status, word := range map[string]string{"success": "success", "failed": "failed", "": "never ran"} {
		if got := markup(t, State(status)); !strings.Contains(got, word) {
			t.Errorf("%q is drawn without the word %q: %s", status, word, got)
		}
	}
}
