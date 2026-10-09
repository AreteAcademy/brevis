package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/auth"
	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
)

var signedIn = auth.Credential{User: "ana", Hash: "$2a$10$notarealhash"}

// A DESTINATION THAT IS NOT A RELATION GETS NO TABS AT ALL.
//
// A bucket and a topic have no columns and no rows. A tab on one is a box
// that can only ever say no, which is worse than no tab: it invites somebody
// to try, and then explains. Reproduced on the running console this
// afternoon -- `file:///data/landing/` offered Preview and Query and
// answered "only BigQuery destinations can be read today".
func TestABucketOrATopicGetsNoTabs(t *testing.T) {
	for _, target := range []string{
		"file:///data/landing/",
		"s3://bucket/prefix",
		"gs://bucket/prefix",
		"pubsub://acme/clicks",
	} {
		t.Run(target, func(t *testing.T) {
			asked := false
			svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				asked = true
				_, _ = w.Write([]byte(`{"columns":["k"],"rows":[["1"]]}`))
			}))
			defer svc.Close()
			ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, target, "file")

			body := render(t, ui, "/data/target?u="+url.QueryEscape(target))
			for _, gone := range []string{"tab=preview", "tab=query"} {
				if strings.Contains(body, gone) {
					t.Errorf("a destination that is not a relation offers %q", gone)
				}
			}
			// AND NOT BY HIDING A LINK. The parameter typed by hand must not
			// reach a warehouse either.
			render(t, ui, "/data/target?u="+url.QueryEscape(target)+"&tab=preview")
			if asked {
				t.Error("it asked a SQL service about a bucket")
			}
		})
	}
}

// A RELATION THE SERVICE CANNOT READ YET STILL GETS ITS TABS, with the
// reason in them. `mysql://` is a table; nobody has written the dialect.
// That is a sentence worth showing, and the opposite of a bucket.
func TestARelationWithNoReaderKeepsItsTabs(t *testing.T) {
	svc := (&sqlFake{status: http.StatusBadRequest,
		body: `{"error":"mysql destinations cannot be read; bigquery and postgres can"}`}).start(t)
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, "mysql://app/orders", "mysql")

	body := render(t, ui, "/data/target?u="+url.QueryEscape("mysql://app/orders"))
	for _, want := range []string{"tab=preview", "tab=query"} {
		if !strings.Contains(body, want) {
			t.Errorf("a relation lost its %q", want)
		}
	}
}

// NO CONNECTION: SAY WHICH ONE IS MISSING.
//
// `serve` answers "no connection is declared for that destination" and
// deliberately names nothing -- its refusals never echo their input. The
// CONSOLE can: it holds the target, it is already drawing it at the top of
// the page, and a reader needs the database to go and declare it.
func TestANoConnectionRefusalNamesTheDatabase(t *testing.T) {
	f := &sqlFake{status: http.StatusBadRequest,
		body: `{"error":"no connection is declared for that destination","code":"no-connection"}`}
	svc := f.start(t)
	target := "postgres://brevis_it/public/orders"
	ui := consoleOf(t, sqlserve.New(svc.URL, ""), signedIn, target, "postgres")

	body := render(t, ui, "/data/target?u="+url.QueryEscape(target)+"&tab=preview")
	for _, want := range []string{"brevis_it", "brevis.yaml"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page never says %q, so nobody knows what to declare", want)
		}
	}
	// AND THE PAGE SURVIVES IT. A missing declaration is a configuration
	// gap, not an error page.
	if !strings.Contains(body, "Writers") {
		t.Error("the page lost the rest of itself")
	}
}

// `serve` UNREACHABLE: A REASON WHERE THE GRID WOULD BE, and never the
// address. An internal host is not a reader's business and a screenshot
// travels.
func TestServeUnreachableDrawsAReasonAndNoAddress(t *testing.T) {
	// A server that is closed before the request: the client gets a
	// transport error, which is the shape of a service that is not there.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := dead.URL
	dead.Close()

	ui := consoleOf(t, sqlserve.New(addr, ""), signedIn, probeTarget, "bigquery")
	body := render(t, ui, "/data/target?u="+probeTarget+"&tab=preview")

	if !strings.Contains(body, "could not be reached") {
		t.Error("the page does not say the service is unreachable")
	}
	if strings.Contains(body, strings.TrimPrefix(addr, "http://")) {
		t.Error("the page carries the service's address")
	}
	if !strings.Contains(body, "Writers") {
		t.Error("the page lost the rest of itself")
	}
}
