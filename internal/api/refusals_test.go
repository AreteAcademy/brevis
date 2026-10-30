package api_test

import (
	"net/http"
	"net/http/httptest"
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

			body := render(t, ui, "/ingest/loads/bronze/load")
			for _, gone := range []string{"/sql?target="} {
				if strings.Contains(body, gone) {
					t.Errorf("a destination that is not a relation offers %q", gone)
				}
			}
			// AND NOT BY HIDING A LINK. The parameter typed by hand must not
			// reach a warehouse either.
			render(t, ui, "/ingest/loads/bronze/load?tab=preview")
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

	body := render(t, ui, "/ingest/loads/bronze/load")
	for _, want := range []string{"/sql?target="} {
		if !strings.Contains(body, want) {
			t.Errorf("a relation lost its %q", want)
		}
	}
}
