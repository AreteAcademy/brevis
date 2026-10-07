package components

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/a-h/templ"
)

// Spark draws a destination's last loads as a small line: one point per load,
// oldest on the left.
//
// A load whose step did not say how many rows it wrote leaves a GAP, not a
// point at zero. Absent is not zero, and a line dropping to the floor would
// tell an operator the table emptied overnight when the step merely did not
// count.
//
// Hand-written SVG with only numbers in it, so there is nothing to escape.
func Spark(values []*int64) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		known := 0
		var top int64
		for _, v := range values {
			if v != nil {
				known++
				top = max(top, *v)
			}
		}
		if known < 2 {
			_, err := io.WriteString(w, `<span class="text-xs text-muted">—</span>`)
			return err
		}

		const width, height, pad = 120.0, 24.0, 2.0
		step := (width - 2*pad) / float64(len(values)-1)
		y := func(v int64) float64 {
			if top == 0 {
				return height - pad
			}
			return height - pad - float64(v)/float64(top)*(height-2*pad)
		}

		var path strings.Builder
		pen := false
		var lastX, lastY float64
		for i, v := range values {
			if v == nil {
				pen = false
				continue
			}
			x, yy := pad+float64(i)*step, y(*v)
			if pen {
				fmt.Fprintf(&path, " L%.1f %.1f", x, yy)
			} else {
				fmt.Fprintf(&path, " M%.1f %.1f", x, yy)
				pen = true
			}
			lastX, lastY = x, yy
		}
		_, err := fmt.Fprintf(w,
			`<svg class="text-gold-strong" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" role="img" aria-label="rows per load, last %d loads">`+
				`<path d="%s" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/>`+
				`<circle cx="%.1f" cy="%.1f" r="2" fill="currentColor"/></svg>`,
			width, height, width, height, len(values), strings.TrimSpace(path.String()), lastX, lastY)
		return err
	})
}
