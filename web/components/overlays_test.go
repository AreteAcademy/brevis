package components

import (
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// A DRAWER AND A MODAL ARE NATIVE <dialog>s, NAMED BY THEIR TITLE.
//
// showModal() gives focus containment, Escape and an inert page for free;
// what the element cannot know is its own name, so the heading's id is the
// dialog's aria-labelledby. The close button is a `method="dialog"` form,
// which closes it with no script.
func TestADrawerAndAModalAreNamedDialogs(t *testing.T) {
	for kind, html := range map[string]string{
		"drawer": draw(t, Drawer("d-x", "Configure x", nil)),
		"modal":  draw(t, Modal("m-x", "Run x now", nil)),
	} {
		for _, want := range []string{
			`<dialog id="` + kind[:1] + `-x"`,
			`class="` + kind,
			`aria-labelledby="` + kind[:1] + `-x-title"`,
			`id="` + kind[:1] + `-x-title"`,
			`method="dialog"`,
			`aria-label="Close"`,
			"data-dialog-body",
		} {
			if !strings.Contains(html, want) {
				t.Errorf("%s lacks %s: %s", kind, want, html)
			}
		}
		if strings.Contains(html, "autofocus") {
			t.Errorf("%s focuses its close button: the first field is where the reader starts", kind)
		}
	}
}

// A FOOTER IS WHERE A DRAWER'S ACTIONS STAY, outside the scrolling body.
func TestADrawerFooterSitsOutsideItsBody(t *testing.T) {
	html := draw(t, Drawer("d-y", "Y", templ.Raw(`<button>Run</button>`)))
	body, foot := strings.Index(html, "data-dialog-body"), strings.Index(html, "data-dialog-foot")
	if foot < 0 || foot < body || !strings.Contains(html[foot:], "<button>Run</button>") {
		t.Errorf("the footer is not after the body with its actions: %s", html)
	}
}

// A TOAST IS A STATE AND A SENTENCE, and an action when there is one.
func TestAToastSaysWhatHappenedWithItsState(t *testing.T) {
	html := draw(t, Toast("fail", "Run of orders failed", "View logs", "/runs/1"))
	for _, want := range []string{`class="toast`, "st-diamond", "Run of orders failed", `href="/runs/1"`, ">View logs<"} {
		if !strings.Contains(html, want) {
			t.Errorf("the toast lacks %s: %s", want, html)
		}
	}
	if plain := draw(t, Toast("ok", "Paused", "", "")); strings.Contains(plain, "<a") {
		t.Errorf("a toast with no action draws a link: %s", plain)
	}
}

// A SWITCH IS ANNOUNCED AS ONE: role=switch with its state, and a name.
func TestASwitchSaysItIsASwitchAndWhetherItIsOn(t *testing.T) {
	on, off := draw(t, Switch("Live", true, nil)), draw(t, Switch("Live", false, nil))
	for html, state := range map[string]string{on: "true", off: "false"} {
		for _, want := range []string{`type="button"`, `role="switch"`, `aria-checked="` + state + `"`, `aria-label="Live"`} {
			if !strings.Contains(html, want) {
				t.Errorf("the switch lacks %s: %s", want, html)
			}
		}
	}
}

// A MENU BADGE IS A COUNT THAT NEEDS SOMEBODY, OR A PULSE THAT IS LIVE.
func TestANavBadgeIsACountOrAPulse(t *testing.T) {
	fail, live := draw(t, NavBadge("fail", "2")), draw(t, NavBadge("live", "3"))
	if !strings.Contains(fail, "nav-badge-fail") || !strings.Contains(fail, ">2<") {
		t.Errorf("the failing badge = %s", fail)
	}
	if !strings.Contains(live, "nav-badge-live") || !strings.Contains(live, "st-pulse") || !strings.Contains(live, "3") {
		t.Errorf("the live badge = %s", live)
	}
}
