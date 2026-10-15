package components

import "fmt"

// Stat is one number in a Stats strip.
type Stat struct {
	Label, Value string
	// Unit is printed small beside the number ("s", "GB"); Note under it.
	Unit, Note string
	// Icon is a glyph name for the label; empty draws none.
	Icon string
}

// statsCols lays the strip out by how many numbers it holds: two side by
// side, three in a row from small screens up, four as two rows of two until
// there is room for one.
func statsCols(n int) string {
	switch n {
	case 1:
		return "grid-cols-1"
	case 2:
		return "grid-cols-2"
	case 3:
		return "grid-cols-1 sm:grid-cols-3"
	}
	return "grid-cols-2 lg:grid-cols-4"
}

// Def is one row of a DefinitionList.
type Def struct {
	Term, Value string
	// Mono draws the value in the machine's face: a URI, a cron, a key.
	Mono bool
}

func footText(shown, total int, noun string) string {
	if shown == total {
		return fmt.Sprintf("%d %s", total, noun)
	}
	return fmt.Sprintf("Showing %d of %d %s", shown, total, noun)
}

func methodClass(m string) string {
	if m == "GET" {
		return "bg-state-success/13 text-state-success"
	}
	return "bg-surface-2 text-muted"
}
