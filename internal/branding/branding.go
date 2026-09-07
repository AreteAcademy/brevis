// Package branding loads the installation's visual identity.
//
// It exists because the interface will be used by different customers, and each
// wants its own name, its own phrase and its own colours. What is NOT
// customisable is the "Powered by Brevis" attribution: it does not come from
// configuration, it comes from the code, and so there is no YAML value capable
// of removing it.
//
// Choosing YAML follows the rest of the project -- workflows are YAML, and a
// second configuration mechanism (a database, a panel, environment variables
// for twenty colours) would be a new way of doing the same thing.
package branding

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Brand is an installation's identity.
type Brand struct {
	// Title appears in the sidebar and in the pages' <title>.
	Title string `yaml:"title"`

	// Subtitle is the small-caps line under the title.
	Subtitle string `yaml:"subtitle"`

	// Phrase is the quotation in the sidebar's footer. Multiple lines are
	// preserved -- the break is part of the text's rhythm.
	Phrase string `yaml:"phrase"`

	// Logo is the graphic mark next to the title. It accepts an absolute URL
	// (the customer's hosted logo) or an internal path starting at `/assets/`.
	//
	// Empty falls back to the embedded symbol, and the default is internal on
	// purpose: a logo that depends on an external host disappears when that host
	// goes down, when the cluster has no route to the internet, or when the
	// customer reorganises their own site. An operations tool's screen must not
	// break over that.
	Logo string `yaml:"logo"`

	Theme Theme `yaml:"theme"`
}

// Theme is the colours. Each field maps to a CSS variable Tailwind already
// emits; overriding them at runtime repaints the whole interface without
// recompiling CSS, because EVERY utility resolves its colour through
// `var(--color-*)`.
type Theme struct {
	Background     string `yaml:"background"`
	BackgroundSoft string `yaml:"background_soft"`
	Surface        string `yaml:"surface"`
	Ink            string `yaml:"ink"`
	Muted          string `yaml:"muted"`
	Accent         string `yaml:"accent"`
	AccentStrong   string `yaml:"accent_strong"`

	Succeeded string `yaml:"success"`
	Failed    string `yaml:"failed"`
	Running   string `yaml:"running"`
	Queued    string `yaml:"queued"`
	Retrying  string `yaml:"retrying"`
	Canceled  string `yaml:"canceled"`
	Waiting   string `yaml:"pending"`

	// Skipped is a step whose trigger rule was not satisfied. It gets a colour
	// of its own and a MUTED one: a step that was correctly not run must not
	// read as something that went wrong, and it must not read as `pending`
	// either, which means "has not run yet".
	Skipped string `yaml:"skipped"`
}

// DefaultLogo is the embedded symbol, served from the binary itself.
const DefaultLogo = "/assets/logo.svg"

// Attribution is fixed. Not a configuration field, on purpose: it is the one
// thing on the screen the customer does not choose.
const Attribution = "Powered by Brevis"

// Default is the default identity, used when there is no brand file.
func Default() Brand {
	return Brand{
		Title:    "Brevis",
		Subtitle: "Orchestration",
		Logo:     DefaultLogo,
		Phrase:   "Clarity, structure and virtue\nare also part\nof whoever builds.",
		Theme: Theme{
			Background:     "#f4efe4",
			BackgroundSoft: "#fbf8f1",
			Surface:        "#fffdf8",
			Ink:            "#21180f",
			Muted:          "#6e6254",
			Accent:         "#aa8450",
			AccentStrong:   "#8a693d",
			Succeeded:      "#4c7a56",
			Failed:         "#b0503c",
			Running:        "#3f6d8f",
			Queued:         "#b3822f",
			Retrying:       "#a35f28",
			Canceled:       "#8a8175",
			Waiting:        "#a89b8a",
			// A dusty plum: distinct in HUE from the two warm greys beside it,
			// so "did not run" and "has not run yet" are told apart at a
			// glance, and desaturated enough that a graph full of correctly
			// skipped steps does not look alarming.
			Skipped: "#8b7089",
		},
	}
}

// Load reads the brand file. Absence is NOT an error: the default
// installation has no file at all, and requiring one would make the container
// fail at boot over an optional customisation.
//
// Missing fields inherit the default, so a two-line file -- just the name and
// the phrase -- is a valid file.
func Load(path string) (Brand, error) {
	m := Default()
	if path == "" {
		return m, nil
	}
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return m, fmt.Errorf("reading %s: %w", path, err)
	}
	// Decodes ONTO the default: yaml.v3 only writes the fields present in the
	// file, so the rest survives.
	//
	// KnownFields, so an unrecognized key is an ERROR. Before this, a typo --
	// or a file still using the pre-v0.7 Portuguese keys, `titulo:` and
	// `tema:` -- was ignored in silence, and the installation came up with the
	// default identity while somebody looked for the reason their colours had
	// not been applied.
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return Default(), fmt.Errorf("%s: %w (the field names are the ones in "+
			"brand.example.yaml; they became English in v0.7)", path, err)
	}
	if err := m.Validate(); err != nil {
		return Default(), fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

var hex = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

// Validate refuses a colour that is not hexadecimal.
//
// This is security, not purism: the theme's values are written inside a <style>
// block on the page. A free-form string there could close the declaration and
// inject arbitrary CSS -- which, on an operations panel, is capable of hiding a
// failure state behind a selector.
func (m Brand) Validate() error {
	if strings.TrimSpace(m.Title) == "" {
		return fmt.Errorf("the title cannot be empty")
	}
	for name, colour := range m.Theme.colours() {
		if !hex.MatchString(colour) {
			return fmt.Errorf("colour %s: %q is not a hex value (#rgb, #rrggbb or #rrggbbaa)", name, colour)
		}
	}
	if err := validateLogo(m.Logo); err != nil {
		return err
	}
	return nil
}

// validateLogo accepts only https://, http:// and an internal path.
//
// For the same reason as the colours: the value goes into an <img>'s `src`. A
// `javascript:` or a `data:text/html,...` there runs script in the session of
// whoever opened the panel -- and whoever edits the brand file may not be
// whoever operates the cluster. The list is an allowlist, not a blocklist:
// refusing `javascript:` by name lets through the next scheme somebody
// invents.
func validateLogo(logo string) error {
	if logo == "" {
		return nil
	}
	if strings.HasPrefix(logo, "/") && !strings.HasPrefix(logo, "//") {
		return nil
	}
	if strings.HasPrefix(logo, "https://") || strings.HasPrefix(logo, "http://") {
		return nil
	}
	return fmt.Errorf("logo %q: use https://, http:// or an internal path "+
		"starting with /", logo)
}

func (t Theme) colours() map[string]string {
	return map[string]string{
		// Keyed by the YAML NAME, not the Go field: the error this feeds names a
		// colour, and naming one the file does not contain sends whoever is
		// fixing it looking for a key that is not there.
		"background": t.Background, "background_soft": t.BackgroundSoft, "surface": t.Surface,
		"ink": t.Ink, "muted": t.Muted,
		"accent": t.Accent, "accent_strong": t.AccentStrong,
		"success": t.Succeeded, "failed": t.Failed, "running": t.Running,
		"queued": t.Queued, "retrying": t.Retrying, "canceled": t.Canceled,
		"pending": t.Waiting, "skipped": t.Skipped,
	}
}

// CSS returns the variables to inject into the <head>.
//
// Empty when the theme is the default: the compiled sheet already carries those
// values, and repeating them would be bytes on every page to change nothing.
func (m Brand) CSS() string {
	fallback := Default().Theme
	if m.Theme == fallback {
		return ""
	}

	var b strings.Builder
	b.WriteString(":root{")
	write := func(variable, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s:%s;", variable, value)
		}
	}
	write("--color-parchment", m.Theme.Background)
	write("--color-parchment-soft", m.Theme.BackgroundSoft)
	write("--color-surface", m.Theme.Surface)
	write("--color-ink", m.Theme.Ink)
	write("--color-muted", m.Theme.Muted)
	write("--color-gold", m.Theme.Accent)
	write("--color-gold-strong", m.Theme.AccentStrong)

	// Derived: line and highlight are the same colour with transparency.
	// Computing them here, rather than asking the customer, keeps them from
	// configuring a border that clashes with the ink they picked.
	write("--color-line", withAlpha(m.Theme.Ink, "1a"))
	write("--color-line-soft", withAlpha(m.Theme.Ink, "0d"))
	write("--color-gold-wash", withAlpha(m.Theme.Accent, "14"))

	write("--color-state-success", m.Theme.Succeeded)
	write("--color-state-failed", m.Theme.Failed)
	write("--color-state-running", m.Theme.Running)
	write("--color-state-queued", m.Theme.Queued)
	write("--color-state-retrying", m.Theme.Retrying)
	write("--color-state-canceled", m.Theme.Canceled)
	write("--color-state-pending", m.Theme.Waiting)

	// The body's background is a hand-written gradient in the source CSS, so it
	// does not follow the variables on its own.
	fmt.Fprintf(&b, "}body{background:linear-gradient(180deg,%s 0%%,%s 44%%,%s 100%%);}",
		m.Theme.BackgroundSoft, m.Theme.Background, m.Theme.BackgroundSoft)
	return b.String()
}

// withAlpha appends the alpha channel to a 6-digit colour. Short formats, or ones
// that already carry alpha, are returned untouched — mixing channels would give
// a wrong colour instead of a visible error.
func withAlpha(colour, alfa string) string {
	if len(colour) != 7 {
		return colour
	}
	return colour + alfa
}

// Lines splits the sentence for the template. The break is the author's, and
// turning it into a space would change the text's rhythm in the sidebar.
func (m Brand) Lines() []string {
	var out []string
	for _, l := range strings.Split(m.Phrase, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

type key struct{}

// IntoContext injeta a marca no contexto da requisicao.
//
// The context, and not a parameter on every template: every page needs the
// brand, and adding it to ten components' signatures just to reach the base
// layout would make each new screen a chance to forget.
func IntoContext(ctx context.Context, m Brand) context.Context {
	return context.WithValue(ctx, key{}, m)
}

// De recovers the brand. With no brand in the context — a test rendering
// directly, a path that did not pass through the middleware — it returns the
// default rather than a screen with no name at all.
func De(ctx context.Context) Brand {
	if m, ok := ctx.Value(key{}).(Brand); ok {
		return m
	}
	return Default()
}
