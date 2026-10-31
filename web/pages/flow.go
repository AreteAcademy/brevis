package pages

import (
	"context"
	"strings"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
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
// or "dead_letter"; Main is what it is, Detail how. Where either is words
// rather than the data's own -- the buffer's triggers, the dead letter --
// mainIn and detailIn write them in the reader's language when the page is
// drawn (screen-streams T3), since the flow is built before.
type FlowNode struct {
	Kind, Main, Detail string
	mainIn, detailIn   func(context.Context) string
}

// MainIn is what the step is, in the reader's language.
func (n FlowNode) MainIn(ctx context.Context) string {
	if n.mainIn != nil {
		return n.mainIn(ctx)
	}
	return n.Main
}

// DetailIn is how the step does it, in the reader's language.
func (n FlowNode) DetailIn(ctx context.Context) string {
	if n.detailIn != nil {
		return n.detailIn(ctx)
	}
	return n.Detail
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
			{Kind: "source", Main: driver,
				Detail: joinWords(" ", text("source.method"), text("source.url"))},
			{Kind: "destination", Main: target,
				Detail: joinWords(" · ", text("target.driver"), text("target.dedup"))},
		}}
	}

	sink := text("sink.type")
	if sink == "" {
		return nil
	}
	flow := &Flow{}
	source := FlowNode{Kind: "source", Detail: text("format")}
	switch {
	case text("path") != "":
		source.Main = "POST " + text("path")
	case text("name") != "":
		source.Main = "stream " + text("name")
	default:
		source.Main = "events"
	}
	flow.Nodes = append(flow.Nodes, source)

	// THE BUFFER, when its flush is configured: "every 2 minutes or 2,000
	// events", then any other trigger as the file names it. A switch
	// (`claim`) is not a trigger and says nothing here.
	// The ceilings a batch never passes -- its age and its size -- are said
	// as one "at most 6 minutes / 8MiB"; a trigger with no words here stays as
	// the file names it.
	every, records := text("buffer.flush.every"), text("buffer.flush.records")
	maxAge, maxSize := text("buffer.flush.max_age"), text("buffer.flush.size")
	var others []string
	for _, path := range order {
		key, ok := strings.CutPrefix(path, "buffer.flush.")
		if !ok || key == "every" || key == "records" || key == "max_age" || key == "size" {
			continue
		}
		if _, isBool := f[path].(bool); isBool {
			continue
		}
		others = append(others, key+" "+text(path))
	}
	if every != "" || records != "" || maxAge != "" || maxSize != "" || len(others) > 0 {
		flow.Nodes = append(flow.Nodes, FlowNode{Kind: "buffer", Main: joinWords(" ", text("buffer.type")),
			detailIn: func(ctx context.Context) string {
				var b string
				switch {
				case every != "" && records != "":
					b = i18n.T(ctx, "flow.buffer.both", "every", durationWords(ctx, every), "records", events(ctx, records))
				case every != "":
					b = i18n.T(ctx, "flow.buffer.every", "every", durationWords(ctx, every))
				case records != "":
					b = i18n.T(ctx, "flow.buffer.records", "records", events(ctx, records))
				}
				ceiling := ""
				if maxAge != "" || maxSize != "" {
					age := ""
					if maxAge != "" {
						age = durationWords(ctx, maxAge)
					}
					ceiling = i18n.T(ctx, "flow.buffer.max", "what", joinWords(" / ", age, maxSize))
				}
				return joinWords(" · ", append([]string{b, ceiling}, others...)...)
			}})
	}

	write := text("sink.write")
	if sink == "auto_table" {
		write = text("sink.into.write")
	}
	flow.Nodes = append(flow.Nodes, FlowNode{Kind: "destination", Main: target,
		Detail: joinWords(" · ", sink, write)})

	if text("dead_letter.type") != "" {
		flow.DeadLetter = &FlowNode{Kind: "dead_letter", Main: text("dead_letter.path"),
			mainIn:   func(ctx context.Context) string { return destination(ctx, text, "dead_letter", true) },
			detailIn: func(ctx context.Context) string { return i18n.T(ctx, "flow.refused") }}
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

// flowTitle is a node's heading, by its kind, in the reader's language.
func flowTitle(ctx context.Context, kind string) string {
	switch kind {
	case "source":
		return i18n.T(ctx, "flow.source")
	case "buffer":
		return i18n.T(ctx, "flow.buffer")
	case "dead_letter":
		return i18n.T(ctx, "flow.dead_letter")
	}
	return i18n.T(ctx, "flow.destination")
}
