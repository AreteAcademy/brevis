package components

import (
	"fmt"

	"github.com/a-h/templ"
)

// Stat is one number in a Stats strip.
type Stat struct {
	Label, Value string
	// Unit is printed small beside the number ("s", "GB"); Note faint, on
	// the value's line (density-base DB4).
	Unit, Note string
	// Icon is a glyph name for the label; empty draws none.
	Icon string
	// Dot is a state's colour (a `bg-state-*` class) for a dot before the
	// label; empty draws none.
	Dot string
	// Bar is an optional thin bar under the value, in segments.
	Bar []BarSegment
	// Sub is faint text beside the label ("· 3 loads").
	Sub string
	// Tone is a text colour class for the value, for a number that needs a
	// look (`text-state-failed`); empty is the ink.
	Tone string
	// Body is drawn under the label in place of a value: a small chart.
	Body templ.Component
	// Title is the cell's tooltip: how to read what it shows.
	Title string
}

// BarSegment is one part of a Stat's bar: its share of the whole (0..1) and
// its colour class.
type BarSegment struct {
	Share float64
	Class string
}

// width is the segment's share as a CSS width.
func (b BarSegment) width() string {
	return fmt.Sprintf("width:%.0f%%", b.Share*100)
}

// statsCols lays the strip out by how many numbers it holds: two side by
// side, three in a row from small screens up, four as two rows of two until
// there is room for one.
func statsCols(n int) string {
	if n == 1 {
		return "grid-cols-1"
	}
	// Cells of at least 150px, as many to a row as fit: four numbers are one
	// row on a desktop and two on a phone with no rule per count.
	return "grid-cols-[repeat(auto-fit,minmax(150px,1fr))]"
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

// KVItem is one cell of a key-value grid: a machine's key, its value, and a
// note on what it means.
type KVItem struct {
	Key, Value, Note string
}
