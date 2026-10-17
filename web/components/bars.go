package components

import (
	"context"
	"fmt"
	"html"
	"io"

	"github.com/a-h/templ"
)

// LoadBar is one load on a destination's page.
type LoadBar struct {
	Href  string
	Rows  *int64
	Title string
}

// LoadBars draws a destination's loads, oldest first, each bar a link to its run,
// followed by `missed` dashed bars: slots that went by without a load.
//
// A load that did not say how many rows it wrote is drawn hollow at full
// height -- present, uncounted -- and never as a zero; one that wrote zero is
// a red sliver.
func LoadBars(bars []LoadBar, missed int) templ.Component {
	return LoadBarsSized(bars, missed, "h-11")
}

// LoadBarsSized is LoadBars at another height class: 26px inside a KPI cell
// (screen-load-v2 LV2), 44px on its own.
func LoadBarsSized(bars []LoadBar, missed int, size string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		n := len(bars) + missed
		if n == 0 {
			_, err := io.WriteString(w, `<p class="text-xs text-muted">No load yet.</p>`)
			return err
		}
		const height, gap = 96.0, 3.0
		width := float64(n*14 + (n-1)*int(gap))
		if width < 200 {
			width = 200
		}
		bw := (width - gap*float64(n-1)) / float64(n)
		var top int64
		for _, b := range bars {
			if b.Rows != nil {
				top = max(top, *b.Rows)
			}
		}
		// 44px tall at about 22px a bar (density-base DB5): the width follows
		// the number of loads, so three loads are three slim bars and not three
		// slabs stretched across the panel. The viewBox is 17 units a bar.
		if _, err := fmt.Fprintf(w, `<svg class="%s" style="width:min(100%%,%.0fpx)" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" role="img" aria-label="rows per load, oldest first">`, size, width*25/17, width, height); err != nil {
			return err
		}
		for i, b := range bars {
			x := float64(i) * (bw + gap)
			// Hollow at full height when the load did not count: present, uncounted.
			shape := fmt.Sprintf(`<rect x="%.1f" y="1" width="%.1f" height="%.1f" fill="none" stroke="currentColor" stroke-width="1" class="text-line-strong"/>`, x, bw, height-2)
			if b.Rows != nil {
				h := 2.0
				if top > 0 {
					h = max(2.0, float64(*b.Rows)/float64(top)*(height-2))
				}
				// Zero is red: counted, and empty -- the load a reader is
				// looking for.
				fill := "fill-gold-strong"
				if *b.Rows == 0 {
					fill = "fill-state-failed"
				}
				shape = fmt.Sprintf(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" class="%s"/>`, x, height-h, bw, h, fill)
			}
			if _, err := fmt.Fprintf(w, `<a href="%s"><title>%s</title>%s</a>`,
				html.EscapeString(b.Href), html.EscapeString(b.Title), shape); err != nil {
				return err
			}
		}
		for i := 0; i < missed; i++ {
			x := float64(len(bars)+i) * (bw + gap)
			if _, err := fmt.Fprintf(w, `<rect x="%.1f" y="1" width="%.1f" height="%.1f" fill="none" stroke="currentColor" stroke-width="1" stroke-dasharray="3 2" class="text-state-failed"><title>a scheduled load that did not arrive</title></rect>`, x, bw, height-2); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, `</svg>`)
		return err
	})
}
