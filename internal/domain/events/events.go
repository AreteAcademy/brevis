// Package events names what can happen to an installation, for the console's
// events feed: the closed list of kinds, and which source each belongs to.
package events

// Kind is what happened, as stored in events.kind: `<thing>.<what>`.
type Kind string

const (
	WorkflowPublished Kind = "workflow.published"
	WorkflowRemoved   Kind = "workflow.removed"
	SchedulePaused    Kind = "schedule.paused"
	ScheduleResumed   Kind = "schedule.resumed"

	// The run kinds are not stored: the feed reads them from runs.
	RunStarted   Kind = "run.started"
	RunSucceeded Kind = "run.succeeded"
	RunFailed    Kind = "run.failed"
	RunCanceled  Kind = "run.canceled"

	StreamPublished    Kind = "stream.published"
	GatewayUnpublished Kind = "gateway.unpublished"

	EndpointsPublished   Kind = "endpoints.published"
	EndpointsUnpublished Kind = "endpoints.unpublished"
)

// Source is where an event comes from, which is what the feed filters by.
type Source string

const (
	FromWorkflow Source = "workflow"
	FromStream   Source = "stream"
	FromEndpoint Source = "endpoint"
)

var sources = map[Kind]Source{
	WorkflowPublished: FromWorkflow,
	WorkflowRemoved:   FromWorkflow,
	SchedulePaused:    FromWorkflow,
	ScheduleResumed:   FromWorkflow,
	RunStarted:        FromWorkflow,
	RunSucceeded:      FromWorkflow,
	RunFailed:         FromWorkflow,
	RunCanceled:       FromWorkflow,

	StreamPublished:    FromStream,
	GatewayUnpublished: FromStream,

	EndpointsPublished:   FromEndpoint,
	EndpointsUnpublished: FromEndpoint,
}

// Kinds is every kind this version knows, in a fixed order.
var Kinds = []Kind{
	WorkflowPublished, WorkflowRemoved, SchedulePaused, ScheduleResumed,
	RunStarted, RunSucceeded, RunFailed, RunCanceled,
	StreamPublished, GatewayUnpublished,
	EndpointsPublished, EndpointsUnpublished,
}

// SourceOf is the source of k. A kind this version does not know -- written
// by a newer one -- has none, and a reader skips it rather than guess.
func SourceOf(k Kind) (Source, bool) {
	s, ok := sources[k]
	return s, ok
}
