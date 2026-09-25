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
