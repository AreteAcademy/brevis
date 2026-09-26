package gateway

import (
	"context"

	"github.com/AreteAcademy/brevis/sdk"
)

// Envelope is what a sink receives, re-exported so an implementation of Sinker
// does not have to import the SDK to name the type it is handed.
type Envelope = sdk.Envelope

// Sinker is one destination, and it is the SDK's own Writer narrowed to what
// this needs.
//
// Narrowed rather than reused whole so a test can supply one without a cloud
// account, and so the gateway never grows a second idea of what a destination
// is: every implementation shipped here IS an sdk driver.
type Sinker interface {
	Write(ctx context.Context, batch []sdk.Envelope) (int64, error)
	Describe() string
}

// Admitter is a sink that can refuse ONE event before it is buffered.
//
// Optional, and satisfied by structural typing: a sink that does not implement
// it admits everything, which is what every sink did before this existed.
//
// It is the cure for the poison batch. A destination that can refuse an
// individual event -- `auto_table` refuses a table name outside its rules --
// would otherwise refuse it at WRITE time, which fails the whole batch: one
// malformed event from one client buries the events of every other client in
// the same flush window, and none of them is told.
//
// Here the refusal lands where per-event refusals already land: the event is
// rejected with its reason in the response, counted under its own label, and
// every well-formed event in the same request still lands.
type Admitter interface {
	Admit(event map[string]any) error
}

// Measurer is a sink that wants to know how many bytes each event arrived as.
//
// Optional, like Admitter, and for the same reason: most sinks must not get
// this. A stream that lands in a topic or in a table somebody declared would
// have a field appear in its payload that nothing asked for -- a new key for
// every subscriber, or a column the table does not have and the load refuses.
// Only a sink that OWNS its table's shape can carry it, which today is
// `auto_table`.
//
// The pipe calls this after the hook and after the oversize path, so the
// number is final, and it OVERWRITES whatever is on the event: the field name
// is the gateway's own, and a producer who sends it must not be able to forge
// their own volume.
//
// The count is what ARRIVED on the wire, envelope included. It is not what the
// event weighs in memory, not what the destination stores, and not what a hook
// may have turned it into -- see Flush.Size, which is measured the same way and
// for the same reason.
//
// It returns what this event's volume is ATTRIBUTED to: the table, for
// auto_table. The pipe owns the metrics and the sink owns the routing, so the
// name has to travel back rather than the metrics travelling in. Empty means
// "do not attribute", and the volume metrics are skipped for that event.
type Measurer interface {
	Measure(event map[string]any, bytes int) string
}
