// Command gateway is the published gateway: no hooks.
//
// Which is the honest artifact for a compiled-hook design, and the limit is
// worth stating where somebody meets it. A hook is Go, compiled in, so an image
// built here can only carry hooks that were compiled here -- and none were.
// This image serves streams that declare no `hook:`, which is every stream that
// lands what it was sent.
//
// A hook of your own means a binary of your own, and it is ten lines:
//
//	import "github.com/AreteAcademy/brevis/gateway"
//
//	func main() {
//	    hooks := gateway.NewHooks()
//	    hooks.MustRegister("enrich", enrich)
//	    gateway.Main(hooks)
//	}
//
// That is the same arrangement the SDK asks for -- the consumer compiles their
// own binary with their own code -- and it is why both products have one idiom
// rather than two. `gateway/example` is a working one.
package main

import "github.com/AreteAcademy/brevis/gateway"

func main() { gateway.Main(nil) }
