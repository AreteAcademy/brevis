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
