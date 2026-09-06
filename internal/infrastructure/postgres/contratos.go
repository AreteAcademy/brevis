package postgres

import (
	app "github.com/AreteAcademy/brevis/internal/application/execution"
)

// What the runner expects of this repository, checked at compile time.
//
// Without this, a signature that changes on one side only shows up when somebody
// assembles both — which today happens nowhere in the code, because the
// dispatcher -> Runner link does not exist yet.
var (
	_ app.Historico   = (*RunRepo)(nil)
	_ app.Persistidor = (*RunRepo)(nil)
)
