// Package migrations embute o SQL de schema no binario.
//
// The embed lives here, and not in the postgres package, because `//go:embed`
// only reaches files in the package's own directory — it does not take `../`.
package migrations

import "embed"

// FS holds the versioned migrations, applied by goose.
//
//go:embed *.sql
var FS embed.FS
