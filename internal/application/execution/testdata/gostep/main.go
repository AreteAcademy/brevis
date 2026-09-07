// A Go step that speaks the same contract as the Python one beside it.
//
// It reads what a shell step published, publishes with two Set calls, and
// exists so the cross-language test has a Go end that is a REAL binary rather
// than the engine calling its own package.
package main

import (
	"fmt"
	"os"

	bctx "github.com/AreteAcademy/brevis/sdk/context"
)

func main() {
	bucket, err := bctx.String("extract.bucket")
	if err != nil {
		fmt.Fprintln(os.Stderr, "go step:", err)
		os.Exit(1)
	}
	fmt.Println("go read:", bucket)

	if err := bctx.Set("checked_by", "go"); err != nil {
		fmt.Fprintln(os.Stderr, "go step:", err)
		os.Exit(1)
	}
	if err := bctx.Set("rows", 7); err != nil {
		fmt.Fprintln(os.Stderr, "go step:", err)
		os.Exit(1)
	}
}
