// Package sdk_test carries the documentation's examples.
//
// They live here, and not inside a comment, because an example in a comment does
// not compile -- and the two that lived in sdk.go and pipeline.go went on
// documenting four fields of Target and two of Source that had stopped existing.
// Anyone who copied them got code that does not build.
//
// It is `package sdk_test`, external on purpose: that way an example is written
// with the same `sdk.` a consumer writes, and only sees what is exported.
package sdk_test

import (
	"context"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/pycompat"
	"github.com/AreteAcademy/brevis/sdk/to"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

// A whole fetcher in two calls: where it comes from, and where it goes.
func Example() {
	ctx := context.Background()

	dados, err := sdk.Extract(ctx, sdk.Source{
		From: from.HTTP{
			URL: "https://api.open-meteo.com/v1/forecast?latitude=-23.5&longitude=-46.6&hourly=temperature_2m",
			Records: func(r sdk.Response) ([]any, error) {
				doc, err := r.Object()
				if err != nil {
					return nil, err
				}
				return sdk.ParallelArrays("hourly", "time", "temperature_2m")(doc)
			},
		},
	})
	if err != nil {
		return
	}

	// What we take from the source.
	dados = sdk.Transform(dados, sdk.Accept("time", "temperature_2m"))

	// Where it goes, and with which columns.
	if _, err := sdk.Load(ctx, dados, sdk.Target{
		To:      to.Files{Path: "./landing/temperatura/"},
		Columns: []string{"time", "temperature_2m"},
	}); err != nil {
		return
	}
}

// Pipeline is the same fetcher as a value: Run handles the flags, the -dry-run,
// the logging and the exit code.
func ExamplePipeline() {
	sdk.Run(sdk.Pipeline{
		Source: sdk.Source{
			From: from.HTTP{
				URL: "https://api.exemplo.com/eventos",
				Records: func(r sdk.Response) ([]any, error) {
					var eventos []any
					return eventos, r.JSON(&eventos)
				},
			},
		},
		Transform: []sdk.Transformer{
			sdk.Without("generationtime_ms"),
			sdk.IngestionID(),
			sdk.IngestionLoadedAt(),
		},
		Target: sdk.Target{
			To:      to.Files{Path: "./landing/eventos/"},
			Columns: []string{"id", "created_at", "ingestion_id", "ingestion_loaded_at"},
		},
	})
}

// Key composes the source_key by joining payload fields, in the order given.
//
// It produces an sdk.KeySelector, and ComputeText is what writes one into a
// column.
func ExampleKey() {
	_ = sdk.ComputeText("source_key", sdk.Key("latitude", "longitude", "time"))
}

// KeyWith is Key with the rendering injected, for when the key has to match one
// from a system that has already written rows.
func ExampleKeyWith() {
	_ = sdk.ComputeText("source_key", sdk.KeyWith(pycompat.Text, "provider", "id"))
}

// Field reads one payload field as the record's timestamp.
func ExampleField() {
	_ = sdk.ComputeText("record_ts", sdk.Field("time"))
}

// IngestionID writes the ingestion_id column from the four provenance columns,
// which have to exist before it in the chain.
func ExampleIngestionID() {
	_ = []sdk.Transformer{
		sdk.Compute("provider", func(map[string]any) (any, error) { return "open_meteo", nil }),
		sdk.Compute("entity", func(map[string]any) (any, error) { return "hourly_temperature", nil }),
		sdk.ComputeText("source_key", sdk.Key("latitude", "longitude", "time")),
		sdk.ComputeText("record_ts", sdk.Field("time")),
		sdk.IngestionID(),
	}
}

// RejectIf refuses a 200 whose body carries {"error": true}.
func ExampleRejectIf() {
	_ = from.HTTP{
		URL: "https://api.exemplo.com/eventos",
		Records: func(r sdk.Response) ([]any, error) {
			if err := sdk.RejectIf("error")(r); err != nil {
				return nil, err
			}
			doc, err := r.Object()
			if err != nil {
				return nil, err
			}
			return sdk.ParallelArrays("hourly", "time", "temperature_2m")(doc)
		},
	}
}

// Bool serves the three-state options, where nil ("not said") has to be
// distinguishable from false.
func ExampleBool() {
	_ = bigquery.Table{
		Dataset:     "landing",
		Name:        "temperatura",
		CreateTable: sdk.Bool(false), // nunca, nem numa primeira execucao
	}
}

// Source is the origin: the driver in From, plus what is true of all of them.
func ExampleSource() {
	_ = sdk.Source{
		From:    from.HTTP{URL: "https://api.exemplo.com/v1/eventos"},
		Preview: 5,
	}
}

// Target is the destination: the driver in To, plus the declared columns.
func ExampleTarget() {
	_ = sdk.Target{
		To:      bigquery.Table{Dataset: "bronze", Name: "pedidos"},
		Columns: []string{"ingestion_id", "ingestion_loaded_at", "payload"},
	}
}
