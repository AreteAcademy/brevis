package core

import "testing"

// The formula is frozen: a row written in Go has to match the one a
// Python fetcher writes for the same record. Checked against Python's, and not
// against another implementation of ours -- two implementations of ours can
// change together and the test still pass.
func TestComputeIngestionIDAgainstPython(t *testing.T) {
	casos := []struct {
		provider, entity, sourceKey, recordTS, expected string
	}{
		// uuid.uuid5(UUID("e3a4f8c0-1b9d-4ea0-9c2e-77f6a6c4a4d7"), "p|e|k|2026-01-01T00:00:00Z")
		{"p", "e", "k", "2026-01-01T00:00:00Z", "178d0b49-dece-5738-b8eb-f5cae222a1ea"},
	}

	for _, c := range casos {
		got, err := ComputeIngestionID(c.provider, c.entity, c.sourceKey, c.recordTS)
		if err != nil {
			t.Fatalf("ComputeIngestionID: %v", err)
		}
		if got != c.expected {
			t.Errorf("ComputeIngestionID(%q,%q,%q,%q) = %s, congelado em %s",
				c.provider, c.entity, c.sourceKey, c.recordTS, got, c.expected)
		}
	}
}

// The Envelope and the transformer have to land in the same place, because the
// same row
// can arrive through either path.
func TestEnvelopeUsaAMesmaFormula(t *testing.T) {
	env := Envelope{Provider: "p", Entity: "e", SourceKey: "k", RecordTS: "t"}
	pelaEnvelope, err := env.IngestionID()
	if err != nil {
		t.Fatal(err)
	}
	direto, _ := ComputeIngestionID("p", "e", "k", "t")
	if pelaEnvelope != direto {
		t.Errorf("os dois caminhos divergiram: %s != %s", pelaEnvelope, direto)
	}
}
