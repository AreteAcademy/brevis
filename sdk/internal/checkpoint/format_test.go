package checkpoint

import (
	"encoding/json"
	"testing"
)

// TestTheManifestKeysAreTheOnDiskFormat pins the manifest's JSON keys.
//
// They are Portuguese while the code around them is English, and that looks
// like an oversight, which is exactly why this test exists: a rename here does
// not fail to compile and does not fail loudly at runtime.
//
// What it costs, per key:
//
//   - "registros": the count check goes silent, and a truncated depot loads
//     fewer rows than the first attempt did;
//   - "partes": every resume finds an empty depot and re-extracts, spending
//     the vendor quota this package exists to save;
//   - "numeros": UseNumber is never turned on, `19.0` comes back as `19`, and
//     every ingestion_id of the resumed attempt differs from the first one's.
//     No error, no log -- a duplicated row after the next merge.
//
// Changing the format is allowed. Changing it by accident is not, and this is
// the line that tells the two apart.
func TestTheManifestKeysAreTheOnDiskFormat(t *testing.T) {
	b, err := json.Marshal(Manifest{
		Version: manifestVersion, Records: 2,
		Parts: []string{"parte-00000.ndjson"}, Numbers: NumbersLiteral,
		Pipeline: "p", Run: "r", WrittenAt: "2026-09-06T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{"versao", "registros", "partes", "numeros", "gravado_em"} {
		if _, ok := got[key]; !ok {
			t.Errorf("the manifest no longer writes %q; a depot written by a released "+
				"SDK stops being readable, and the failure is silent. Full manifest: %s",
				key, b)
		}
	}

	if manifestFile != "_completo" {
		t.Errorf("the manifest file is %q, and a released SDK wrote _completo", manifestFile)
	}
	if partPattern != "parte-%05d.ndjson" {
		t.Errorf("the part pattern is %q, and a released SDK wrote parte-%%05d.ndjson", partPattern)
	}
}
