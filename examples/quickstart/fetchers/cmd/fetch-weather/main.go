// fetch-weather pulls an hourly forecast from Open-Meteo and lands it as CSV.
//
// It is a whole fetcher in one file, which is the point: everything that is not
// specific to this vendor -- retry, decoding, provenance, the identity of a row,
// the line of log at the end -- belongs to the SDK.
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
		Name: "weather",

		Source: sdk.Source{
			From: from.HTTP{
				URL: env("WEATHER_URL",
					"https://api.open-meteo.com/v1/forecast"+
						"?latitude=-23.55&longitude=-46.63"+
						"&hourly=temperature_2m,relative_humidity_2m&past_days=2"),
				Records: func(r sdk.Response) ([]any, error) {
					doc, err := r.Object()
					if err != nil {
						return nil, err
					}
					// Plenty of APIs answer 200 with an error in the body. Left
					// unchecked, that document lands as if it were data.
					if bad, _ := doc["error"].(bool); bad {
						return nil, sdk.Reject("open-meteo refused: %v", doc["reason"])
					}
					// The hourly block is three arrays paired by index. Anything
					// outside it -- latitude, longitude -- describes the whole
					// series and is copied onto every record.
					return sdk.ParallelArrays("hourly",
						"time", "temperature_2m", "relative_humidity_2m")(doc)
				},
			},
			Preview: 3,
		},

		Transform: []sdk.Transformer{
			sdk.Accept("time", "temperature_2m", "relative_humidity_2m", "latitude", "longitude"),

			// Provenance, and then the identity derived from it. The order
			// matters: IngestionID reads these four columns.
			sdk.Compute("provider", func(map[string]any) (any, error) { return "open_meteo", nil }),
			sdk.Compute("entity", func(map[string]any) (any, error) { return "hourly_weather", nil }),
			sdk.Compute("source_key", func(r map[string]any) (any, error) {
				return sdk.Key("latitude", "longitude", "time")(r)
			}),
			sdk.Compute("record_ts", func(r map[string]any) (any, error) {
				return sdk.Field("time")(r)
			}),
			sdk.IngestionID(),
			sdk.IngestionLoadedAt(),
		},

		Target: sdk.Target{
			To: to.Files{Path: outputDir(), Format: sdk.FormatCSV},

			// Declaring the columns is what turns "the destination changed"
			// into an error naming the column, instead of a load that quietly
			// drops a field.
			Columns: []string{
				"time", "temperature_2m", "relative_humidity_2m",
				"latitude", "longitude",
				"provider", "entity", "source_key", "record_ts",
				"ingestion_id", "ingestion_loaded_at",
			},
		},
	})
}

// outputDir puts every run in its own directory.
//
// BREVIS_RUN_ID is injected into every step by the engine, so it is the token
// the next step can derive without anyone passing anything: this step writes
// under it, the next one reads under it.
func outputDir() string {
	base := env("OUT_DIR", "/data")
	if run := os.Getenv(sdk.EnvRunID); run != "" {
		return fmt.Sprintf("%s/%s/raw/", base, run)
	}
	return base + "/manual/raw/"
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
