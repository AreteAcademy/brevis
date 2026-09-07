// Command 11-files reads files and writes files, with no cloud at all.
//
// It runs on the first try:
//
//	go run ./11-files
//
// The same pipeline serves S3 and GCS by changing one line -- the path's scheme
// says the backend, and the Store is passed in rather than chosen inside the
// driver:
//
//	from.Files{Path: "s3://bucket/day=1/*.ndjson", Store: s3.New(client)}
//	to.Files{Path: "gs://bucket/landing/", Store: gcs.New(client)}
//
// That is what makes this program compile not one line of AWS or Google.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to"
)

func main() {
	dir, err := os.MkdirTemp("", "brevis-arquivos-*")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	inDir := filepath.Join(dir, "entrada")
	outDir := filepath.Join(dir, "saida")
	if err := os.MkdirAll(inDir, 0o750); err != nil {
		log.Fatal(err)
	}

	// Two "extractions" that already existed on disk.
	seed(inDir, "2026-09-03.ndjson", `{"sku":"W-1","quantidade":3,"lixo":"x"}
{"sku":"W-2","quantidade":9,"lixo":"y"}`)
	seed(inDir, "2026-09-04.ndjson", `{"sku":"W-3","quantidade":1,"lixo":"z"}`)

	ctx := context.Background()

	// Where it comes from. The files are always read in order: a positional Key
	// depends on that, and with no order the ingestion_id would change between
	// runs.
	data, err := sdk.Extract(ctx, sdk.Source{
		From:    from.Files{Path: filepath.Join(inDir, "*.ndjson")},
		Preview: 5,
	})
	if err != nil {
		log.Fatalf("extract: %v", err)
	}

	// What row it builds. "lixo" is not declared, so it does not come out.
	data = sdk.Transform(data, sdk.Accept("sku", "quantidade"))

	// Where it goes, and with which columns.
	res, err := sdk.Load(ctx, data, sdk.Target{
		To: to.Files{
			Path:        outDir + "/",
			PartitionBy: "ingestion_loaded_at",
			Compress:    true,
		},
		Columns: []string{"ingestion_id", "ingestion_loaded_at", "sku", "quantidade"},
	})
	if err != nil {
		log.Fatalf("load: %v", err)
	}

	fmt.Println(res)
	fmt.Println("\nescrito em", outDir)
	_ = filepath.Walk(outDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			fmt.Printf("  %s  (%d bytes)\n", p[len(outDir)+1:], info.Size())
		}
		return nil
	})
}

func seed(dir, name, content string) {
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content+"\n"), 0o600); err != nil {
		log.Fatal(err)
	}
}
