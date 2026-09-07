package sdk

import (
	"fmt"
	"testing"
)

// typicalChain is the chain a real fetcher writes: clean up, rename, stamp
// provenance and compose the ingestion_id.
func typicalChain() []Transformer {
	return []Transformer{
		Accept("id", "nome", "valor", "ts"),
		Rename(map[string]string{"id": "source_key", "ts": "record_ts"}),
		Compute("provider", func(map[string]any) (any, error) { return "bench", nil }),
		Compute("entity", func(map[string]any) (any, error) { return "registros", nil }),
		IngestionID(),
		IngestionLoadedAt(),
	}
}

func registro(i int) map[string]any {
	return map[string]any{
		"id":    fmt.Sprint(i),
		"nome":  "registro qualquer",
		"valor": 10.5,
		"ts":    "2026-09-05T12:00:00Z",
	}
}

// BenchmarkTransformChain measures the chain alone, with no HTTP and no
// decoding -- the previous benchmark mixed all three, and the profile needed a
// second look to tell which cost belonged to what.
func BenchmarkTransformChain(b *testing.B) {
	fns := typicalChain()
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		payload, _, err := applyAll(fns, registro(i))
		if err != nil {
			b.Fatal(err)
		}
		if payload == nil {
			b.Fatal("payload vazio")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "registros/s")
}

// BenchmarkOneTransformerAlone isolates the cost of ONE map copy, so the
// arithmetic of "how many copies the chain makes" is checkable rather than
// inferred.
func BenchmarkOneTransformerAlone(b *testing.B) {
	fns := []Transformer{Compute("provider", func(map[string]any) (any, error) { return "x", nil })}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := applyAll(fns, registro(i)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIngestionID isolates the id's composition, which the profile pointed
// at as the
// maior bloco sozinho.
func BenchmarkIngestionID(b *testing.B) {
	fns := []Transformer{IngestionID()}
	r := map[string]any{
		"provider": "acme", "entity": "pedidos",
		"source_key": "12345", "record_ts": "2026-09-05T12:00:00Z",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copia := make(map[string]any, len(r))
		for k, v := range r {
			copia[k] = v
		}
		if _, _, err := applyAll(fns, copia); err != nil {
			b.Fatal(err)
		}
	}
}
