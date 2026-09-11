// Publishing to a Pub/Sub topic that already exists.
//
// Runnable against the emulator, which is the only way an example for a managed
// service can be one:
//
//	docker compose -f ../docker-compose.drivers.yml up -d pubsub
//	export PUBSUB_EMULATOR_HOST=localhost:8085
//	go run ./13-pubsub -create-topic   # the INFRASTRUCTURE, once
//	go run ./13-pubsub                 # the pipeline
//	go run ./13-pubsub -read           # what a subscriber sees
//
// # The one thing to notice
//
// `-create-topic` is a separate flag, and it is not tidiness. The driver never
// creates a topic and never will: a pipeline that can create one can create the
// WRONG one, and unlike a mistyped table nobody finds out -- the messages go
// somewhere, and the subscriber that should have received them stays quiet.
// Creating a topic is an act of infrastructure.
//
// The same rule shapes everything below. The topic exists before this program
// does, its subscribers were written first and their filters were written
// first, so **the message is the payload and nothing else** unless this program
// says otherwise. There is no Brevis-shaped envelope anywhere in what a
// subscriber receives.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	gps "cloud.google.com/go/pubsub"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to/pubsub"
)

const (
	project      = "brevis-example"
	topic        = "orders"
	subscription = "orders-example"
)

func main() {
	var create, read bool
	flag.BoolVar(&create, "create-topic", false, "creates the topic and the subscription, then exits")
	flag.BoolVar(&read, "read", false, "reads what was published, then exits")

	// Both of these are OUTSIDE the pipeline on purpose. See the package
	// comment: one is infrastructure and the other is somebody else's program.
	flag.Parse()
	switch {
	case create:
		must(createTopic(context.Background()))
		return
	case read:
		must(readBack(context.Background()))
		return
	}

	sdk.Run(sdk.Pipeline{
		Name: "example_pubsub",

		// Any source at all. A topic is a destination like any other, which is
		// the point of the SDK's shape: this reads a file, and swapping it for
		// from.HTTP or from/postgres changes nothing below.
		Source: sdk.Source{
			From:    from.Files{Path: orders()},
			Preview: 3,
		},

		Target: sdk.Target{
			To: pubsub.Topic{
				Project: project,
				Name:    topic,

				// WHAT GOES IN THE ATTRIBUTES IS DECIDED HERE, and nothing is
				// added by the driver. These names are this program's -- a real
				// topic's contract would use whatever its subscribers already
				// filter on.
				//
				// Nil would be no attributes at all, and that is the default.
				//
				// It reads the PAYLOAD. Envelope also has Provider, Entity and
				// SourceKey, and no source in the SDK fills them -- from.Files
				// and every other built-in yield `{Payload: ...}` and nothing
				// else -- so `e.IngestionID()` would fail here. Writing this
				// example is what found that.
				Attributes: func(e sdk.Envelope) map[string]string {
					row, ok := e.Payload.(map[string]any)
					if !ok {
						return nil
					}
					return map[string]string{
						"orderId": fmt.Sprint(row["order_id"]),
						"region":  fmt.Sprint(row["region"]),
					}
				},

				// Ordering is OFF unless this is set, because it costs
				// throughput. With it, messages sharing a key arrive in order
				// -- here, every order of one customer.
				//
				//	OrderingKey: func(e sdk.Envelope) string {
				//		row, _ := e.Payload.(map[string]any)
				//		return fmt.Sprint(row["customer"])
				//	},
			},
		},
	})
}

// orders finds the sample file whether this is run from `examples/` or from
// inside the example, and FAILS LOUDLY when it is nowhere.
//
// The check is not defensive noise. `from.Files` with a path that does not
// exist yields zero records and NO error -- which is right for a glob over a
// directory that had nothing new today, and wrong for a filename somebody
// mistyped: the run succeeds, loads nothing, and says so in a line that looks
// like a quiet day. An example is not the place to demonstrate that.
func orders() string {
	for _, p := range []string{
		"testdata/orders.ndjson",           // run from inside 13-pubsub
		"13-pubsub/testdata/orders.ndjson", // run from examples/
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	fmt.Fprintln(os.Stderr, "error: orders.ndjson is nowhere. Run this from examples/ or from examples/13-pubsub")
	os.Exit(1)
	return ""
}

// createTopic is the infrastructure this pipeline REFUSES to do for you.
//
// In a real installation this is Terraform, or a console, or a platform team --
// anywhere but the pipeline. It is here so the example runs.
func createTopic(ctx context.Context) error {
	c, err := gps.NewClient(ctx, project)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	t, err := c.CreateTopic(ctx, topic)
	if err != nil {
		// Already there is the normal case on a second run.
		t = c.Topic(topic)
		fmt.Printf("  topic %q already exists\n", topic)
	} else {
		fmt.Printf("  topic %q created\n", topic)
	}

	// A subscription, so `-read` has something to read. A real one belongs to
	// whoever consumes, not to whoever publishes -- which is exactly why the
	// publisher must not decide what the messages look like.
	if _, err := c.CreateSubscription(ctx, subscription, gps.SubscriptionConfig{Topic: t}); err != nil {
		fmt.Printf("  subscription %q already exists\n", subscription)
		return nil
	}
	fmt.Printf("  subscription %q created\n", subscription)
	return nil
}

// readBack is the subscriber, and it is deliberately a different program.
//
// Brevis publishes; what reads is somebody else's. This exists so the example
// can show what actually arrives -- which is the payload, plus exactly the
// attributes the pipeline above asked for.
func readBack(ctx context.Context) error {
	c, err := gps.NewClient(ctx, project)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	sub := c.Subscription(subscription)
	sub.ReceiveSettings.MaxOutstandingMessages = 10

	// Receive runs the callback CONCURRENTLY, on several goroutines. The first
	// version of this counted and printed inside it, and the output came out
	// interleaved with every message numbered 3 -- a data race, in an example.
	// Collect under a lock, print after.
	var mu sync.Mutex
	var got []string

	err = sub.Receive(ctx, func(_ context.Context, m *gps.Message) {
		var payload map[string]any
		_ = json.Unmarshal(m.Data, &payload)
		line := fmt.Sprintf("    data:       %v\n    attributes: %v", payload, m.Attributes)

		mu.Lock()
		got = append(got, line)
		mu.Unlock()

		m.Ack()
	})
	if err != nil && ctx.Err() == nil {
		return err
	}

	// Sorted, so two runs of this example print the same thing. Pub/Sub does
	// not promise an order without an ordering key, and pretending it does is
	// how an example teaches something untrue.
	sort.Strings(got)
	for i, line := range got {
		fmt.Printf("  message %d\n%s\n", i+1, line)
	}
	fmt.Printf("\n  %d message(s) read\n", len(got))
	return nil
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
