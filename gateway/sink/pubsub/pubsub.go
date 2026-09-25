// Package pubsub publishes a gateway's batches to a Google Pub/Sub topic.
//
// Importing it costs around 7 MB: the Google client stack, gRPC and the auth
// chain. It is the second most expensive sink after BigQuery, and the two
// share most of what they bring.
package pubsub

import (
	"context"
	"fmt"
	"strings"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
	topubsub "github.com/AreteAcademy/brevis/sdk/to/pubsub"
)

// Sink is what the YAML calls this driver.
const Sink = "pubsub"

// New builds the driver from a config block.
//
// The SDK driver does the work; this is the translation from a YAML file to
// the two functions it takes. Nothing about the payload is touched: what a
// subscriber receives is the event as it arrived, after the hook, and the
// attributes the file named -- which is the rule that driver was built on.
func New(b gateway.Build) (gateway.Sinker, error) {
	s := b.Sink
	if strings.TrimSpace(s.Project) == "" {
		return nil, fmt.Errorf("`project` is empty")
	}
	if strings.TrimSpace(s.Topic) == "" {
		return nil, fmt.Errorf("`topic` is empty")
	}
	t := topubsub.Topic{Project: s.Project, Name: s.Topic}

	// A YAML file cannot carry a function, so it carries the names of fields
	// and the function is built here. The client still decides the contract --
	// these are their own fields -- and the gateway invents no attribute of its
	// own, which is the correction that shaped this driver.
	if len(s.Attributes) > 0 {
		fields := append([]string(nil), s.Attributes...)
		t.Attributes = func(e sdk.Envelope) map[string]string {
			row, ok := e.Payload.(map[string]any)
			if !ok {
				return nil
			}
			attrs := make(map[string]string, len(fields))
			for _, f := range fields {
				// A field the event does not carry is ABSENT, not empty. An
				// empty attribute and a missing one mean different things to a
				// subscriber filtering on them.
				if v, present := row[f]; present {
					attrs[f] = gateway.Text(v)
				}
			}
			if len(attrs) == 0 {
				return nil
			}
			return attrs
		}
	}

	if s.OrderingKey != "" {
		field := s.OrderingKey
		t.OrderingKey = func(e sdk.Envelope) string {
			row, ok := e.Payload.(map[string]any)
			if !ok {
				return ""
			}
			return gateway.Text(row[field])
		}
	}

	return &sink{topic: t, name: fmt.Sprintf("pubsub:%s/%s", s.Project, s.Topic)}, nil
}

type sink struct {
	topic topubsub.Topic
	name  string
}

func (p *sink) Describe() string { return p.name }

func (p *sink) Write(ctx context.Context, batch []gateway.Envelope) (int64, error) {
	res, err := p.topic.Write(ctx, batch, sdk.WriteOptions{})
	if res == nil {
		return 0, err
	}
	// RowsLoaded on the error path too: a publish that failed halfway wrote
	// some, and reporting zero would have the operator re-send what already
	// landed.
	return res.RowsLoaded, err
}
