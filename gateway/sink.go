package gateway

import (
	"context"
	"fmt"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/to/pubsub"
)

// Sinker is one destination, and it is the SDK's own Writer narrowed to what
// this needs.
//
// Narrowed rather than reused whole so a test can supply one without a cloud
// account, and so the gateway never grows a second idea of what a destination
// is: every implementation here IS an sdk driver.
type Sinker interface {
	Write(ctx context.Context, batch []sdk.Envelope) (int64, error)
	Describe() string
}

// pubsubSink publishes to a topic somebody else owns.
//
// The driver does the work; this is the translation from a YAML file to the
// two functions it takes. Nothing about the payload is touched: what a
// subscriber receives is the event as it arrived, after the hook, and the
// attributes the file named -- which is the rule that driver was built on.
type pubsubSink struct {
	topic pubsub.Topic
	name  string
}

func newPubSubSink(s Sink) *pubsubSink {
	t := pubsub.Topic{Project: s.Project, Name: s.Topic}

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
					attrs[f] = text(v)
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
			return text(row[field])
		}
	}

	return &pubsubSink{topic: t, name: fmt.Sprintf("pubsub:%s/%s", s.Project, s.Topic)}
}

func (p *pubsubSink) Describe() string { return p.name }

func (p *pubsubSink) Write(ctx context.Context, batch []sdk.Envelope) (int64, error) {
	res, err := p.topic.Write(ctx, batch, sdk.WriteOptions{})
	if res == nil {
		return 0, err
	}
	// RowsLoaded on the error path too: a publish that failed halfway wrote
	// some, and reporting zero would have the operator re-send what already
	// landed.
	return res.RowsLoaded, err
}

// build resolves a stream's sink. It is the only place a type name becomes an
// implementation, so an unknown one cannot reach the request path.
func build(s Sink) (Sinker, error) {
	switch s.Type {
	case SinkPubSub:
		return newPubSubSink(s), nil
	default:
		return nil, fmt.Errorf("sink type %q", s.Type)
	}
}
