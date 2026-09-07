package redshift

import (
	"fmt"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// BenchmarkEncodeNDJSON stays in the repository because it is what showed the
// size of the gain: 13 allocations for 10 thousand lines, against ~50 thousand
// when every record went through a map[string]any in the json.Encoder.
func BenchmarkEncodeNDJSON(b *testing.B) {
	envelopes := make([]core.Envelope, 10000)
	for i := range envelopes {
		envelopes[i] = core.Envelope{Payload: map[string]any{
			"ingestion_id": fmt.Sprintf("id-%d", i), "provider": "acme",
			"valor": "10.50", "nome": "registro qualquer",
		}}
	}
	columns := []string{"ingestion_id", "provider", "valor", "nome"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncodeNDJSON(envelopes, columns); err != nil {
			b.Fatal(err)
		}
	}
}
