package components

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/web/i18n"
	"github.com/a-h/templ"
)

func drawIn(t *testing.T, ctx context.Context, c templ.Component) string {
	t.Helper()
	var b bytes.Buffer
	if err := c.Render(ctx, &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// THE SHARED COMPONENTS SPEAK THE PAGE'S LANGUAGE: a state's word, a
// dialog's close button, a monogram's name, a short date.
func TestTheSharedComponentsSpeakThePagesLanguage(t *testing.T) {
	pt := i18n.WithLang(context.Background(), "pt-BR")
	es := i18n.WithLang(context.Background(), "es")
	at := time.Date(2026, 10, 29, 9, 5, 0, 0, time.UTC)
	for _, c := range []struct {
		html string
		want string
	}{
		{drawIn(t, pt, Status("failed")), ">falhou<"},
		{drawIn(t, es, Status("on_time")), ">al día<"},
		{drawIn(t, pt, StatusMark("stale")), `aria-label="desatualizado"`},
		{drawIn(t, pt, State("")), ">nunca rodou<"},
		{drawIn(t, pt, Freshness("late")), ">atrasado<"},
		{drawIn(t, pt, Modal("m", "x", nil)), `aria-label="Fechar"`},
		{drawIn(t, pt, Logo("file", 18)), `aria-label="Arquivos locais"`},
		{drawIn(t, es, ShortStamp(&at)), ">29 oct 09:05<"},
	} {
		if !strings.Contains(c.html, c.want) {
			t.Errorf("want %s in %s", c.want, c.html)
		}
	}
}

// AND ENGLISH IS WHAT IT WAS: the old formatters, which the screens still
// call without a context, print exactly what they printed before.
func TestTheOldFormattersStillSpeakEnglish(t *testing.T) {
	d := 1500 * time.Millisecond
	if got := Duration(&d); got != "1.5s" {
		t.Errorf("Duration = %q", got)
	}
	ago := time.Now().Add(-3 * time.Minute)
	if got := When(&ago); got != "3 min ago" {
		t.Errorf("When = %q", got)
	}
}
