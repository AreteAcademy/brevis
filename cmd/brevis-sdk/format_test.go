package main

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
)

// The case table for the one decision this CLI got wrong three ways.
func TestResolveFormat(t *testing.T) {
	cases := []struct {
		name   string
		flag   string
		url    string
		want   sdk.Format
		errors bool
	}{
		{name: "an explicit flag wins", flag: "ndjson", url: "https://x/a.csv", want: sdk.FormatNDJSON},
		{name: "case and spaces do not matter", flag: " CSV ", url: "https://x/a", want: sdk.FormatCSV},

		// The defect: an unrecognised value used to become CSV in silence, and
		// the CSV reader accepts anything, so the output was garbage with no
		// error anywhere.
		{name: "an unknown format is refused", flag: "yaml", url: "https://x/a", errors: true},
		{name: "a typo is refused", flag: "jsonl", url: "https://x/a", errors: true},

		// With no flag, the URL decides -- which is what `run` never did.
		{name: "csv from the path", url: "https://x/data.csv", want: sdk.FormatCSV},
		{name: "json from the path", url: "https://x/data.json", want: sdk.FormatJSON},
		{name: "ndjson from the path", url: "https://x/data.ndjson", want: sdk.FormatNDJSON},
		{name: "jsonl is ndjson", url: "https://x/data.jsonl", want: sdk.FormatNDJSON},
		{name: "xml from the path", url: "https://x/data.xml", want: sdk.FormatXML},
		{name: "a gzipped path reads through the .gz", url: "https://x/data.csv.gz", want: sdk.FormatCSV},

		// The query string is the server's parameter, not a statement about the
		// body. Reading it would be guessing from something that is not ours.
		{name: "the query string does not decide", url: "https://x/report?out=csv", want: ""},
		{name: "a path that says nothing", url: "https://api.example.com/v1/events", want: ""},
		{name: "no url at all", url: "", want: ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveFormat(c.flag, c.url)
			if c.errors {
				if err == nil {
					t.Fatalf("resolveFormat(%q) accepted it; the CSV reader takes "+
						"anything, so this used to produce garbage with no error", c.flag)
				}
				if !strings.Contains(err.Error(), "csv, json, ndjson, xml") {
					t.Errorf("the error does not list what IS valid: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveFormat(%q, %q): %v", c.flag, c.url, err)
			}
			if got != c.want {
				t.Errorf("= %q, want %q", got, c.want)
			}
		})
	}
}

// Empty is the SDK's default, which is JSON -- deliberately not CSV.
//
// Between two wrong guesses, take the one that stops: JSON on a CSV body fails
// on the first line, while CSV on a JSON body succeeds and returns one column of
// nonsense. The second is the failure this project refuses.
func TestEmptyIsNotCSV(t *testing.T) {
	got, err := resolveFormat("", "https://api.example.com/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	if got == sdk.FormatCSV {
		t.Error("an unknown body defaults to CSV, which parses anything and " +
			"fails silently")
	}
}
