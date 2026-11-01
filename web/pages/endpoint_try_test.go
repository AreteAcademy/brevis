package pages

import (
	"errors"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/infrastructure/endpointcall"
)

// tryView is the fresh endpoint's page on a console that can run it, with
// Try it's form and, when given, an answer.
func tryView(t *testing.T, a *endpointcall.Answer, runErr error) EndpointView {
	t.Helper()
	all, entries := endpointPageFixture()
	e := endpointNamed(all, "fresh")
	v := BuildEndpoint(e, entries, catalogNow)
	v.CanRun, v.Form, v.Answer, v.RunErr = true, TryForm(e.Endpoint.Params, nil), a, runErr
	return v
}

// TRY IT SPEAKS THE READER'S LANGUAGE (screen-endpoints T5): the form, the
// button, the answer's words and the call from outside; the parameters'
// names, the values and the columns are the data's own.
func TestTryItHasNoEnglishLeft(t *testing.T) {
	hit := &endpointcall.Answer{Status: 200, Cache: "hit", Millis: 12, Received: 600,
		Columns: []string{"day"}, Rows: [][]any{{"2026-10-12"}}}
	empty := &endpointcall.Answer{Status: 200, Cache: "miss", Millis: 80}
	var text string
	for _, v := range []EndpointView{tryView(t, nil, nil), tryView(t, hit, nil), tryView(t, empty, nil)} {
		text += visibleText(regexpPre.ReplaceAllString(drawIn(t, "pt-BR", Endpoint(v)), " "))
	}
	if !strings.Contains(text, "Executar requisição") || !strings.Contains(text, "Testar") {
		t.Error("Try it is not in Portuguese")
	}
	for _, english := range []string{"Try it", "Run request", "Runs through", "required", "default", " rows",
		"Showing the first", "No rows", "The answer appears here", "Call it", "From the cache", "Went to the warehouse"} {
		if at := strings.Index(text, english); at >= 0 {
			t.Errorf("Try it still says %q: …%s…", english, text[max(0, at-60):min(len(text), at+40)])
		}
	}
}

// THE ANSWER SAYS WHERE IT CAME FROM: the status with its reason, the time,
// the rows, and the cache as the API said it -- HIT or MISS -- with what that
// means for the warehouse.
func TestTheAnswerExplainsTheCache(t *testing.T) {
	for cache, want := range map[string]string{
		"hit":    "From the cache: the warehouse was not queried.",
		"miss":   "Went to the warehouse, and the answer was kept in the cache.",
		"bypass": "The cache was not used: the warehouse answered.",
	} {
		page := drawIn(t, "en", EndpointAnswer(tryView(t, &endpointcall.Answer{Status: 200, Cache: cache, Millis: 12}, nil)))
		if !strings.Contains(page, ">"+strings.ToUpper(cache)+"<") || !strings.Contains(page, want) {
			t.Errorf("%s: %s", cache, visibleText(page))
		}
		if !strings.Contains(page, "200 OK") || !strings.Contains(page, "12 ms") {
			t.Errorf("%s: the status or the time is missing: %s", cache, visibleText(page))
		}
	}
}

// WHY A RUN BROUGHT NOTHING BACK IS SAID IN THE READER'S WORDS: the console's
// own failures are known errors, said by key; anything else is said as it
// came.
func TestARunErrorIsSaidInTheReadersWords(t *testing.T) {
	page := drawIn(t, "pt-BR", EndpointAnswer(tryView(t, nil, endpointcall.ErrUnreachable)))
	if !strings.Contains(page, "A API não respondeu") {
		t.Errorf("unreachable: %s", visibleText(page))
	}
	page = drawIn(t, "pt-BR", EndpointAnswer(tryView(t, nil, errors.New("something else"))))
	if !strings.Contains(page, "something else") {
		t.Errorf("an unknown error: %s", visibleText(page))
	}
}

// TRY IT IS A 360px COLUMN THAT STAYS AS THE PAGE SCROLLS, and the call from
// outside goes with it; where the console cannot run the endpoint, the call
// stays in the page.
func TestTryItIsAColumnWithTheCallFromOutside(t *testing.T) {
	page := drawIn(t, "en", Endpoint(tryView(t, nil, nil)))
	side := page[strings.Index(page, "data-side"):]
	if !strings.Contains(page, "_360px]") || !strings.Contains(side, "sticky") {
		t.Error("Try it is not a sticky 360px column")
	}
	if !strings.Contains(side, `id="endpoint-curl"`) || !strings.Contains(side, "Call from outside") {
		t.Error("the call from outside is not beside the form")
	}
	v := tryView(t, nil, nil)
	v.CanRun = false
	if page := drawIn(t, "en", Endpoint(v)); !strings.Contains(page, `id="endpoint-curl"`) || strings.Contains(page, "data-side") {
		t.Error("with no Try it, the call from outside left the page")
	}
}
