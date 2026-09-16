package core

import (
	"reflect"
	"testing"
)

// ParamList turns a `list|<type>` param back into a slice.
//
// The engine sends every param as a string -- one map[string]string from the
// trigger form to this process -- so a list arrives comma-joined and this is
// what splits it.
func TestParamList(t *testing.T) {
	rc := RunContext{Params: map[string]string{
		"tables":  "users,orders",
		"spaced":  " users , orders ",
		"one":     "x",
		"empty":   "",
		"numbers": "1,7,30",
	}}

	casos := map[string]struct {
		name string
		want []string
	}{
		"the ordinary case":                 {"tables", []string{"users", "orders"}},
		"a form leaves a space after comma": {"spaced", []string{"users", "orders"}},
		"one item is still a list":          {"one", []string{"x"}},
		// Not []string{""}: iterating over a param nobody filled in has to do
		// nothing, and one empty element would run the body once on nothing.
		"nobody filled it in": {"empty", nil},
		"absent":              {"nope", nil},
		// Items are NOT converted: a fetcher that wants numbers knows better
		// than this package which width and which error handling it wants.
		"integers stay strings": {"numbers", []string{"1", "7", "30"}},
	}
	for why, c := range casos {
		if got := rc.ParamList(c.name); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: ParamList(%q) = %#v, want %#v", why, c.name, got, c.want)
		}
	}
}

// Reading a scalar param with ParamList gives the single value rather than
// something surprising: the two APIs coexist on one map, and a step that guesses
// wrong should not get a mangled value.
func TestParamListOnAScalarGivesTheOneValue(t *testing.T) {
	rc := RunContext{Params: map[string]string{"date": "2026-09-16"}}
	if got := rc.ParamList("date"); !reflect.DeepEqual(got, []string{"2026-09-16"}) {
		t.Errorf("ParamList on a scalar = %#v", got)
	}
}

// A RunContext built outside the engine has no Params map at all, and reading
// one must not panic -- a fetcher run by hand should not notice this exists.
func TestParamListOnAnEmptyContextDoesNotPanic(t *testing.T) {
	var rc RunContext
	if got := rc.ParamList("tables"); got != nil {
		t.Errorf("got %#v, want nil", got)
	}
}
