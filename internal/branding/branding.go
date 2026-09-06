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
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Brand is an installation's identity.
type Brand struct {
	// Title appears in the sidebar and in the pages' <title>.
	Title string `yaml:"titulo"`

	// Subtitle is the small-caps line under the title.
	Subtitle string `yaml:"subtitulo"`

	// Frase is the quotation in the sidebar's footer. Multiple lines are
	// preserved -- the break is part of the text's rhythm.
	Frase string `yaml:"frase"`

	// Logo is the graphic mark next to the title. It accepts an absolute URL
	// (the customer's hosted logo) or an internal path starting at `/assets/`.
	//
	// Empty falls back to the embedded symbol, and the default is internal on
	// purpose: a logo that depends on an external host disappears when that host
	// goes down, when the cluster has no route to the internet, or when the
	// customer reorganises their own site. An operations tool's screen must not
	// break over that.
	Logo string `yaml:"logo"`

	Theme Theme `yaml:"tema"`
}

// Theme is the colours. Each field maps to a CSS variable Tailwind already
// emits; overriding them at runtime repaints the whole interface without
// recompiling CSS, because EVERY utility resolves its colour through
// `var(--color-*)`.
type Theme struct {
	Fundo         string `yaml:"fundo"`
	FundoSuave    string `yaml:"fundo_suave"`
	Superficie    string `yaml:"superficie"`
	Tinta         string `yaml:"tinta"`
	TextoSuave    string `yaml:"texto_suave"`
	Accent        string `yaml:"destaque"`
	DestaqueForte string `yaml:"destaque_forte"`

	Sucesso    string `yaml:"sucesso"`
	Falha      string `yaml:"falha"`
	Executando string `yaml:"executando"`
	Fila       string `yaml:"fila"`
	Repetindo  string `yaml:"repetindo"`
	Cancelado  string `yaml:"cancelado"`
	Aguardando string `yaml:"aguardando"`
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
		Frase:    "Clarity, structure and virtue\nare also part\nof whoever builds.",
		Theme: Theme{
			Fundo:         "#f4efe4",
			FundoSuave:    "#fbf8f1",
			Superficie:    "#fffdf8",
			Tinta:         "#21180f",
			TextoSuave:    "#6e6254",
			Accent:        "#aa8450",
			DestaqueForte: "#8a693d",
			Sucesso:       "#4c7a56",
			Falha:         "#b0503c",
			Executando:    "#3f6d8f",
			Fila:          "#b3822f",
			Repetindo:     "#a35f28",
			Cancelado:     "#8a8175",
			Aguardando:    "#a89b8a",
		},
	}
}

// Load reads the brand file. Absence is NOT an error: the default
// installation has no file at all, and requiring one would make the container
// fail at boot over an optional customisation.
//
// Missing fields inherit the default, so a two-line file -- just the name and
// the phrase -- is a valid file.
func Load(caminho string) (Brand, error) {
	m := Default()
	if caminho == "" {
		return m, nil
	}
	conteudo, err := os.ReadFile(caminho)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return m, fmt.Errorf("reading %s: %w", caminho, err)
	}
	// Decodes ONTO the default: yaml.v3 only writes the fields present in the
	// file, so the rest survives.
	if err := yaml.Unmarshal(conteudo, &m); err != nil {
		return Default(), fmt.Errorf("%s: invalid yaml: %w", caminho, err)
	}
	if err := m.Validate(); err != nil {
		return Default(), fmt.Errorf("%s: %w", caminho, err)
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
	for nome, cor := range m.Theme.cores() {
		if !hex.MatchString(cor) {
			return fmt.Errorf("colour %s: %q is not a hex value (#rgb, #rrggbb or #rrggbbaa)", nome, cor)
		}
	}
	if err := validarLogo(m.Logo); err != nil {
		return err
	}
	return nil
}

// validarLogo accepts only https://, http:// and an internal path.
//
// For the same reason as the colours: the value goes into an <img>'s `src`. A
// `javascript:` or a `data:text/html,...` there runs script in the session of
// whoever opened the panel -- and whoever edits the brand file may not be
// whoever operates the cluster. The list is an allowlist, not a blocklist:
// refusing `javascript:` by name lets through the next scheme somebody
// invents.
func validarLogo(logo string) error {
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

func (t Theme) cores() map[string]string {
	return map[string]string{
		"fundo": t.Fundo, "fundo_suave": t.FundoSuave, "superficie": t.Superficie,
		"tinta": t.Tinta, "texto_suave": t.TextoSuave,
		"destaque": t.Accent, "destaque_forte": t.DestaqueForte,
		"sucesso": t.Sucesso, "falha": t.Falha, "executando": t.Executando,
		"fila": t.Fila, "repetindo": t.Repetindo, "cancelado": t.Cancelado,
		"aguardando": t.Aguardando,
	}
}

// CSS returns the variables to inject into the <head>.
//
// Empty when the theme is the default: the compiled sheet already carries those
// values, and repeating them would be bytes on every page to change nothing.
func (m Brand) CSS() string {
	padrao := Default().Theme
	if m.Theme == padrao {
		return ""
	}

	var b strings.Builder
	b.WriteString(":root{")
	escreve := func(variavel, valor string) {
		if valor != "" {
			fmt.Fprintf(&b, "%s:%s;", variavel, valor)
		}
	}
	escreve("--color-parchment", m.Theme.Fundo)
	escreve("--color-parchment-soft", m.Theme.FundoSuave)
	escreve("--color-surface", m.Theme.Superficie)
	escreve("--color-ink", m.Theme.Tinta)
	escreve("--color-muted", m.Theme.TextoSuave)
	escreve("--color-gold", m.Theme.Accent)
	escreve("--color-gold-strong", m.Theme.DestaqueForte)

	// Derived: line and highlight are the same colour with transparency.
	// Computing them here, rather than asking the customer, keeps them from
	// configuring a border that clashes with the ink they picked.
	escreve("--color-line", comAlfa(m.Theme.Tinta, "1a"))
	escreve("--color-line-soft", comAlfa(m.Theme.Tinta, "0d"))
	escreve("--color-gold-wash", comAlfa(m.Theme.Accent, "14"))

	escreve("--color-state-success", m.Theme.Sucesso)
	escreve("--color-state-failed", m.Theme.Falha)
	escreve("--color-state-running", m.Theme.Executando)
	escreve("--color-state-queued", m.Theme.Fila)
	escreve("--color-state-retrying", m.Theme.Repetindo)
	escreve("--color-state-canceled", m.Theme.Cancelado)
	escreve("--color-state-pending", m.Theme.Aguardando)

	// The body's background is a hand-written gradient in the source CSS, so it
	// does not follow the variables on its own.
	fmt.Fprintf(&b, "}body{background:linear-gradient(180deg,%s 0%%,%s 44%%,%s 100%%);}",
		m.Theme.FundoSuave, m.Theme.Fundo, m.Theme.FundoSuave)
	return b.String()
}

// comAlfa appends the alpha channel to a 6-digit colour. Short formats, or ones
// that already carry alpha, are returned untouched — mixing channels would give
// a wrong colour instead of a visible error.
func comAlfa(cor, alfa string) string {
	if len(cor) != 7 {
		return cor
	}
	return cor + alfa
}

// Lines splits the sentence for the template. The break is the author's, and
// turning it into a space would change the text's rhythm in the sidebar.
func (m Brand) Lines() []string {
	var out []string
	for _, l := range strings.Split(m.Frase, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

type chave struct{}

// IntoContext injeta a marca no contexto da requisicao.
//
// The context, and not a parameter on every template: every page needs the
// brand, and adding it to ten components' signatures just to reach the base
// layout would make each new screen a chance to forget.
func IntoContext(ctx context.Context, m Brand) context.Context {
	return context.WithValue(ctx, chave{}, m)
}

// De recovers the brand. With no brand in the context — a test rendering
// directly, a path that did not pass through the middleware — it returns the
// default rather than a screen with no name at all.
func De(ctx context.Context) Brand {
	if m, ok := ctx.Value(chave{}).(Brand); ok {
		return m
	}
	return Default()
}
