package sdk

import (
	"fmt"
	"io"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Source says where records come from, and what every origin honours.
//
// The origin itself is From -- from.HTTP, from.Files, postgres.Query. What
// lives here instead of in the driver is what is true of all of them: the
// preview, the counters.
//
// See ExampleSource.
type Source struct {
	// From is the origin. Required.
	From Reader

	// Preview prints the first N records once the read finishes, the way a
	// dataframe's head() shows the top of a frame. Zero prints nothing.
	//
	// It answers "what did I actually just pull?" without a debugger and
	// without draining the stream into a variable. The sample is taken as the
	// records stream past, so it costs N records of memory and never changes
	// what the consumer receives.
	Preview int

	// PreviewBytes caps the printed block. Zero uses 4096. Rows are dropped
	// from the bottom until it fits, and the footer says how many.
	PreviewBytes int

	// PreviewWriter is where the table goes. Nil means os.Stderr.
	PreviewWriter io.Writer

	// Stats, when not nil, is filled in as the read proceeds. Read it after
	// the stream is drained: that is when the counters are final.
	Stats *core.Stats

	// Snapshot keeps the record AS THE SOURCE DELIVERED IT, under this name,
	// before any Transform. Empty keeps nothing.
	//
	//	Source: sdk.Source{From: ..., Snapshot: "payload"}
	//
	// # Why here and not a Transformer
	//
	// The snapshot of the raw record has to be taken before any derived field.
	// As a transformer it would depend on its POSITION in the chain -- and
	// getting the order wrong is not an error: it gives a "raw" record carrying
	// the fields the chain itself has just written, and nobody notices until
	// somebody queries the data months later.
	//
	// Here the guarantee is structural: the snapshot is taken where the record
	// leaves the source, and no ordering can contaminate it.
	//
	// # What it copies
	//
	// A shallow copy of the map. The top-level fields are isolated from what the
	// chain does afterwards, which is where the transformers write. A NESTED
	// value stays shared -- a transformer that changes a sub-object's contents
	// changes the snapshot too. No built-in transformer does that.
	Snapshot string
}

func (s Source) validate() error {
	if s.From == nil {
		return fmt.Errorf("Source.From is required: pass an origin, such as " +
			"from.HTTP{URL: \"https://api.example.com/v1/events\"}")
	}
	return nil
}

// options folds the Source into what every driver receives.
func (s Source) options(run RunContext) core.ReadOptions {
	return core.ReadOptions{
		Preview:       s.Preview,
		PreviewBytes:  s.PreviewBytes,
		PreviewWriter: s.PreviewWriter,
		Stats:         s.Stats,
		Run:           run,
	}
}
