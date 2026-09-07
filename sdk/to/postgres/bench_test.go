package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	topg "github.com/AreteAcademy/brevis/sdk/to/postgres"
)

// BenchmarkCargaPostgres mede a carga contra o servidor de verdade.
//
// It exists because §5 of phase 5 asks for a number: without one the
// documentation promises performance nobody measured, which is how
// `DeleteAfterLoad` reached the text with a default it did not have.
//
//	BREVIS_IT_PG_DSN=... go test -run XXX -bench CargaPostgres ./to/postgres/
func BenchmarkCargaPostgres(b *testing.B) {
	d := ""
	if d = envOuPular(b); d == "" {
		return
	}
	conn := conectarBench(b, d)
	nome := tabelaBench(b, conn)

	const rows = 10000
	lote := make([]sdk.Envelope, rows)
	agora := time.Now().UTC().Format(time.RFC3339)
	for i := range lote {
		lote[i] = sdk.Envelope{Payload: map[string]any{
			"ingestion_id":        fmt.Sprintf("id-%06d", i),
			"ingestion_loaded_at": agora,
			"provider":            "bench",
			"source_key":          fmt.Sprintf("k%d", i),
			"valor":               "10.50",
		}}
	}

	destino := topg.Table{DSN: d, Name: nome}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := destino.Write(context.Background(), lote, sdk.WriteOptions{}); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(rows*b.N)/b.Elapsed().Seconds(), "rows/s")
}
