package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	// v1, deprecated upstream in favour of v2 -- and deliberately the same
	// major the SDK driver imports (sdk/go.mod pins v1.30.0). A test that
	// published with one client and read with another would be exercising a
	// combination that never ships. It moves when the driver moves.
	"cloud.google.com/go/pubsub" //nolint:staticcheck
	"github.com/AreteAcademy/brevis/gateway"
)

// The whole path, against a real emulator: POST → hook → identity → batch →
// topic, and then read back what a SUBSCRIBER sees.
//
// Reading it back is the point. Everything up to the publish can be asserted
// against a fake; what a consumer actually receives is the only thing that says
// the contract held.
func TestIntegrationAnEventReachesTheTopicAsItWasSent(t *testing.T) {
	host := os.Getenv("PUBSUB_EMULATOR_HOST")
	if host == "" {
		t.Skip("PUBSUB_EMULATOR_HOST is not set")
	}
	const project = "gw-test"
	topic := fmt.Sprintf("clicks-%d", time.Now().UnixNano())
	sub := createTopic(t, project, topic)

	cfg := configFor(t, project, topic, `
    hook: enrich
    sink:
      type: pubsub
      project: `+project+`
      topic: `+topic+`
      attributes: [tenant, region]`)

	hooks := gateway.NewHooks()
	hooks.MustRegister("enrich", func(e map[string]any) (map[string]any, error) {
		host, _ := e["host"].(string)
		e["tenant"] = strings.Split(host, ".")[0]
		return e, nil
	})

	srv, err := gateway.New(cfg, hooks)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"event_id":"e-1","occurred_at":"2026-09-24T10:00:00Z",` +
		`"host":"acme.example.com","region":"sa-east-1","amount":123.45}`
	post(t, ts.URL+"/v1/clicks", body, http.StatusAccepted)

	if err := srv.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}

	got := receive(t, project, sub, 1)
	if len(got) != 1 {
		t.Fatalf("the subscriber saw %d messages", len(got))
	}
	m := got[0]

	var payload map[string]any
	if err := json.Unmarshal(m.Data, &payload); err != nil {
		t.Fatalf("the message is not JSON: %v", err)
	}

	// The payload arrives as it was sent, plus what the hook added. Nothing
	// Brevis-shaped wraps it: the topic's contract belongs to whoever owns it.
	if payload["host"] != "acme.example.com" || payload["region"] != "sa-east-1" {
		t.Errorf("the original fields did not survive: %v", payload)
	}
	if payload["tenant"] != "acme" {
		t.Errorf("the hook did not run: tenant=%v", payload["tenant"])
	}
	// The amount is a number and must not come back as 1.2345e+02.
	if fmt.Sprint(payload["amount"]) != "123.45" {
		t.Errorf("amount arrived as %v", payload["amount"])
	}

	// The ingestion_id travels ON the payload, so a consumer can deduplicate.
	id, _ := payload["ingestion_id"].(string)
	if len(id) != 36 {
		t.Errorf("ingestion_id is %q", id)
	}

	// Only the declared attributes, and a field the event lacks is ABSENT
	// rather than empty.
	if m.Attributes["tenant"] != "acme" || m.Attributes["region"] != "sa-east-1" {
		t.Errorf("attributes are %v", m.Attributes)
	}
	if len(m.Attributes) != 2 {
		t.Errorf("the gateway added an attribute of its own: %v", m.Attributes)
	}
}

// A retried POST is the same event, so the id is the same -- which is what lets
// anything downstream absorb the retry.
func TestIntegrationARetryCarriesTheSameIngestionID(t *testing.T) {
	if os.Getenv("PUBSUB_EMULATOR_HOST") == "" {
		t.Skip("PUBSUB_EMULATOR_HOST is not set")
	}
	const project = "gw-test"
	topic := fmt.Sprintf("retry-%d", time.Now().UnixNano())
	sub := createTopic(t, project, topic)

	cfg := configFor(t, project, topic, `
    sink:
      type: pubsub
      project: `+project+`
      topic: `+topic)
	srv, err := gateway.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"event_id":"e-9","occurred_at":"2026-09-24T10:00:00Z","host":"a.b"}`
	post(t, ts.URL+"/v1/clicks", body, http.StatusAccepted)
	post(t, ts.URL+"/v1/clicks", body, http.StatusAccepted)
	if err := srv.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := receive(t, project, sub, 2)
	if len(got) != 2 {
		t.Fatalf("expected both publishes, saw %d", len(got))
	}
	ids := make([]string, 0, 2)
	for _, m := range got {
		var p map[string]any
		_ = json.Unmarshal(m.Data, &p)
		ids = append(ids, fmt.Sprint(p["ingestion_id"]))
	}
	if ids[0] != ids[1] {
		t.Errorf("the same event produced two ids: %s and %s", ids[0], ids[1])
	}
}

func configFor(t *testing.T, project, topic, tail string) *gateway.Config {
	t.Helper()
	yaml := `
name: test_gateway
listen:
  addr: :0
streams:
  - name: clicks
    path: /v1/clicks
    format: json
    identity:
      provider: web
      entity: click
      source_key: event_id
      record_ts: occurred_at
    buffer:
      flush:
        records: 500
        every: 1h` + tail + "\n"

	path := t.TempDir() + "/g.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := gateway.Load(path)
	if err != nil {
		t.Fatalf("loading the config: %v", err)
	}
	return cfg
}

func post(t *testing.T, url, body string, want int) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body)) //nolint:gosec,noctx
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		t.Fatalf("POST returned %d, want %d", resp.StatusCode, want)
	}
}

func createTopic(t *testing.T, project, topic string) string {
	t.Helper()
	ctx := context.Background()
	cli, err := pubsub.NewClient(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	tp, err := cli.CreateTopic(ctx, topic)
	if err != nil {
		t.Fatalf("creating the topic: %v", err)
	}
	s, err := cli.CreateSubscription(ctx, topic+"-sub", pubsub.SubscriptionConfig{Topic: tp})
	if err != nil {
		t.Fatalf("creating the subscription: %v", err)
	}
	return s.ID()
}

func receive(t *testing.T, project, sub string, want int) []*pubsub.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cli, err := pubsub.NewClient(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()

	var got []*pubsub.Message
	done := make(chan struct{})
	go func() {
		_ = cli.Subscription(sub).Receive(ctx, func(_ context.Context, m *pubsub.Message) {
			got = append(got, m)
			m.Ack()
			if len(got) >= want {
				cancel()
			}
		})
		close(done)
	}()
	<-done

	sort.Slice(got, func(i, j int) bool { return string(got[i].Data) < string(got[j].Data) })
	return got
}
