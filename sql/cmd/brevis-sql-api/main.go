// brevis-sql-api: declared, read-only SQL served over HTTP.
//
// THE PUBLISHED BINARY, AND IT HAS NO HOOKS. A hook is Go compiled in, so an
// image built here can only carry hooks compiled here -- and none are. It
// serves files that declare no `hook:` and refuses the ones that do, naming
// sql/example, which is how a binary with hooks is made: these same lines,
// with a registration between them.
package main

import "github.com/AreteAcademy/brevis/sql/api"

func main() { api.Main(api.NewHooks()) }
