package pages

import (
	"strings"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// Flow is how data reaches a destination, as one writer's configuration
// declares it: a row of nodes from source to destination and, for a gateway
// stream, a branch where refused batches go.
//
// DRAWN ONLY FROM WHAT IS DECLARED. Every label below is a configuration
// field or the destination itself; a part the configuration does not mention
// -- a buffer, a dead letter -- is not drawn. A diagram that showed them
// anyway would be inventing the pipeline it claims to describe.
type Flow struct {
	Nodes      []FlowNode
	DeadLetter *FlowNode
}

// FlowNode is one step of a Flow. Kind is "source", "buffer", "destination"
// or "dead_letter"; Main is what it is, Detail how.
type FlowNode struct {
	Kind, Title, Main, Detail string
}

// FlowOf reads a writer's configuration into a Flow, or nil when it has none
// to draw: a writer that did not describe itself (a Python, dbt or shell
// step), or a configuration that names neither a gateway's sink nor an SDK
// pipeline's source.
//
// The two shapes are the ones Summary already tells apart: a gateway stream
// (`sink.type`) and an SDK pipeline (`source.driver`).
func FlowOf(c *postgres.WriterConfig, target string) *Flow {
	if c == nil {
		return nil
	}
	f := map[string]any{}
	var order []string
	for _, fl := range c.Fields {
		f[fl.Path] = fl.Value
		order = append(order, fl.Path)
	}
	text := func(path string) string {
		if v, ok := f[path]; ok && v != nil {
			return valueText(v)
		}
		return ""
	}

	if driver := text("source.driver"); driver != "" {
		return &Flow{Nodes: []FlowNode{
			{Kind: "source", Title: "Source", Main: driver,
				Detail: joinWords(" ", text("source.method"), text("source.url"))},
			{Kind: "destination", Title: "Destination", Main: target,
				Detail: joinWords(" · ", text("target.driver"), text("target.dedup"))},
		}}
	}

	sink := text("sink.type")
	if sink == "" {
		return nil
	}
	flow := &Flow{}
	source := FlowNode{Kind: "source", Title: "Source", Detail: text("format")}
	switch {
	case text("path") != "":
		source.Main = "POST " + text("path")
	case text("name") != "":
		source.Main = "stream " + text("name")
	default:
		source.Main = "events"
	}
	flow.Nodes = append(flow.Nodes, source)

	// THE BUFFER, when its flush is configured: each trigger in the order the
	// file declares it, "every 2m or 2000 events". A switch (`claim`) is not
	// a trigger and says nothing here.
	var triggers []string
	for _, path := range order {
		key, ok := strings.CutPrefix(path, "buffer.flush.")
		if !ok {
			continue
		}
		if _, isBool := f[path].(bool); isBool {
			continue
		}
		if key == "every" {
			triggers = append(triggers, "every "+text(path))
		} else {
			triggers = append(triggers, text(path)+" "+key)
		}
	}
	if len(triggers) > 0 {
		flow.Nodes = append(flow.Nodes, FlowNode{Kind: "buffer", Title: "Buffer",
			Main: joinWords(" ", text("buffer.type")), Detail: strings.Join(triggers, " or ")})
	}

	write := text("sink.write")
	if sink == "auto_table" {
		write = text("sink.into.write")
	}
	flow.Nodes = append(flow.Nodes, FlowNode{Kind: "destination", Title: "Destination", Main: target,
		Detail: joinWords(" · ", sink, write)})

	if text("dead_letter.type") != "" {
		flow.DeadLetter = &FlowNode{Kind: "dead_letter", Title: "Dead letter",
			Main: destination(text, "dead_letter", true), Detail: "refused batches"}
	}
	return flow
}

// joinWords joins the parts that are not empty.
func joinWords(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// flowIcon is a node's glyph.
func flowIcon(kind string) string {
	switch kind {
	case "source":
		return "plug"
	case "buffer":
		return "rows-3"
	case "dead_letter":
		return "triangle-alert"
	}
	return "database"
}

// flowBorder marks the destination: the node the page is about. In ink, not
// the accent, which is the screen's one primary action.
func flowBorder(kind string) string {
	if kind == "destination" {
		return "border-ink-2"
	}
	return "border-line"
}
