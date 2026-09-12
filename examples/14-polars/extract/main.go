// Step one: the Go SDK reads the CSVs and writes what Polars will aggregate.
//
// It does the part a dataframe library is bad at -- talking to a source,
// retrying it, counting what came out -- and stops exactly where a dataframe
// library gets good.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/AreteAcademy/brevis/sdk"
	bctx "github.com/AreteAcademy/brevis/sdk/context"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to"
)

func main() {
	ctx := context.Background()

	shared := os.Getenv("SHOP_WORKDIR")
	if shared == "" {
		log.Fatal("set SHOP_WORKDIR: the three steps hand work over through files, " +
			"so they need a directory all three can see")
	}
	staged := filepath.Join(shared, "orders")

	data, err := sdk.Extract(ctx, sdk.Source{
		// Format is explicit: the extension does not decide, and a .csv read
		// as NDJSON fails on the header line -- which is at least loud.
		From:    from.Files{Path: ordersCSV(), Format: sdk.FormatCSV},
		Preview: 3,
	})
	if err != nil {
		log.Fatalf("extract: %v", err)
	}

	// Accept, and nothing else. The reshaping belongs to the step that has a
	// dataframe -- doing half of it here would put the same logic in two
	// languages, which is the arrangement that drifts.
	data = sdk.Transform(data, sdk.Accept(
		"order_id", "client_id", "product_id", "quantity", "unit_price",
		"discount", "total", "status", "placed_at"))

	res, err := sdk.Load(ctx, data, sdk.Target{
		To: to.Files{Path: staged + "/"},
		Columns: []string{
			"order_id", "client_id", "product_id", "quantity", "unit_price",
			"discount", "total", "status", "placed_at"},
	})
	if err != nil {
		log.Fatalf("load: %v", err)
	}
	if len(res.Objects) == 0 {
		log.Fatal("nothing was written, so there is nothing for Polars to read")
	}

	// The HANDOFF. What the next step reads is a path, not the data: a
	// dataframe of two thousand rows has no business travelling through an
	// environment variable capped at 4 KB.
	//
	// Objects is what the driver ACTUALLY wrote, and it is read from the result
	// rather than rebuilt from the path above -- to.Files picks the file's name,
	// timestamp included, and guessing it here would be right until the day it
	// is not.
	if err := bctx.Set("path", res.Objects[0]); err != nil {
		log.Fatalf("publishing the path: %v", err)
	}

	fmt.Printf("\n%d order(s) staged at %s\n", res.Rows, res.Objects[0])
}

// ordersCSV finds the fixture whether this runs from examples/ or from the
// example's own directory. See 13-pubsub for why this is checked rather than
// assumed: from.Files with a path that does not exist reads zero records and
// reports no error at all.
func ordersCSV() string {
	for _, p := range []string{
		"testdata/shop/orders.csv",
		"../testdata/shop/orders.csv",
		"examples/testdata/shop/orders.csv",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	log.Fatal("orders.csv is nowhere; run this from examples/")
	return ""
}
