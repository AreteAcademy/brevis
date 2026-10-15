package pages

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

func kinds(f *Flow) string {
	var k []string
	for _, n := range f.Nodes {
		k = append(k, n.Kind)
	}
	if f.DeadLetter != nil {
		k = append(k, "+"+f.DeadLetter.Kind)
	}
	return strings.Join(k, " ")
}

// A GATEWAY STREAM IS DRAWN AS WHAT ITS CONFIGURATION DECLARES: events posted
// to its path, the buffer that batches them, the destination, and -- as a
// branch -- where refused batches go.
func TestAGatewayStreamFlowsFromItsPathToItsDestination(t *testing.T) {
	c := &postgres.WriterConfig{Fields: []catalog.ManifestField{
		{Path: "name", Value: "orders"},
		{Path: "path", Value: "/v1/orders"},
		{Path: "format", Value: "json"},
		{Path: "buffer.flush.every", Value: "2m"},
		{Path: "sink.type", Value: "bigquery"},
		{Path: "sink.write", Value: "append"},
		{Path: "dead_letter.type", Value: "files"},
		{Path: "dead_letter.path", Value: "./dead-letter/orders/"},
	}}
	f := FlowOf(c, "bigquery://acme/landing/orders")
	if f == nil {
		t.Fatal("a described gateway stream has no flow")
	}
	if got := kinds(f); got != "source buffer destination +dead_letter" {
		t.Fatalf("the flow is %q", got)
	}
	for _, want := range []struct{ node, text string }{
		{f.Nodes[0].Main, "POST /v1/orders"},
		{f.Nodes[0].Detail, "json"},
		{f.Nodes[1].Detail, "every 2m"},
		{f.Nodes[2].Main, "bigquery://acme/landing/orders"},
		{f.Nodes[2].Detail, "append"},
		{f.DeadLetter.Main, "./dead-letter/orders/"},
	} {
		if !strings.Contains(want.node, want.text) {
			t.Errorf("%q does not say %q", want.node, want.text)
		}
	}
}

// A PART THE CONFIGURATION DOES NOT DECLARE IS NOT DRAWN. No buffer settings,
// no buffer node; no dead letter, no branch. A diagram that showed them anyway
// would be inventing the pipeline it claims to describe.
func TestAGatewayStreamDrawsOnlyWhatItDeclares(t *testing.T) {
	c := &postgres.WriterConfig{Fields: []catalog.ManifestField{
		{Path: "path", Value: "/v1/clicks"},
		{Path: "sink.type", Value: "pubsub"},
	}}
	f := FlowOf(c, "pubsub://brevis-local/clicks")
	if got := kinds(f); got != "source destination" {
		t.Errorf("an undeclared buffer or dead letter was drawn: %q", got)
	}
}

// AN SDK PIPELINE IS ITS SOURCE AND ITS TARGET: the driver it reads with and
// where it reads from, then the destination and how it lands.
func TestAnSDKPipelineFlowsFromItsSourceToItsTarget(t *testing.T) {
	c := &postgres.WriterConfig{Fields: []catalog.ManifestField{
		{Path: "source.driver", Value: "from.HTTP"},
		{Path: "source.url", Value: "https://api.example.com/v1/items"},
		{Path: "target.driver", Value: "bigquery.Table"},
		{Path: "target.dedup", Value: "merge"},
	}}
	f := FlowOf(c, "bigquery://acme/landing/items")
	if got := kinds(f); got != "source destination" {
		t.Fatalf("the flow is %q", got)
	}
	if f.Nodes[0].Main != "from.HTTP" || !strings.Contains(f.Nodes[0].Detail, "api.example.com") {
		t.Errorf("the source is %+v", f.Nodes[0])
	}
	if f.Nodes[1].Main != "bigquery://acme/landing/items" || !strings.Contains(f.Nodes[1].Detail, "merge") {
		t.Errorf("the destination is %+v", f.Nodes[1])
	}
}

// A WRITER THAT DID NOT DESCRIBE ITSELF HAS NO DIAGRAM -- a Python, dbt or
// shell step lands without a configuration -- and neither does one whose
// configuration names no source the console can read.
func TestAWriterThatDidNotDescribeItselfHasNoFlow(t *testing.T) {
	if FlowOf(nil, "postgres://db/public/x") != nil {
		t.Error("an undescribed writer has a flow")
	}
	if FlowOf(&postgres.WriterConfig{Fields: []catalog.ManifestField{{Path: "x", Value: "y"}}}, "postgres://db/public/x") != nil {
		t.Error("a configuration with neither a sink nor a source has a flow")
	}
}
