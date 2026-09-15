package from

import (
	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Over expands a list of values into one source per value.
//
//	From: from.Many{
//	    Discover: func(ctx context.Context) ([]sdk.Reader, error) {
//	        cred, err := credential()
//	        if err != nil {
//	            return nil, err
//	        }
//	        return from.Over(brazilUFs, func(uf string) sdk.Reader {
//	            return inventorySource(cred, uf)
//	        }), nil
//	    },
//	    Workers: 6,
//	    OnError: sdk.ContinueOnError,
//	}
//
// It exists because an API with no "everything" endpoint is common: the
// inventory is read one federative unit at a time, one municipality, one
// account, one day. The loop that builds those sources is the same four lines
// in every fetcher that meets one.
//
// # Why it builds the READER and not the URL
//
// The obvious shape is a parameter: name a query key, give it values, let the
// SDK write the query string. It was asked for that way, and the fetchers that
// asked said no.
//
// Of the four places this pattern appears, one varies a query parameter with a
// static list, one varies a query parameter whose values are DISCOVERED at run
// time, and one puts its value in the PATH -- its query parameter is the same
// in every source. A feature keyed on query parameters reaches half of them.
//
// And the one that does vary a query parameter needs a dialect: keys that are
// accented phrases, spaces as %20 rather than +, commas left literal.
// url.Values.Encode writes + and would break precisely the case that asked for
// this. Handing back a Reader means the SDK never holds the string, so no
// vendor's encoding is ever its business -- the constraint disappears rather
// than being configured around.
//
// # What it deliberately does not do
//
// No workers, no failure policy, no laziness of its own: it returns a slice,
// and from.Many owns all three. A fan-out that carried its own worker pool
// would be a second copy of the one that already works.
//
// The cartesian product of two lists is absent for the same reason the
// parameter is: nothing needs it yet. Nesting two calls expresses it the day
// something does.
func Over[T any](values []T, build func(T) core.Reader) []core.Reader {
	if build == nil {
		// A nil builder yields a slice of nil Readers, and from.Many's message
		// would then name a source index -- pointing at the list rather than at
		// the missing function. Better to say it here, where it is true.
		panic("from.Over: build is nil, so there is nothing to make a source with")
	}

	// nil for an empty list, NOT an empty non-nil slice. from.Many already
	// separates "nothing to read" from "did not know where to read", and its
	// message for an empty Discover is the one a caller should get; returning
	// something non-nil here would not change that, but returning nil keeps the
	// two indistinguishable at the only place that inspects them.
	if len(values) == 0 {
		return nil
	}

	sources := make([]core.Reader, 0, len(values))
	for _, v := range values {
		sources = append(sources, build(v))
	}
	return sources
}
