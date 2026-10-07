package sdk

import (
	"time"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// targetPreview samples the rows on their way to the destination.
//
// It is a type and not a function because the batched path (FlushEvery) never
// holds the whole load: the sample fills from batches as they go and prints
// once, and a closure over a slice in two call sites is the shape that drifts.
type targetPreview struct {
	want    int
	columns []string
	sample  []any
	rows    int
}

// newTargetPreview returns nil when the target asked for none, so every call
// site is `if p != nil` and no method has to guard.
func newTargetPreview(t Target) *targetPreview {
	if t.Preview <= 0 {
		return nil
	}
	return &targetPreview{want: t.Preview, columns: t.Columns}
}

// take keeps the first N records, whole.
//
// The FIRST version of this projected each record through Columns before
// sampling, to "show what the driver will read". A mutation test proved that
// dead: the renderer is already given Columns as its order, so it reads exactly
// those names out of whatever it is handed, and the output was byte for byte
// the same with the projection removed.
//
// Which is the better arrangement anyway. Projecting here would have been a
// second copy of how a driver reads a column, in the one place nobody would
// think to check when the first copy changed.
func (p *targetPreview) take(batch []Envelope) {
	p.rows += len(batch)
	for _, e := range batch {
		if len(p.sample) >= p.want {
			return
		}
		p.sample = append(p.sample, e.Payload)
	}
}

// write prints the table. Called once, after the last take.
func (p *targetPreview) write(t Target, elapsed time.Duration) {
	core.WritePreviewIn(t.PreviewWriter, p.sample, t.PreviewBytes, core.PreviewStats{
		Rows:     p.rows,
		Duration: elapsed,
	}, p.columns)
}
