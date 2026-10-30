package workflow_test

import (
	"encoding/json"
	"strings"
	"testing"

	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
)

// THE FOUR NEW KEYS ARE ON DISK, AND ONLY WHEN SET: a param that uses none of
// them is the same document it was, which an older engine reads as before --
// and one that uses them only adds keys an older engine ignores.
func TestTheParamsPresentationKeysAreOnDiskWhenSet(t *testing.T) {
	plain, err := json.Marshal(wf.Param{Name: "date", Type: wf.ParamString})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"Nome":"date","Tipo":"string","Default":"","Descricao":"","Enum":null,"Pattern":""}`; string(plain) != want {
		t.Errorf("a plain param = %s, want the document it always was: %s", plain, want)
	}

	raw, err := json.Marshal(wf.Param{Name: "load_full", Type: wf.ParamBool,
		Title: "Full load", Group: "Load", Warn: "reprocesses everything; it costs", Uses: "bigquery"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"Title": "Full load", "Group": "Load", "Warn": "reprocesses everything; it costs", "Uses": "bigquery",
	} {
		if got[key] != want {
			t.Errorf("key %q = %v, want %q", key, got[key], want)
		}
	}
}

// A WORKFLOW PUBLISHED BEFORE THESE FIELDS reads with them empty, and nothing
// else lost.
func TestAnOldParamDocumentReadsWithoutThem(t *testing.T) {
	var p wf.Param
	old := `{"Nome":"date","Tipo":"string","Default":"2026-01-01","Descricao":"the day","Enum":null,"Pattern":""}`
	if err := json.Unmarshal([]byte(old), &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "date" || p.Default != "2026-01-01" || p.Description != "the day" {
		t.Errorf("the old document lost a field: %+v", p)
	}
	if p.Title != "" || p.Group != "" || p.Warn != "" || p.Uses != "" {
		t.Errorf("the old document grew a field: %+v", p)
	}
}

// THE PUBLISH CHECKS THEIR SHAPE, naming the param: one line each, a title
// and a group of 60 characters at most, a warning of 200, and a connector key
// in lowercase, digits and dashes.
func TestThePresentationKeysAreCheckedForShape(t *testing.T) {
	for _, c := range []struct {
		name string
		p    wf.Param
		want string // "" when it is accepted
	}{
		{"all of them", wf.Param{Title: "Carga completa", Group: "Carga", Warn: "reprocessa tudo; custa caro", Uses: "big-query2"}, ""},
		{"60 accented characters", wf.Param{Title: strings.Repeat("é", 60)}, ""},
		{"a title of two lines", wf.Param{Title: "Full\nload"}, "title"},
		{"a group of two lines", wf.Param{Group: "Load\r"}, "group"},
		{"a warning of two lines", wf.Param{Warn: "costly\nslow"}, "warn"},
		{"a title of 61", wf.Param{Title: strings.Repeat("a", 61)}, "title"},
		{"a group of 61", wf.Param{Group: strings.Repeat("a", 61)}, "group"},
		{"a warning of 201", wf.Param{Warn: strings.Repeat("a", 201)}, "warn"},
		{"a connector in capitals", wf.Param{Uses: "BigQuery"}, "uses"},
		{"a connector with a space", wf.Param{Uses: "big query"}, "uses"},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.p.Name, c.p.Type = "load_full", wf.ParamBool
			err := withParams(c.p).Validate()
			switch {
			case c.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case c.want != "" && err == nil:
				t.Error("accepted")
			case c.want != "" && (!strings.Contains(err.Error(), `"load_full"`) || !strings.Contains(err.Error(), c.want)):
				t.Errorf("the refusal %q does not name the param and %s", err, c.want)
			}
		})
	}
}

// NONE OF THEM CHANGES WHAT A VALUE MAY BE: a param with a warning, a title and
// a connector accepts and refuses exactly what it did without them.
func TestThePresentationKeysDoNotTouchTheValue(t *testing.T) {
	plain := wf.Param{Name: "mode", Type: wf.ParamString, Enum: []string{"fast", "slow"}}
	dressed := plain
	dressed.Title, dressed.Group, dressed.Warn, dressed.Uses = "Mode", "Run", "slow is slow", "postgres"
	for _, v := range []string{"fast", "slow", "medium", "fast;rm"} {
		if (plain.Accepts(v) == nil) != (dressed.Accepts(v) == nil) {
			t.Errorf("%q: plain %v, dressed %v", v, plain.Accepts(v), dressed.Accepts(v))
		}
	}
}
