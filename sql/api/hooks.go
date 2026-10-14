package api

import api "github.com/AreteAcademy/brevis/sql/internal/api"

// ALIASES, NOT COPIES. The server loads and runs these types, so the ones a
// binary of your own registers are the same types the server calls -- no
// conversion between them, and no second definition to drift.

// Hooks is what a binary knows how to run, named by an endpoint's `hook:`.
type Hooks = api.Hooks

// Request is what a before hook sees of one call; only Query is kept.
type Request = api.Request

// Before runs before the parameters are read: nil goes on, Refuse answers
// 400, any other error answers 500 with the text kept for the audit line.
type Before = api.Before

// Result is what an after hook sees: the rows, already cut by max_rows.
type Result = api.Result

// After returns what becomes the answer's `data`. Its output is cached, so it
// must be a function of its Result.
type After = api.After

// NewHooks is an empty registry; the published binary is Main(NewHooks()).
func NewHooks() *Hooks { return api.NewHooks() }

// Refuse is how a before hook answers 400, with msg as written.
func Refuse(msg string) error { return api.Refuse(msg) }
