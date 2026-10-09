package layouts

import "context"

// The data tools travel in the CONTEXT, like the brand and the operator do.
//
// The bar has to know whether `/sql` exists, and the bar is drawn by Base,
// which every page calls with a Page it builds itself. Threading a boolean
// through nine of those would mean nine signatures changing for one question
// nobody passes on purpose -- and the one that forgot would offer a section
// that answers "not configured".
//
// ABSENT MEANS OFF, which is the safe half: a caller that never sets it gets
// a console with no SQL section, and a section that does not exist cannot
// mislead anybody.
type toolsKey struct{}

// WithSQL says whether this request's console has a SQL service to reach.
func WithSQL(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, toolsKey{}, on)
}

// HasSQL reads it back.
func HasSQL(ctx context.Context) bool {
	on, _ := ctx.Value(toolsKey{}).(bool)
	return on
}
