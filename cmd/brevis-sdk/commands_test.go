package main

import (
	"strings"
	"testing"
)

// The command's help says it reads NDJSON from stdin. It used to read nothing:
// it built an empty slice, loaded zero rows, and printed "Load completed /
// Rows: 0".
//
// A pipe whose upstream produced nothing looked exactly like a pipe that worked.
func TestLoadReadsTheNDJSONOnStdin(t *testing.T) {
	input := `{"id":1,"name":"a"}
{"id":2,"name":"b"}
{"id":3,"name":"c"}`

	envelopes, err := lerNDJSON(strings.NewReader(input))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(envelopes) != 3 {
		t.Fatalf("read %d records, want 3", len(envelopes))
	}
	first, ok := envelopes[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload is %T, want a JSON object", envelopes[0].Payload)
	}
	if first["name"] != "a" {
		t.Errorf("first record: %v", first)
	}
	// Order is the file's order: a positional key depends on it.
	last := envelopes[2].Payload.(map[string]any)
	if last["name"] != "c" {
		t.Errorf("the order changed: %v", last)
	}
}

// A malformed record among thousands is unfindable without the line number.
func TestLoadNamesTheLineThatIsBroken(t *testing.T) {
	_, err := lerNDJSON(strings.NewReader(`{"id":1}
{"id":2}
{"id":  }`))
	if err == nil {
		t.Fatal("malformed JSON was accepted")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("the message does not name the line: %v", err)
	}
}

// A record whose payload crosses 64 KB is why this uses a Decoder and not a
// Scanner: a Scanner stops reading in silence at its buffer limit.
func TestLoadReadsARecordLargerThanAScannerLine(t *testing.T) {
	grande := `{"payload":"` + strings.Repeat("x", 200_000) + `"}`
	envelopes, err := lerNDJSON(strings.NewReader(grande))
	if err != nil {
		t.Fatalf("a 200 kB record was refused: %v", err)
	}
	if len(envelopes) != 1 {
		t.Fatalf("read %d records, want 1", len(envelopes))
	}
}

// Empty input is empty, and the caller refuses it rather than loading nothing.
func TestEmptyStdinReadsNoRecords(t *testing.T) {
	envelopes, err := lerNDJSON(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(envelopes) != 0 {
		t.Errorf("read %d records from empty input", len(envelopes))
	}
}
