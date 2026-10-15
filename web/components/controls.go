package components

import "github.com/a-h/templ"

// ButtonKind is what a button is for, and therefore how loud it is.
type ButtonKind int

const (
	// ButtonSecondary is the default: bordered, quiet, most buttons.
	ButtonSecondary ButtonKind = iota
	// ButtonPrimary is the one thing a screen most wants done: ink on the
	// ground, the loudest thing that is not a colour.
	ButtonPrimary
	// ButtonDanger is a verb that loses something.
	ButtonDanger
	// ButtonAccent is the brand's colour, and it is spent sparingly: the
	// workbench's Run, which is the one verb that screen exists for.
	ButtonAccent
)

// ButtonSize is a button's height.
type ButtonSize int

const (
	SizeRegular ButtonSize = iota // 32px
	SizeSmall                     // 28px, in a table row or a dense toolbar
)

const buttonBase = "inline-flex shrink-0 items-center justify-center gap-1.5 rounded-card font-medium whitespace-nowrap transition-colors duration-150 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent-strong disabled:pointer-events-none disabled:opacity-50"

// ButtonClass is a button's look, for a control that is not a Button -- a
// link that opens a dialog and has to carry its own attributes.
func ButtonClass(k ButtonKind, s ButtonSize) string { return buttonClass(k, s) }

func buttonClass(k ButtonKind, s ButtonSize) string {
	size := "h-8 px-3 text-[0.82rem]"
	if s == SizeSmall {
		size = "h-7 px-2.5 text-[0.78rem]"
	}
	var kind string
	switch k {
	case ButtonPrimary:
		kind = "bg-ink text-canvas hover:bg-ink/90"
	case ButtonDanger:
		kind = "border border-line bg-surface text-state-failed hover:bg-state-failed/10"
	case ButtonAccent:
		kind = "bg-accent-strong text-canvas hover:bg-accent-strong/90"
	default:
		kind = "border border-line bg-surface text-ink hover:bg-surface-2"
	}
	return buttonBase + " " + size + " " + kind
}

// buttonType takes `type` out of the caller's attributes, so a button asked
// to submit is drawn with ONE type. Anything else passes through untouched.
func buttonType(attrs templ.Attributes) (string, templ.Attributes) {
	typ, rest := "button", templ.Attributes{}
	for k, v := range attrs {
		if k == "type" {
			if s, ok := v.(string); ok && s != "" {
				typ = s
			}
			continue
		}
		rest[k] = v
	}
	return typ, rest
}

// SegOption is one choice in a segmented filter: a link, so the filter is a
// URL and works with no script.
type SegOption struct {
	Label, Href string
	// Count is printed beside the label when set: how many rows the choice
	// would show. A string so the caller decides "3" or "3+".
	Count   string
	Current bool
}

// Tab is one tab in a tablist.
type Tab struct {
	Key, Label string
	Selected   bool
}

func ariaBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
