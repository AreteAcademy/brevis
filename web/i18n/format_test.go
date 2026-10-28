package i18n

import (
	"context"
	"testing"
	"time"
)

func in(lang string) context.Context { return WithLang(context.Background(), lang) }

// NUMBERS READ THE WAY THE READER WRITES THEM: English groups thousands with
// a comma and splits decimals with a point; Portuguese and Spanish the other
// way round.
func TestNumbersFollowTheLanguage(t *testing.T) {
	for lang, want := range map[string][2]string{
		"en": {"1,209", "1.5"}, "pt-BR": {"1.209", "1,5"}, "es": {"1.209", "1,5"},
	} {
		if got := Number(in(lang), 1209); got != want[0] {
			t.Errorf("%s: Number(1209) = %q", lang, got)
		}
		if got := Decimal(in(lang), 1.5, 1); got != want[1] {
			t.Errorf("%s: Decimal(1.5) = %q", lang, got)
		}
	}
	if got := Number(in("en"), -1234567); got != "-1,234,567" {
		t.Errorf("a negative million = %q", got)
	}
	if got := Number(in("pt-BR"), 999); got != "999" {
		t.Errorf("three digits = %q", got)
	}
}

// A DATE IN THE READER'S ORDER AND WORDS, 24-hour in all three.
func TestDatesFollowTheLanguage(t *testing.T) {
	at := time.Date(2026, 10, 29, 9, 5, 0, 0, time.UTC)
	for lang, want := range map[string][3]string{
		"en":    {"Oct 29 09:05", "Oct 29 09:05", "Thu, Oct 29, 2026"},
		"pt-BR": {"29 out 09:05", "29 out 09:05", "qui, 29 out 2026"},
		"es":    {"29 oct 09:05", "29 oct 09:05", "jue, 29 oct 2026"},
	} {
		for i, layout := range []string{"short", "brief", "day"} {
			if got := Date(in(lang), at, layout); got != want[i] {
				t.Errorf("%s %s = %q, want %q", lang, layout, got, want[i])
			}
		}
	}
	early := time.Date(2026, 3, 2, 4, 0, 0, 0, time.UTC)
	if got := Date(in("en"), early, "short"); got != "Mar 02 04:00" {
		t.Errorf("short pads the day: %q", got)
	}
	if got := Date(in("en"), early, "brief"); got != "Mar 2 04:00" {
		t.Errorf("brief does not: %q", got)
	}
}

// RELATIVE TIME, up to a day, and a date after it -- the same boundary the
// console has always drawn, in each language.
func TestSinceSaysHowLongAgoInTheLanguage(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		lang string
		at   time.Time
		want string
	}{
		{"en", now.Add(-20 * time.Second), "now"},
		{"en", now.Add(-3 * time.Minute), "3 min ago"},
		{"en", now.Add(5 * time.Minute), "in 5 min"},
		{"en", now.Add(-2 * time.Hour), "2h ago"},
		{"pt-BR", now.Add(-3 * time.Minute), "há 3 min"},
		{"pt-BR", now.Add(5 * time.Minute), "em 5 min"},
		{"pt-BR", now.Add(-20 * time.Second), "agora"},
		{"es", now.Add(-3 * time.Minute), "hace 3 min"},
		{"es", now.Add(5 * time.Minute), "en 5 min"},
		{"pt-BR", now.Add(-50 * time.Hour), Date(in("pt-BR"), now.Add(-50*time.Hour).Local(), "short")},
	} {
		if got := Since(in(c.lang), now, c.at); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.lang, now.Sub(c.at), got, c.want)
		}
	}
}

// A DURATION KEEPS ITS UNITS -- ms, s, m, h read the same in all three -- and
// takes the language's decimal mark.
func TestElapsedTakesTheDecimalMark(t *testing.T) {
	for _, c := range []struct {
		lang string
		d    time.Duration
		want string
	}{
		{"en", 400 * time.Millisecond, "400ms"},
		{"en", 1500 * time.Millisecond, "1.5s"},
		{"pt-BR", 1500 * time.Millisecond, "1,5s"},
		{"en", 3*time.Minute + 4*time.Second, "3m 04s"},
		{"es", 2*time.Hour + 5*time.Minute, "2h 05m"},
	} {
		if got := Elapsed(in(c.lang), c.d); got != c.want {
			t.Errorf("%s %v = %q, want %q", c.lang, c.d, got, c.want)
		}
	}
}
