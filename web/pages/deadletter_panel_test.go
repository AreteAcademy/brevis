package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// gatewayPage renders a destination whose one writer is a gateway stream with
// these published fields.
func gatewayPage(t *testing.T, fields ...catalog.ManifestField) string {
	t.Helper()
	d := postgres.TargetDetail{CatalogEntry: postgres.CatalogEntry{
		Target: "bigquery://acme/landing/clicks", Kind: "bigquery",
		Writers: []postgres.CatalogWriter{{Gateway: &postgres.GatewayWriter{
			Name: "edge", Stream: "/v1/clicks", Role: "sink", Kind: "bigquery",
			PublishedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
			Config:      &postgres.WriterConfig{Fields: fields},
		}}},
	}}
	var b bytes.Buffer
	if err := configurations(BuildTarget(d, time.Now())).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func f(path string, v any) catalog.ManifestField { return catalog.ManifestField{Path: path, Value: v} }

// #86: the page says where a stream's refused batches go and how to get them
// back -- the commands, ready to copy, for where the credentials are. The
// console reads nothing of the dead letter itself.
func TestTheDestinationPageShowsHowToResendTheDeadLetter(t *testing.T) {
	html := gatewayPage(t,
		f("name", "clicks"), f("sink.type", "bigquery"), f("sink.write", "append"),
		f("dead_letter.type", "files"), f("dead_letter.path", "gs://acme-dl/clicks/"))

	for _, want := range []string{
		"data-deadletter",
		"gs://acme-dl/clicks/",
		// Two lines: copied as one they are a command that fails.
		"gateway deadletter ls &lt;gateway.yaml&gt; --stream clicks\ngateway deadletter resend &lt;gateway.yaml&gt; &lt;batch&gt; --dry-run",
		// append: what the resend protects and what it refuses.
		"--allow-duplicates",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
}

// Under merge the dedup key is the protection, and the page says that instead.
func TestTheDeadLetterPanelSaysMergeIsProtected(t *testing.T) {
	html := gatewayPage(t,
		f("name", "orders"), f("sink.type", "postgres"), f("sink.write", "merge"),
		f("dead_letter.type", "files"), f("dead_letter.path", "/var/dead-letter/orders/"))
	if !strings.Contains(html, "--stream orders") || !strings.Contains(html, "dedup") {
		t.Error("a merge stream's panel does not say the dedup key protects a resend")
	}
	if strings.Contains(html, "--allow-duplicates") {
		t.Error("a merge stream's panel warns about duplicates")
	}
}

// auto_table writes as its `into` says.
func TestTheDeadLetterPanelReadsAutoTablesInto(t *testing.T) {
	html := gatewayPage(t,
		f("name", "tables"), f("sink.type", "auto_table"), f("sink.into.write", "append"),
		f("dead_letter.type", "files"), f("dead_letter.path", "gs://acme-dl/tables/"))
	if !strings.Contains(html, "--allow-duplicates") {
		t.Error("an auto_table appending into its tables is not warned about")
	}
}

// Only a files dead letter can be read back; anything else gets the reason,
// not a command that would fail.
func TestTheDeadLetterPanelForAnUnreadableDeadLetter(t *testing.T) {
	html := gatewayPage(t,
		f("name", "events"), f("sink.type", "bigquery"), f("sink.write", "append"),
		f("dead_letter.type", "pubsub"), f("dead_letter.project", "acme"), f("dead_letter.topic", "dl"))
	if !strings.Contains(html, "data-deadletter") || strings.Contains(html, "gateway deadletter ls") {
		t.Error("a pubsub dead letter got a command, or no panel at all")
	}
	if !strings.Contains(html, "files") {
		t.Error("the panel does not say only a files dead letter can be read back")
	}
}

// A stream that published no dead letter has no panel.
func TestNoDeadLetterNoPanel(t *testing.T) {
	html := gatewayPage(t, f("name", "clicks"), f("sink.type", "bigquery"), f("sink.write", "append"))
	if strings.Contains(html, "data-deadletter") {
		t.Error("a panel for a dead letter nobody published")
	}
}

// A sink with no write mode -- Pub/Sub -- neither appends nor merges, and the
// panel must not claim the dedup key protects it. Found on the local stack.
func TestTheDeadLetterPanelForADestinationWithNoMerge(t *testing.T) {
	html := gatewayPage(t,
		f("name", "clicks"), f("sink.type", "pubsub"),
		f("dead_letter.type", "files"), f("dead_letter.path", "/var/dead-letter/clicks/"))
	if strings.Contains(html, "dedup key") || strings.Contains(html, "This stream merges") {
		t.Error("a Pub/Sub stream is told the dedup key protects a resend")
	}
	if !strings.Contains(html, "idempotent") {
		t.Error("the panel does not say the consumer has to be idempotent")
	}
}
