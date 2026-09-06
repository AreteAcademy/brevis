package sdk

import (
	"fmt"
	"iter"
	"strings"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Stage is one phase between the source and the destination: a Map that
// reshapes each record, or an Aggregate that folds them into groups.
//
//	Stages: []sdk.Stage{
//		sdk.Map(normalise),
//		sdk.Aggregate(sdk.Reduce{By: sdk.GroupBy("area", "year"), Agg: ...}),
//		sdk.Map(sdk.IngestionID()),
//	}
//
// # Two stages is already a lot
//
// Read that as a warning, not as encouragement. Aggregation is usually the
// warehouse's job: SQL over a landed table is easier to re-run, easier to fix
// and does not spend the vendor's window. A four-stage pipeline is probably
// solving the problem in the wrong place -- and the reason stages exist is not
// to make that comfortable, it is that identity cannot be computed before the
// row that lands exists.
//
// # Why order lives in a list
//
// The alternative was a second field -- Transform, Reduce, TransformAfter --
// and the question it could not answer was "why two phases and not three".
// A pipeline that aggregates by day and then by month needs two reductions.
// The number two came from nothing but the first case that showed up.
type Stage struct {
	kind         string
	transformers []Transformer
	reduce       *Reduce
}

// Stage kinds, as they appear in StageResult.
const (
	StageMap       = "map"
	StageAggregate = "aggregate"
)

// Map is a stage that reshapes each record, in order. It is what Transform
// declares, and the two are the same thing.
func Map(fns ...Transformer) Stage {
	return Stage{kind: StageMap, transformers: fns}
}

// Aggregate is a stage that folds records into groups. See Reduce.
//
// Every Aggregate is a BARRIER: nothing flows past it until the source is
// drained. That is inherent to aggregating, not a limitation -- but with
// stacked stages it is easy to forget. The memory ceiling stays the largest
// stage's, groups times state, never records.
func Aggregate(r Reduce) Stage {
	return Stage{kind: StageAggregate, reduce: &r}
}

// StageResult is what one stage did: how many records went in and how many
// came out.
//
// Without it, "5,515 rows" says nothing about where the other six million
// went, and finding out means bisecting the pipeline by hand.
type StageResult struct {
	Kind string // StageMap or StageAggregate

	In  int64
	Out int64

	// Groups is how many groups an aggregation produced. Zero for a map.
	Groups int64
}

// stages resolves the shorthand into the list that actually runs.
func (p *Pipeline) stages() ([]Stage, error) {
	if len(p.Stages) > 0 {
		if len(p.Transform) > 0 || p.Reduce != nil {
			return nil, fmt.Errorf("stages was declared together with Transform or Reduce: " +
				"they describe the same thing, and one of them would be ignored in silence. " +
				"Keep Stages, and move the others into it with sdk.Map and sdk.Aggregate")
		}
		return p.Stages, nil
	}

	var out []Stage
	if len(p.Transform) > 0 {
		out = append(out, Map(p.Transform...))
	}
	if p.Reduce != nil {
		out = append(out, Aggregate(*p.Reduce))
	}
	return out, nil
}

// validate refuses what cannot run, before the extract spends the vendor's
// window.
func (s Stage) validate() error {
	switch s.kind {
	case StageMap:
		return nil
	case StageAggregate:
		return s.reduce.validate()
	default:
		return fmt.Errorf("unknown stage %q: build stages with sdk.Map or sdk.Aggregate", s.kind)
	}
}

// apply wires the stage into the stream and counts what passes through it.
func (s Stage) apply(records iter.Seq2[Envelope, error], count *StageResult, origin string) iter.Seq2[Envelope, error] {
	count.Kind = s.kind

	entering := counting(records, &count.In)
	switch s.kind {
	case StageAggregate:
		out := s.reduce.apply(entering)
		return counting(out, &count.Out, func() { count.Groups = count.Out })
	default:
		out := entering
		if len(s.transformers) > 0 {
			out = transformAll(out, s.transformers, origin)
		}
		return counting(out, &count.Out)
	}
}

// counting increments n for every record that passes, and runs onEnd when the
// stream is exhausted.
func counting(records iter.Seq2[Envelope, error], n *int64, onEnd ...func()) iter.Seq2[Envelope, error] {
	return func(yield func(Envelope, error) bool) {
		for env, err := range records {
			if err == nil {
				*n++
			}
			if !yield(env, err) {
				return
			}
		}
		for _, f := range onEnd {
			f()
		}
	}
}

// identityColumns are the ones an Aggregate must not receive. See
// Reduce.refuseIdentity.
var identityColumns = []string{core.MetadataID}

// refuseIdentity stops a reduction that would aggregate the identity of the
// rows feeding it.
//
// It does not raise on its own -- it produces a row whose key describes
// nothing, which is worse. Identity belongs to the row that LANDS, and after
// an aggregation that row does not exist yet.
func refuseIdentity(row map[string]any) error {
	var found []string
	for _, c := range identityColumns {
		if _, has := row[c]; has {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf("this aggregation received records that already carry %s. "+
		"Identity describes the row that lands, and aggregating after it produces a key "+
		"that corresponds to nothing. Move the stage that computes it after the last "+
		"aggregation", strings.Join(found, " and "))
}
