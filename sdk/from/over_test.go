package from_test

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/from"
)

// One source per value, each carrying its own. Without this the whole feature
// is a loop somebody still writes by hand.
func TestOverBuildsOneSourcePerValue(t *testing.T) {
	ufs := []string{"AC", "AL", "AM"}

	sources := from.Over(ufs, func(uf string) sdk.Reader {
		return fakeSource{name: "inventory?uf=" + uf, lines: 1}
	})

	if len(sources) != len(ufs) {
		t.Fatalf("%d values produced %d sources", len(ufs), len(sources))
	}
	for i, uf := range ufs {
		if got := sources[i].Describe(); !strings.Contains(got, uf) {
			t.Errorf("source %d describes %q, which does not name %q", i, got, uf)
		}
	}
}

// Describe() has to tell them apart. With 27 sources an error that says only
// "HTTP" names nothing, and the operator cannot tell which value failed.
func TestOverKeepsTheSourcesDistinguishable(t *testing.T) {
	sources := from.Over([]string{"AC", "AL"}, func(uf string) sdk.Reader {
		return fakeSource{name: "uf=" + uf}
	})

	// Checked before indexing: an index into a short slice PANICS, and a panic
	// takes the whole test binary down -- hiding every test after this one.
	// That is how a mutation run reported two failures where there were three.
	if len(sources) != 2 {
		t.Fatalf("two values produced %d sources", len(sources))
	}
	if sources[0].Describe() == sources[1].Describe() {
		t.Errorf("both sources describe themselves as %q", sources[0].Describe())
	}
}

// The value is not always a string, and not always a query parameter. This is
// the case the feature was almost built unable to serve: a path segment, from
// a struct.
func TestOverCarriesWhateverTheValueIs(t *testing.T) {
	type station struct {
		uid    string
		region string
	}

	sources := from.Over([]station{{"br-1", "south"}, {"br-2", "north"}},
		func(s station) sdk.Reader {
			return fakeSource{name: fmt.Sprintf("/feed/%s?region=%s", s.uid, s.region)}
		})

	if len(sources) != 2 {
		t.Fatalf("two values produced %d sources", len(sources))
	}
	if got := sources[1].Describe(); got != "/feed/br-2?region=north" {
		t.Errorf("the second source is %q", got)
	}
}

// An empty list is not an error here. from.Many is what separates "nothing to
// read" from "did not know where to read", and it already says so well; a
// second opinion from this function would be a second message for one state.
func TestOverOnAnEmptyListDefersToMany(t *testing.T) {
	sources := from.Over([]string{}, func(string) sdk.Reader { return fakeSource{} })
	if sources != nil {
		t.Fatalf("an empty list produced %#v", sources)
	}

	_, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.Many{Discover: func(context.Context) ([]sdk.Reader, error) {
			return from.Over([]string{}, func(string) sdk.Reader { return fakeSource{} }), nil
		}},
	})
	if err == nil {
		t.Fatal("an empty expansion extracted without complaint")
	}
	if !strings.Contains(err.Error(), "no source at all") {
		t.Errorf("the error is %q, and it should be Many's own", err)
	}
}

// The default path: no Workers, no OnError, nothing configured. A feature whose
// only tested shape is the configured one has an untested default.
func TestOverNeedsNoConfiguration(t *testing.T) {
	data, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.Many{
			Sources: from.Over([]string{"a", "b", "c"}, func(v string) sdk.Reader {
				return fakeSource{name: v, lines: 2}
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var rows int
	for _, err := range data.Records {
		if err != nil {
			t.Fatal(err)
		}
		rows++
	}
	if rows != 6 {
		t.Errorf("three sources of two rows gave %d", rows)
	}
}

// ContinueOnError is from.Many's, and this is the combination the fetchers use:
// one federative unit that fails must not take the other 26 down.
func TestOverLeavesTheFailurePolicyToMany(t *testing.T) {
	sources := from.Over([]string{"ok1", "boom", "ok2"}, func(v string) sdk.Reader {
		if v == "boom" {
			return fakeSource{name: v, openErr: fmt.Errorf("%s is down", v)}
		}
		return fakeSource{name: v, lines: 2}
	})

	data, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.Many{Sources: sources, OnError: sdk.ContinueOnError},
	})
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	for _, err := range data.Records {
		if err != nil {
			t.Fatal(err)
		}
		rows++
	}
	if rows != 4 {
		t.Errorf("the two healthy sources gave %d rows, want 4", rows)
	}

	// And the default still aborts: a silent continue would be the dangerous
	// half of this pair.
	strict, err := sdk.Extract(context.Background(), sdk.Source{From: from.Many{Sources: sources}})
	if err == nil {
		var readErr error
		for _, e := range strict.Records {
			if e != nil {
				readErr = e
				break
			}
		}
		err = readErr
	}
	if err == nil {
		t.Error("AbortOnError is the default and a failing source was tolerated")
	}
}

// A nil builder must say what is missing. The natural failure is a nil call
// inside the loop, which reports a nil dereference and points at the SDK.
func TestOverRefusesANilBuilder(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a nil builder was accepted")
		}
		if !strings.Contains(fmt.Sprint(r), "build is nil") {
			t.Errorf("the panic says %q, which does not name the missing argument", r)
		}
	}()
	from.Over([]string{"a"}, nil)
}

// The expansion must not read ahead. Over builds N Reader VALUES eagerly --
// that is a struct each, and it is the point -- but nothing is opened until the
// iteration reaches it, and the first rows arrive before the last source is
// touched.
//
// driver.go states the contract this defends: "a driver that materialises the
// whole source before returning puts a 5 GB export in memory". Twenty-seven
// inventories are not 5 GB, but the rule does not get to have exceptions.
func TestOverStaysLazyThroughMany(t *testing.T) {
	var opened int32

	sources := from.Over([]int{0, 1, 2, 3, 4}, func(n int) sdk.Reader {
		return countingSource{n: n, opened: &opened}
	})

	data, err := sdk.Extract(context.Background(), sdk.Source{
		From: from.Many{Sources: sources}, // one worker: strictly in order
	})
	if err != nil {
		t.Fatal(err)
	}

	var first int32 = -1
	for env, err := range data.Records {
		if err != nil {
			t.Fatal(err)
		}
		if first < 0 {
			first = atomic.LoadInt32(&opened)
			_ = env
		}
	}

	if first < 0 {
		t.Fatal("no record arrived")
	}
	if first == int32(len(sources)) {
		t.Errorf("all %d sources were opened before the first record: not lazy", first)
	}
}

// countingSource records how many sources have been opened by the time it is
// read.
type countingSource struct {
	n      int
	opened *int32
}

func (c countingSource) Describe() string { return fmt.Sprintf("source %d", c.n) }

func (c countingSource) Read(context.Context, sdk.ReadOptions) (iter.Seq2[sdk.Envelope, error], error) {
	atomic.AddInt32(c.opened, 1)
	return func(yield func(sdk.Envelope, error) bool) {
		yield(sdk.Envelope{Payload: map[string]any{"n": c.n}}, nil)
	}, nil
}
