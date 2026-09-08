package sdk

import (
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// RunContext is what the Brevis engine knows about this execution. See
// core.RunContext; it lives there because the driver interfaces take it, and
// a driver package cannot import the root.
type RunContext = core.RunContext

// Environment variables the engine sets, re-exported so a fetcher can read
// them without importing an internal package.
const (
	EnvRunID          = core.EnvRunID
	EnvRunFirst       = core.EnvRunFirst
	EnvRunAttempt     = core.EnvRunAttempt
	EnvRunTrigger     = core.EnvRunTrigger
	EnvRunLogicalDate = core.EnvRunLogicalDate
	EnvRunParams      = core.EnvRunParams
)

// ParamCreateTable is the dispatch parameter that asks for the table to be
// created on this run.
const ParamCreateTable = core.ParamCreateTable

// RunContextFromEnv reads what the engine injected into this process.
//
// Pipeline.Run already carries it, so a fetcher built with sdk.Run never calls
// this. It is exported for the OTHER kind of step: a Go program that is a
// workflow step without being a pipeline -- a report writer, a cleanup, a
// notifier -- which otherwise had to read BREVIS_AUTO_ADJUSTED_AT out of the
// environment by hand and parse it. That is exactly the arithmetic the auto
// params exist to remove.
//
// Outside the engine everything comes back zeroed, and Auto.Now() is the wall
// clock.
func RunContextFromEnv() RunContext { return core.RunContextFromEnv() }
