package api_test

import (
	"regexp"
	"strings"
	"testing"
)

// AT MOST ONE PRIMARY ACTION ON A PAGE. A drawer or a modal may have its
// own -- it is a screen of its own while it is open -- so what is inside a
// <dialog> does not count.
func TestNoPageHasMoreThanOnePrimaryAction(t *testing.T) {
	_, browse := browsing(t)
	data := dataUI(catalogFake{entries: demoCatalog()}).WithEndpoints(endpointsFake{})
	dialogs := regexp.MustCompile(`(?s)<dialog.*?</dialog>`)
	for _, page := range []struct {
		path string
		body string
	}{
		{"/ingest/loads", render(t, data, "/ingest/loads")},
		{"/ingest/streams", render(t, data, "/ingest/streams")},
		{"/data/endpoints", render(t, data, "/data/endpoints")},
		{"/sql", render(t, browse, "/sql")},
	} {
		path, body := page.path, page.body
		outside := dialogs.ReplaceAllString(body, "")
		if n := strings.Count(outside, "bg-accent-strong text-on-accent"); n > 1 {
			t.Errorf("%s has %d primary actions", path, n)
		}
	}
}
