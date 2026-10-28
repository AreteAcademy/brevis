package i18n

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// style is how a language writes numbers and dates. A small table rather than
// CLDR: three languages, and the console prints a handful of shapes.
type style struct {
	thousands, decimal string
	months, days       [12]string // days uses its first seven, Sunday first
	monthFirst         bool       // "Oct 29" rather than "29 out"
	dayComma           bool       // "Thu, Oct 29, 2026" rather than "qui, 29 out 2026"
}

var styles = map[string]style{
	"en": {
		thousands: ",", decimal: ".", monthFirst: true, dayComma: true,
		months: [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
		days:   [12]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	},
	"pt-BR": {
		thousands: ".", decimal: ",",
		months: [12]string{"jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"},
		days:   [12]string{"dom", "seg", "ter", "qua", "qui", "sex", "sáb"},
	},
	"es": {
		thousands: ".", decimal: ",",
		months: [12]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"},
		days:   [12]string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"},
	},
}

func styleOf(ctx context.Context) style { return styles[Lang(ctx)] }

// Number is an integer with the language's thousands separator.
func Number(ctx context.Context, n int64) string {
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(styleOf(ctx).thousands)
		}
		b.WriteRune(r)
	}
	return sign + b.String()
}

// Decimal is f to places decimals with the language's decimal mark. It does
// not group thousands: it is for a ratio or a second, not a count.
func Decimal(ctx context.Context, f float64, places int) string {
	return strings.Replace(strconv.FormatFloat(f, 'f', places, 64), ".", styleOf(ctx).decimal, 1)
}

// Date writes t, in t's own location, in one of the console's shapes, 24-hour
// in every language:
//
//	short  Oct 02 09:05   02 out 09:05    a past instant in a list (When)
//	brief  Oct 2 09:05    2 out 09:05     a timestamp cell (ShortStamp)
//	day    Thu, Oct 29, 2026 / qui, 29 out 2026   a calendar day
func Date(ctx context.Context, t time.Time, layout string) string {
	s := styleOf(ctx)
	month := s.months[t.Month()-1]
	clock := t.Format("15:04")
	switch layout {
	case "short", "brief":
		day := strconv.Itoa(t.Day())
		if layout == "short" {
			day = fmt.Sprintf("%02d", t.Day())
		}
		if s.monthFirst {
			return month + " " + day + " " + clock
		}
		return day + " " + month + " " + clock
	case "day":
		weekday := s.days[t.Weekday()]
		if s.dayComma {
			return fmt.Sprintf("%s, %s %d, %d", weekday, month, t.Day(), t.Year())
		}
		return fmt.Sprintf("%s, %d %s %d", weekday, t.Day(), month, t.Year())
	}
	return t.Format(time.RFC3339)
}

// Since says how far t is from now, up to a day -- "3 min ago", "in 5 min",
// "now" -- and the short date after it: "3 min ago" answers the operations
// question, a date the audit one.
func Since(ctx context.Context, now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		return T(ctx, "time.in", "span", span(-d))
	case d < time.Minute:
		return T(ctx, "time.now")
	case d < 24*time.Hour:
		return T(ctx, "time.ago", "span", span(d))
	}
	return Date(ctx, t.Local(), "short")
}

// span is a distance in its largest whole unit. The units read the same in
// all three languages, so they are not in the catalog.
func span(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// Elapsed is a duration in the unit its size calls for: "0.4s" says more than
// "400ms" beside a column that takes minutes.
func Elapsed(ctx context.Context, d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return Decimal(ctx, d.Seconds(), 1) + "s"
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}
