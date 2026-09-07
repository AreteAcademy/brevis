package sdk

import (
	"encoding/json"
	"fmt"
	"iter"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Reduce folds the stream into groups before it reaches the destination.
//
//	Reduce: &sdk.Reduce{
//		By: sdk.GroupBy("region", "year"),
//		Agg: map[string]sdk.Aggregator{
//			"rows":       sdk.Count(),
//			"total":      sdk.Sum("value"),
//			"last_name":  sdk.MaxBy("name", "year"),
//		},
//	}
//
// It sits BETWEEN Transform and Target: the transformers prepare the row, the
// fold reduces, the destination receives the result.
//
// # The rule that decides what exists here
//
// Every aggregator in this package uses CONSTANT memory per group. That is not
// a preference: it is the only rule that keeps the SDK's promise, whose model
// is a stream. An aggregator that kept the rows would undo that silently, and
// the symptom would arrive as a pod killed by the OOM killer at 5am.
//
// With the rule, the cost stays predictable and sayable:
//
//	memory = number of groups x aggregator state
//
// The INPUT does not appear in that sum. That is why Median, Quantile, exact
// Distinct, Mode and Collect do not exist -- and why anyone who looks for them
// gets a message naming the two ways out that do.
type Reduce struct {
	// By are the fields that form the group key. GroupBy() with no fields
	// reduces the whole stream to a single row.
	By Grouping

	// Agg are the computed columns, by the name they will have on the way out.
	Agg map[string]Aggregator

	// Finish runs one pass over the GROUPS once the stream ends, for a global
	// reduction, a join against a small table, or a final projection.
	//
	// It sees the groups, never the records -- and that is what allows the
	// global phase without undoing the guarantee: memory stays proportional to
	// the number of groups.
	Finish func(groups iter.Seq2[Group, map[string]any]) ([]map[string]any, error)
}

// Group is one group's key, with the fields in GroupBy order.
type Group struct {
	Fields []string
	Values map[string]any
}

// Grouping is the set of fields that form the key.
type Grouping struct{ fields []string }

// GroupBy names the fields of the group key, in the order they are given.
//
// With no fields the whole stream becomes one group -- which is how you ask
// for a grand total.
func GroupBy(fields ...string) Grouping { return Grouping{fields: fields} }

// Accumulator is the door underneath: what to do when the ready-made
// aggregators do not cover the case.
//
// Every aggregator in this package is built with it, which is what guarantees
// the door works -- rather than being an escape hatch nobody ever tried.
type Accumulator struct {
	// Init creates the state for a new group.
	Init func() any

	// Add folds one record into the state.
	Add func(acc any, r map[string]any) error

	// Value closes the state into the output column.
	Value func(acc any) (any, error)
}

// Aggregator is a computed column. Use the constructors in this file, or
// Custom for what they do not cover.
type Aggregator struct {
	acc Accumulator

	// refusal is why this aggregator cannot exist. See the end of this file.
	refusal error

	// fields are the record fields this aggregator reads, so that a
	// misspelled name is refused by name instead of producing a zero total.
	fields []string
}

// Custom wraps an Accumulator.
//
// Memory is YOURS from here on: a state that grows with the rows undoes
// Reduce's guarantee, and the SDK has no way to check that for you.
func Custom(a Accumulator) Aggregator {
	return Aggregator{acc: a}
}

// --- The aggregators ------------------------------------------------------

// Count counts the rows in the group.
func Count() Aggregator {
	return Custom(Accumulator{
		Init:  func() any { return new(int64) },
		Add:   func(acc any, _ map[string]any) error { *acc.(*int64)++; return nil },
		Value: func(acc any) (any, error) { return *acc.(*int64), nil },
	})
}

// CountOf counts the rows where the field is not null.
func CountOf(field string) Aggregator {
	a := Custom(Accumulator{
		Init: func() any { return new(int64) },
		Add: func(acc any, r map[string]any) error {
			if r[field] != nil {
				*acc.(*int64)++
			}
			return nil
		},
		Value: func(acc any) (any, error) { return *acc.(*int64), nil },
	})
	return withFields(a, field)
}

// Sum adds the field up. Null and missing are ignored, as in SQL.
func Sum(field string) Aggregator {
	type state struct {
		total float64
		viu   bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &state{} },
		Add: func(acc any, r map[string]any) error {
			n, ok, err := numberOf(r, field)
			if err != nil || !ok {
				return err
			}
			e := acc.(*state)
			e.total, e.viu = e.total+n, true
			return nil
		},
		Value: func(acc any) (any, error) {
			e := acc.(*state)
			if !e.viu {
				return nil, nil // a group with no values at all: null, not zero
			}
			return e.total, nil
		},
	})
	return withFields(a, field)
}

// Mean is the arithmetic mean of the field, ignoring nulls.
func Mean(field string) Aggregator {
	type state struct {
		sum float64
		n   int64
	}
	a := Custom(Accumulator{
		Init: func() any { return &state{} },
		Add: func(acc any, r map[string]any) error {
			n, ok, err := numberOf(r, field)
			if err != nil || !ok {
				return err
			}
			e := acc.(*state)
			e.sum, e.n = e.sum+n, e.n+1
			return nil
		},
		Value: func(acc any) (any, error) {
			e := acc.(*state)
			if e.n == 0 {
				return nil, nil
			}
			return e.sum / float64(e.n), nil
		},
	})
	return withFields(a, field)
}

// Min is the smallest value of the field. Max is the largest.
func Min(field string) Aggregator { return extremo(field, -1) }

// Max is the largest value of the field.
func Max(field string) Aggregator { return extremo(field, +1) }

func extremo(field string, sinal int) Aggregator {
	type state struct {
		value any
		viu   bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &state{} },
		Add: func(acc any, r map[string]any) error {
			v := r[field]
			if v == nil {
				return nil
			}
			e := acc.(*state)
			if !e.viu {
				e.value, e.viu = v, true
				return nil
			}
			cmp, err := compare(v, e.value, field)
			if err != nil {
				return err
			}
			if cmp*sinal > 0 {
				e.value = v
			}
			return nil
		},
		Value: func(acc any) (any, error) { return acc.(*state).value, nil },
	})
	return withFields(a, field)
}

// First is the first non-null value seen in the group. Last is the last.
//
// "First" means in the order the source delivered: for a source with no
// defined order it is not deterministic, and that is the source's doing, not
// this package's.
func First(field string) Aggregator { return endOf(field, true) }

// Last is the last non-null value seen in the group.
func Last(field string) Aggregator { return endOf(field, false) }

func endOf(field string, first bool) Aggregator {
	type state struct {
		value any
		viu   bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &state{} },
		Add: func(acc any, r map[string]any) error {
			v := r[field]
			if v == nil {
				return nil
			}
			e := acc.(*state)
			if first && e.viu {
				return nil
			}
			e.value, e.viu = v, true
			return nil
		},
		Value: func(acc any) (any, error) { return acc.(*state).value, nil },
	})
	return withFields(a, field)
}

// MinBy returns the `value` of the row where `key` is smallest. MaxBy, the
// largest.
//
//	sdk.MaxBy("name", "year")   // the name from the row with the largest year
//
// It is the one that is usually missing, and its absence is what makes people
// keep the rows so they can pick later -- which is exactly what the
// constant-memory rule forbids.
func MinBy(value, key string) Aggregator { return porExtremo(value, key, -1) }

// MaxBy returns the `value` of the row where `key` is largest.
func MaxBy(value, key string) Aggregator { return porExtremo(value, key, +1) }

func porExtremo(valueField, keyField string, sinal int) Aggregator {
	type state struct {
		key   any
		value any
		viu   bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &state{} },
		Add: func(acc any, r map[string]any) error {
			k := r[keyField]
			if k == nil {
				return nil
			}
			e := acc.(*state)
			if !e.viu {
				e.key, e.value, e.viu = k, r[valueField], true
				return nil
			}
			cmp, err := compare(k, e.key, keyField)
			if err != nil {
				return err
			}
			if cmp*sinal > 0 {
				e.key, e.value = k, r[valueField]
			}
			return nil
		},
		Value: func(acc any) (any, error) { return acc.(*state).value, nil },
	})
	return withFields(a, valueField, keyField)
}

// Variance is the sample variance of the field. StdDev is its square root.
//
// By Welford, in one pass: the naive formula (sum of squares minus the square
// of the sum) loses every significant digit when the values are large and
// close together, and the result comes out negative.
func Variance(field string) Aggregator { return welford(field, false) }

// StdDev is the sample standard deviation of the field.
func StdDev(field string) Aggregator { return welford(field, true) }

func welford(field string, raiz bool) Aggregator {
	type state struct {
		n    float64
		medi float64
		m2   float64
	}
	a := Custom(Accumulator{
		Init: func() any { return &state{} },
		Add: func(acc any, r map[string]any) error {
			x, ok, err := numberOf(r, field)
			if err != nil || !ok {
				return err
			}
			e := acc.(*state)
			e.n++
			d := x - e.medi
			e.medi += d / e.n
			e.m2 += d * (x - e.medi)
			return nil
		},
		Value: func(acc any) (any, error) {
			e := acc.(*state)
			if e.n < 2 {
				return nil, nil // the sample variance of one point does not exist
			}
			v := e.m2 / (e.n - 1)
			if raiz {
				return math.Sqrt(v), nil
			}
			return v, nil
		},
	})
	return withFields(a, field)
}

// Any is true when some row has the field true. All, when every row does.
func Any(field string) Aggregator { return boolAgg(field, false) }

// All is true when every row has the field true.
func All(field string) Aggregator { return boolAgg(field, true) }

func boolAgg(field string, all bool) Aggregator {
	type state struct {
		v   bool
		viu bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &state{v: all} },
		Add: func(acc any, r map[string]any) error {
			v := r[field]
			if v == nil {
				return nil
			}
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf("field %q holds %v (%T), which is not a boolean; "+
					"Any and All read booleans", field, v, v)
			}
			e := acc.(*state)
			e.viu = true
			if all {
				e.v = e.v && b
			} else {
				e.v = e.v || b
			}
			return nil
		},
		Value: func(acc any) (any, error) {
			e := acc.(*state)
			if !e.viu {
				return nil, nil
			}
			return e.v, nil
		},
	})
	return withFields(a, field)
}

// Range is the difference between the largest and smallest value.
func Range(field string) Aggregator {
	type state struct {
		min, max float64
		viu      bool
	}
	a := Custom(Accumulator{
		Init: func() any { return &state{} },
		Add: func(acc any, r map[string]any) error {
			n, ok, err := numberOf(r, field)
			if err != nil || !ok {
				return err
			}
			e := acc.(*state)
			if !e.viu {
				e.min, e.max, e.viu = n, n, true
				return nil
			}
			e.min, e.max = math.Min(e.min, n), math.Max(e.max, n)
			return nil
		},
		Value: func(acc any) (any, error) {
			e := acc.(*state)
			if !e.viu {
				return nil, nil
			}
			return e.max - e.min, nil
		},
	})
	return withFields(a, field)
}

func withFields(a Aggregator, fields ...string) Aggregator {
	a.fields = fields
	return a
}

// --- Coercion -------------------------------------------------------------

// numberOf reads a field as a number. The second return says whether there
// was a value: null and missing are ignored, as in SQL.
//
// A text that is a number IS a number -- `12.5` coming out of a CSV is not
// ambiguous, and refusing it would force a transformer that exists only to
// convert. What is not a number becomes an error naming the field AND the
// value, because without the value nobody finds the guilty row among a
// million.
func numberOf(r map[string]any, field string) (float64, bool, error) {
	v := r[field]
	switch t := v.(type) {
	case nil:
		return 0, false, nil
	case float64:
		return t, true, nil
	case float32:
		return float64(t), true, nil
	case int:
		return float64(t), true, nil
	case int64:
		return float64(t), true, nil
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0, false, fmt.Errorf("field %q holds %q, which is not a number", field, t.String())
		}
		return f, true, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false, fmt.Errorf("field %q holds %q, which is not a number", field, t)
		}
		return f, true, nil
	default:
		return 0, false, fmt.Errorf("field %q holds %v (%T), which is not a number", field, v, v)
	}
}

// compare orders two values of the same field. Numbers by value, texts
// lexicographically.
//
// Mixed types are an ERROR, not an invented order: a field carrying 10 and "9"
// would make the maximum depend on arrival order, and the result would change
// between runs without anyone noticing.
func compare(a, b any, field string) (int, error) {
	na, aNum := asNumber(a)
	nb, bNum := asNumber(b)
	if aNum && bNum {
		switch {
		case na < nb:
			return -1, nil
		case na > nb:
			return 1, nil
		}
		return 0, nil
	}

	sa, aTxt := a.(string)
	sb, bTxt := b.(string)
	if aTxt && bTxt {
		return strings.Compare(sa, sb), nil
	}

	return 0, fmt.Errorf("field %q mixes %T and %T in the same group; "+
		"the order between them would depend on arrival order", field, a, b)
}

// asNumber is the coercion WITHOUT text: here a "9" is text, and comparing
// texts against numbers is the error the function above refuses.
func asNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

// --- The fold -------------------------------------------------------------

// groupSeparator joins the key fields. The zero byte does not appear in text
// coming from JSON or CSV, so two distinct groups cannot collide because of a
// value that contains the separator.
const groupSeparator = "\x00"

func (d *Reduce) validate() error {
	if d == nil {
		return nil
	}
	if len(d.Agg) == 0 && d.Finish == nil {
		return fmt.Errorf("reduce with neither Agg nor Finish does nothing; " +
			"drop it, or say what it computes")
	}
	for _, field := range d.By.fields {
		if _, colide := d.Agg[field]; colide {
			return fmt.Errorf("%q is both a group field and an aggregator name; "+
				"the column would have two values", field)
		}
	}
	for _, name := range sortedNames(d.Agg) {
		a := d.Agg[name]
		// The refusal comes BEFORE the extract: finding it afterwards would
		// mean having spent the vendor's quota for nothing.
		if a.refusal != nil {
			return fmt.Errorf("in %q: %w", name, a.refusal)
		}
		if a.acc.Init == nil || a.acc.Add == nil || a.acc.Value == nil {
			return fmt.Errorf("aggregator %q is incomplete: Custom needs "+
				"Init, Add and Value", name)
		}
	}
	return nil
}

type accumulatedGroup struct {
	key    string
	values map[string]any
	states map[string]any
	order  int
}

// apply drena o fluxo, agrega e devolve as rows resultantes.
func (d *Reduce) apply(rows iter.Seq2[Envelope, error]) iter.Seq2[Envelope, error] {
	return func(yield func(Envelope, error) bool) {
		groups, seen, err := d.fold(rows)
		if err != nil {
			yield(Envelope{}, err)
			return
		}
		if err := d.checkFields(seen); err != nil {
			yield(Envelope{}, err)
			return
		}

		out, err := d.closeIt(groups)
		if err != nil {
			yield(Envelope{}, err)
			return
		}
		for _, row := range out {
			if !yield(Envelope{Payload: row}, nil) {
				return
			}
		}
	}
}

func (d *Reduce) fold(rows iter.Seq2[Envelope, error]) ([]*accumulatedGroup, map[string]bool, error) {
	byKey := map[string]*accumulatedGroup{}
	var order []*accumulatedGroup
	seen := map[string]bool{}
	// Identity is checked on the FIRST row: if it reached here it reached
	// every row, and checking once costs nothing among a million.
	firstRow := true

	for env, err := range rows {
		if err != nil {
			return nil, nil, err
		}
		row, err := asRecord(env.Payload)
		if err != nil {
			return nil, nil, err
		}
		if firstRow {
			firstRow = false
			if err := refuseIdentity(row); err != nil {
				return nil, nil, err
			}
		}
		for k := range row {
			seen[k] = true
		}

		key, values, err := d.keyOf(row)
		if err != nil {
			return nil, nil, err
		}
		g := byKey[key]
		if g == nil {
			g = &accumulatedGroup{
				key: key, values: values,
				states: make(map[string]any, len(d.Agg)),
				order:  len(order),
			}
			for name, a := range d.Agg {
				g.states[name] = a.acc.Init()
			}
			byKey[key] = g
			order = append(order, g)
		}
		for name, a := range d.Agg {
			if err := a.acc.Add(g.states[name], row); err != nil {
				return nil, nil, fmt.Errorf("aggregator %q: %w", name, err)
			}
		}
	}

	// Deterministic order by group key. A row's identity comes from its
	// content, so order changes nothing -- but a -sample that returns
	// different rows on every run gets in the way of whoever is debugging.
	sort.Slice(order, func(i, j int) bool { return order[i].key < order[j].key })
	return order, seen, nil
}

// checkFields refuses an aggregator that names a field NO row had.
//
// Without it, a misspelled name produces a column of nulls or zeros and nobody
// notices -- which is the worst way to fail. A field missing from SOME rows
// stays normal, and is ignored as in SQL.
func (d *Reduce) checkFields(seen map[string]bool) error {
	if len(seen) == 0 {
		return nil // empty stream: nothing to check
	}
	missing := map[string]bool{}
	for _, a := range d.Agg {
		for _, c := range a.fields {
			if !seen[c] {
				missing[c] = true
			}
		}
	}
	for _, c := range d.By.fields {
		if !seen[c] {
			missing[c] = true
		}
	}
	if len(missing) == 0 {
		return nil
	}
	names := sortedKeys(missing)
	return fmt.Errorf("the reduction names %s, which no row has. The available fields are: %s",
		strings.Join(quoted(names), ", "), strings.Join(sortedKeys(seen), ", "))
}

func (d *Reduce) keyOf(row map[string]any) (string, map[string]any, error) {
	if len(d.By.fields) == 0 {
		return "", map[string]any{}, nil
	}
	var b strings.Builder
	values := make(map[string]any, len(d.By.fields))
	for i, field := range d.By.fields {
		if i > 0 {
			b.WriteString(groupSeparator)
		}
		v := row[field]
		values[field] = v
		b.WriteString(asText(v))
	}
	return b.String(), values, nil
}

func (d *Reduce) closeIt(groups []*accumulatedGroup) ([]map[string]any, error) {
	rows := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		row := make(map[string]any, len(g.values)+len(d.Agg))
		for k, v := range g.values {
			row[k] = v
		}
		for name, a := range d.Agg {
			v, err := a.acc.Value(g.states[name])
			if err != nil {
				return nil, fmt.Errorf("aggregator %q: %w", name, err)
			}
			row[name] = v
		}
		rows = append(rows, row)
	}

	if d.Finish == nil {
		return rows, nil
	}
	return d.Finish(func(yield func(Group, map[string]any) bool) {
		for i, g := range groups {
			if !yield(Group{Fields: d.By.fields, Values: g.values}, rows[i]) {
				return
			}
		}
	})
}

func asRecord(p any) (map[string]any, error) {
	row, ok := p.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("a reduction aggregates JSON objects; got %T", p)
	}
	return row, nil
}

// sortedNames keeps error messages stable: without it, a pipeline with two
// invalid aggregators would complain about a different one on every run.
func sortedNames(m map[string]Aggregator) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func quoted(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = strconv.Quote(v)
	}
	return out
}

// --- What does not exist, and why -----------------------------------------
//
// These exist as FUNCTIONS and refuse at assembly time, before the extract.
//
// That is not the same as being absent. Somebody who writes `sdk.Median("x")`
// and gets "undefined" from the compiler will implement it by hand -- keeping
// the group's rows, which is exactly what the constant-memory rule exists to
// prevent. Somebody who gets the message below learns WHY, and the two ways
// out that do exist.

// Median does not exist: it needs every row in the group.
//
// Compute it at the destination, with SQL, or use Custom and take on the
// memory cost explicitly.
func Median(field string) Aggregator { return refuse("Median", "every row in the group") }

// Quantile does not exist, for the same reason as Median.
func Quantile(field string, q float64) Aggregator {
	return refuse("Quantile", "every row in the group")
}

// Distinct does not exist: an exact count needs a set per group, which grows
// with the cardinality of the input.
func Distinct(field string) Aggregator { return refuse("Distinct", "a set per group") }

// Mode does not exist: it needs a frequency map per group.
func Mode(field string) Aggregator { return refuse("Mode", "a frequency map per group") }

// Collect does not exist: gathering the group's rows is literally what the
// rule forbids.
func Collect(field string) Aggregator { return refuse("Collect", "every row in the group") }

func refuse(name, cost string) Aggregator {
	return Aggregator{refusal: fmt.Errorf(
		"sdk.%s does not exist: it needs %s, and this aggregator runs in constant "+
			"memory. Two ways out: compute it at the destination, with SQL, or use "+
			"sdk.Custom -- and take on the memory cost explicitly", name, cost)}
}
