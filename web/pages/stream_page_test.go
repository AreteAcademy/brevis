package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// clicksPage is the clicks stream's page with one endpoint reading its sink.
func clicksPage() StreamView {
	s := StreamsOf(printFixtures(time.Now()))[0].Streams[0]
	v := BuildStream(s, nil)
	v.ReadBy = ReadersOf([]postgres.PublishedEndpoint{{API: "metrics-api", Name: "clicks", Path: "/v1/clicks/daily",
		Reads: []string{"pubsub://brevis-local/clicks"}}})
	return v
}

// A STREAM'S PAGE SAYS WHAT IT IS AT THE TOP (screen-streams T3): the
// gateway above the name, one sentence of what it does, and a strip from the
// gateway to the destination; Query destination where SQL reads it, and Send
// test event as the page's verb.
func TestAStreamPageSaysWhatItIs(t *testing.T) {
	page := drawIn(t, "en", StreamPage(clicksPage()))
	for _, want := range []string{
		"Stream · gateway", "events_gateway", ">POST /v1/clicks</h1>",
		"Takes json events over HTTP, batches them and writes them to Google Pub/Sub. What the destination refuses goes to the dead letter.",
		"data-stream-strip", "continuous", "Gateway HTTP", `aria-label="Google Pub/Sub"`, "pubsub://brevis-local/clicks",
		`href="#test-event"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the stream's head has no %q", want)
		}
	}
	if strings.Contains(page, ">Properties<") {
		t.Error("the properties panel is still there")
	}
}

// THE OVERVIEW: the flow numbered, from source through the buffer to the
// destination, with what is refused hanging off it; the stream in one
// sentence; and where it goes next -- the destination, in the catalog, then
// the endpoints that read it.
func TestAStreamsOverviewFollowsTheData(t *testing.T) {
	page := drawIn(t, "en", StreamPage(clicksPage()))
	overview := page[strings.Index(page, `id="overview"`):strings.Index(page, `id="config"`)]
	for _, want := range []string{
		"1 · Source", "2 · Buffer", "3 · Destination", "If the destination refuses", "refused batches",
		"every 1 second or 500 events", "data-flow-packet",
		"In one sentence", "JSON events posted to /v1/clicks",
		"Where it goes next", `href="/data/target?u=pubsub%3A%2F%2Fbrevis-local%2Fclicks"`, "/v1/clicks/daily", "metrics-api",
	} {
		if !strings.Contains(overview, want) {
			t.Errorf("the overview has no %q", want)
		}
	}
}

// THE STREAM'S PAGE SPEAKS THE READER'S LANGUAGE above the tabs and in the
// overview (screen-streams T3); the configuration and the dead letter are
// T4's and T5's.
func TestAStreamsHeadAndOverviewHaveNoEnglishLeft(t *testing.T) {
	page := drawIn(t, "pt-BR", StreamPage(clicksPage()))
	head := visibleText(page[:strings.Index(page, `id="config"`)])
	for _, english := range []string{
		"gateway stream", "Takes", "batches them", "What the destination refuses", "continuous", "published",
		"Send test event", "Overview", "Configuration", "Source", "Destination", "If the destination",
		"refused batches", "every 1", "In one sentence", "Where it goes next", "open in the catalog",
		"volume and latency", "Properties",
	} {
		if at := strings.Index(head, english); at >= 0 {
			t.Errorf("the Portuguese stream still says %q: …%s…", english, head[max(0, at-60):min(len(head), at+40)])
		}
	}
}
