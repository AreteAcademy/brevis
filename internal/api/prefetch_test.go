package api_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// A PAGE THE POINTER RESTS ON IS FETCHED BEFORE THE CLICK (nav-latency NL2):
// speculation rules, `moderate` -- a ~200 ms hover or the press itself -- for
// same-origin page links, so on real infrastructure the click opens a page
// already in hand.
//
// NEVER WHAT COSTS OR ACTS: sign-in and sign-out, the JSON API, the assets,
// any link with `tab=` (Preview is a billed warehouse query), and anything a
// template marks data-no-prefetch.
func TestPagesArePrefetchedOnHoverButNothingThatCostsOrActs(t *testing.T) {
	_, body := get(t, dataUI(catalogFake{}), "/ingest/loads")
	at := strings.Index(body, `<script type="speculationrules">`)
	if at < 0 {
		t.Fatal("the page has no speculation rules")
	}
	raw := body[at+len(`<script type="speculationrules">`):]
	raw = raw[:strings.Index(raw, "</script>")]

	var rules struct {
		Prefetch []struct {
			Where     map[string]any `json:"where"`
			Eagerness string         `json:"eagerness"`
		} `json:"prefetch"`
	}
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		t.Fatalf("the rules are not JSON: %v\n%s", err, raw)
	}
	if len(rules.Prefetch) != 1 || rules.Prefetch[0].Eagerness != "moderate" {
		t.Fatalf("rules = %+v, want one moderate prefetch", rules.Prefetch)
	}
	for _, want := range []string{`"/*"`, `"/login*"`, `"/logout*"`, `"/api/*"`, `"/assets/*"`, `tab=*`, `[data-no-prefetch]`} {
		if !strings.Contains(raw, want) {
			t.Errorf("the rules do not exclude or include %s", want)
		}
	}
	if strings.Contains(raw, "prerender") {
		t.Error("the rules prerender, which runs a page's scripts unasked")
	}
}
