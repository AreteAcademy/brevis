package sdk

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/from"
)

// BenchmarkExtractTransform mede o caminho quente inteiro: decodificar a
// response, apply the transformers and compose the ingestion_id -- which is
// where a
// fetcher passa o tempo dele.
func BenchmarkExtractTransform(b *testing.B) {
	var corpo strings.Builder
	corpo.WriteString(`{"results":[`)
	for i := 0; i < 5000; i++ {
		if i > 0 {
			corpo.WriteString(",")
		}
		fmt.Fprintf(&corpo, `{"id":%d,"nome":"registro %d","valor":%d.75,"ts":"2026-09-05T12:00:00Z"}`, i, i, i)
	}
	corpo.WriteString(`]}`)
	payload := corpo.String()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	// The SDK itself logs on every extract, and the log's io would enter the
	// measurement.
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previous)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, err := Extract(context.Background(), Source{
			From: from.HTTP{URL: srv.URL, DataKey: "results"},
		})
		if err != nil {
			b.Fatal(err)
		}
		data = Transform(data,
			Accept("id", "nome", "valor", "ts"),
			Compute("provider", func(map[string]any) (any, error) { return "bench", nil }),
			Compute("entity", func(map[string]any) (any, error) { return "registros", nil }),
			Rename(map[string]string{"id": "source_key", "ts": "record_ts"}),
			IngestionID(),
			IngestionLoadedAt(),
		)
		n := 0
		for _, err := range data.Records {
			if err != nil {
				b.Fatal(err)
			}
			n++
		}
		if n != 5000 {
			b.Fatalf("linhas = %d", n)
		}
	}
}
