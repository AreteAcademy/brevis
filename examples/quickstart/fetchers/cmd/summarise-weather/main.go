// summarise-weather reads the CSV the previous step landed and folds it into
// one row per day.
//
// Two things worth noticing. It never talks to the vendor: the raw extract is
// already on disk, so this step can be retried on its own without spending the
// API's quota. And it finds the input without anyone passing it a path --
// BREVIS_RUN_ID is injected into every step, so a run-scoped prefix is a token
// both steps can derive.
package main

import (
	"fmt"
	"os"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
	"github.com/AreteAcademy/brevis/sdk/to"
)

func main() {
	sdk.Run(sdk.Pipeline{
		Name: "weather-daily",

		Source: sdk.Source{
			From: from.Files{Path: inputGlob(), Format: sdk.FormatCSV},
		},

		// The order is the point of Stages.
		//
		// Identity is derived from the row that LANDS, and after an aggregation
		// that row does not exist until the fold is done. So the stage that
		// computes it runs LAST -- and an Aggregate that received records
		// already carrying ingestion_id would be refused, naming it.
		Stages: []sdk.Stage{
			sdk.Map(
				// Keep only what the fold reads -- which also DROPS the
				// identity the previous step stamped on each hourly row.
				//
				// That is not tidiness: an Aggregate that receives records
				// already carrying ingestion_id is refused, naming it, because
				// the identity of an hourly reading says nothing about the
				// daily row that replaces it.
				sdk.Accept("time", "temperature_2m", "relative_humidity_2m",
					"latitude", "longitude"),

				sdk.Compute("day", func(r map[string]any) (any, error) {
					t, _ := r["time"].(string)
					if len(t) < 10 {
						return nil, fmt.Errorf("time %q is not a timestamp", t)
					}
					return t[:10], nil
				}),
			),

			sdk.Aggregate(sdk.Reduce{
				By: sdk.GroupBy("latitude", "longitude", "day"),
				Agg: map[string]sdk.Aggregator{
					// The CSV hands everything over as text, and a text that is
					// a number IS a number.
					"hours":        sdk.Count(),
					"temp_mean":    sdk.Mean("temperature_2m"),
					"temp_min":     sdk.Min("temperature_2m"),
					"temp_max":     sdk.Max("temperature_2m"),
					"humidity_max": sdk.Max("relative_humidity_2m"),
					// The hour of the day's peak: the value from the row where
					// the key is largest, without keeping the rows to pick later.
					"peak_hour": sdk.MaxBy("time", "temperature_2m"),
				},
			}),

			sdk.Map(
				sdk.Compute("provider", func(map[string]any) (any, error) { return "open_meteo", nil }),
				sdk.Compute("entity", func(map[string]any) (any, error) { return "daily_weather", nil }),
				sdk.Compute("source_key", func(r map[string]any) (any, error) {
					return sdk.Key("latitude", "longitude", "day")(r)
				}),
				sdk.Compute("record_ts", func(r map[string]any) (any, error) {
					return sdk.Field("day")(r)
				}),
				sdk.IngestionID(),
				sdk.IngestionLoadedAt(),
			),
		},

		Target: sdk.Target{
			To: to.Files{Path: outputDir(), Format: sdk.FormatCSV},
		},
	})
}

func inputGlob() string { return runDir() + "raw/*.csv" }
func outputDir() string { return runDir() + "daily/" }

func runDir() string {
	base := env("OUT_DIR", "/data")
	if run := os.Getenv(sdk.EnvRunID); run != "" {
		return fmt.Sprintf("%s/%s/", base, run)
	}
	return base + "/manual/"
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
