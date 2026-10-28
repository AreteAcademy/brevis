package events

import "testing"

// EVERY KIND HAS A SOURCE, which is the tab's filter: what a workflow did,
// what came through a stream, what an endpoint serves. A run is a workflow's.
func TestEveryKindBelongsToASource(t *testing.T) {
	want := map[Kind]Source{
		WorkflowPublished:    FromWorkflow,
		WorkflowRemoved:      FromWorkflow,
		SchedulePaused:       FromWorkflow,
		ScheduleResumed:      FromWorkflow,
		RunStarted:           FromWorkflow,
		RunSucceeded:         FromWorkflow,
		RunFailed:            FromWorkflow,
		RunCanceled:          FromWorkflow,
		StreamPublished:      FromStream,
		GatewayUnpublished:   FromStream,
		EndpointsPublished:   FromEndpoint,
		EndpointsUnpublished: FromEndpoint,
	}
	if len(Kinds) != len(want) {
		t.Errorf("Kinds has %d, the table %d: a kind was added without a source, or the reverse", len(Kinds), len(want))
	}
	for _, k := range Kinds {
		if got, ok := SourceOf(k); !ok || got != want[k] {
			t.Errorf("SourceOf(%s) = %q, %v; want %q", k, got, ok, want[k])
		}
	}
}

// A KIND THIS VERSION DOES NOT KNOW -- written by a newer one, or by hand --
// has no source, so a reader can skip it rather than draw it wrong.
func TestAnUnknownKindHasNoSource(t *testing.T) {
	if s, ok := SourceOf("cache.generated"); ok {
		t.Errorf("SourceOf(an unknown kind) = %q; want none", s)
	}
}
