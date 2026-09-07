package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	frommy "github.com/AreteAcademy/brevis/sdk/from/mysql"
	tomy "github.com/AreteAcademy/brevis/sdk/to/mysql"

	"github.com/AreteAcademy/brevis/sdk"
)

// BenchmarkMySQLLoad measures the load against a real server, so the
// documentation does not promise performance nobody measured.
//
//	BREVIS_IT_MYSQL_DSN=... go test -run XXX -bench CargaMySQL ./to/mysql/
func BenchmarkMySQLLoad(b *testing.B) {
	d := os.Getenv("BREVIS_IT_MYSQL_DSN")
	if d == "" {
		b.Skip("BREVIS_IT_MYSQL_DSN não definida")
	}

	db, err := sql.Open("mysql", frommy.WithParseTime(d))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })

	name := fmt.Sprintf("bench_%d", time.Now().UnixNano())
	if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE %s (
		ingestion_id VARCHAR(36) NOT NULL, ingestion_loaded_at DATETIME(6) NOT NULL,
		provider VARCHAR(64), source_key VARCHAR(64), valor DECIMAL(18,2))`, name)); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + name) })

	const lines = 10000
	lote := make([]sdk.Envelope, lines)
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range lote {
		lote[i] = sdk.Envelope{Payload: map[string]any{
			"ingestion_id":        fmt.Sprintf("id-%06d", i),
			"ingestion_loaded_at": now,
			"provider":            "bench",
			"source_key":          fmt.Sprintf("k%d", i),
			"valor":               "10.50",
		}}
	}

	target := tomy.Table{DSN: d, Name: name}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := target.Write(context.Background(), lote, sdk.WriteOptions{}); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(lines*b.N)/b.Elapsed().Seconds(), "linhas/s")
}
