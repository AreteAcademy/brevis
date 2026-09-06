package from_test

import (
	"testing"

	"github.com/AreteAcademy/brevis/sdk/from"
)

// TestCampoJSONStillReadsWhatJSONTokenReads guards the deprecated alias.
//
// CampoJSON shipped in a published version. Deleting it would break a
// consumer's build with an error that names a symbol and explains nothing, so
// it stays until v1 -- and this test is what makes the deletion visible here
// instead of there.
func TestCampoJSONStillReadsWhatJSONTokenReads(t *testing.T) {
	body := []byte(`{"data":{"accessToken":"abc123"}}`)

	novo, err := from.JSONToken("data.accessToken")(body)
	if err != nil {
		t.Fatalf("JSONToken: %v", err)
	}
	velho, err := from.CampoJSON("data.accessToken")(body)
	if err != nil {
		t.Fatalf("CampoJSON: %v", err)
	}

	if novo != velho {
		t.Fatalf("the alias diverged: JSONToken=%q CampoJSON=%q", novo, velho)
	}
	if novo != "abc123" {
		t.Fatalf("read %q, want %q", novo, "abc123")
	}
}
