package branding_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/branding"
)

func writeFile(t *testing.T, conteudo string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "brand.yaml")
	if err := os.WriteFile(path, []byte(conteudo), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A missing file is the NORMAL case -- the default installation has none. Failing
// here would take the container down over an optional customization.
func TestAMissingFileUsesTheDefault(t *testing.T) {
	m, err := branding.Load(filepath.Join(t.TempDir(), "nao-existe.yaml"))
	if err != nil {
		t.Fatalf("an absence became an error: %v", err)
	}
	if m.Title != "Brevis" {
		t.Errorf("titulo = %q", m.Title)
	}
	if m.CSS() != "" {
		t.Error("the default theme should emit no CSS -- the compiled sheet already has it")
	}
}

// A missing field inherits the default: a two-line file is a valid file.
func TestMissingFieldsInheritTheDefault(t *testing.T) {
	m, err := branding.Load(writeFile(t, "title: Acme Dados\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Acme Dados" {
		t.Errorf("titulo = %q", m.Title)
	}
	if m.Subtitle != branding.Default().Subtitle {
		t.Errorf("subtitulo perdido: %q", m.Subtitle)
	}
	if m.Theme.Succeeded != "#4c7a56" {
		t.Errorf("cor herdada perdida: %q", m.Theme.Succeeded)
	}
}

func TestACustomThemeBecomesCSS(t *testing.T) {
	m, err := branding.Load(writeFile(t, `
title: Acme
theme:
  ink: "#101820"
  accent: "#c02a2a"
  success: "#0f8f4f"
`))
	if err != nil {
		t.Fatal(err)
	}
	css := m.CSS()
	for _, expected := range []string{
		"--color-ink:#101820",
		"--color-gold:#c02a2a",
		"--color-state-success:#0f8f4f",
		// The derived ones follow the chosen ink and accent, with alpha.
		"--color-line:#101820" + "1a",
		"--color-gold-wash:#c02a2a" + "14",
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("the CSS is missing %q:\n%s", expected, css)
		}
	}
}

// The values go inside a <style>. A free string there could close the declaration
// and inject CSS -- on an operations panel, enough to hide a state
// de falha atras de um seletor.
func TestAnInvalidColourIsRefused(t *testing.T) {
	venenos := []string{
		`theme: {ink: "red"}`,
		`theme: {ink: "#12345"}`,
		`theme: {accent: "#fff;} body{display:none} .x{color:#fff"}`,
		`theme: {success: "url(http://exemplo/x)"}`,
	}
	for _, v := range venenos {
		if _, err := branding.Load(writeFile(t, v)); err == nil {
			t.Errorf("aceitou cor invalida: %s", v)
		}
	}
}

func TestAnEmptyTitleIsRefused(t *testing.T) {
	if _, err := branding.Load(writeFile(t, `title: "   "`)); err == nil {
		t.Error("a blank title would leave the sidebar with no name")
	}
}

func TestABrokenYamlFallsBackToTheDefault(t *testing.T) {
	m, err := branding.Load(writeFile(t, "title: [this: does\n  not close"))
	if err == nil {
		t.Error("yaml invalido deveria ser reportado")
	}
	if m.Title != "Brevis" {
		t.Errorf("an error should return the usable default, got %q", m.Title)
	}
}

// The line break is the author's: turning it into a space would change the text's
// rhythm.
func TestThePhrasePreservesTheLines(t *testing.T) {
	m, err := branding.Load(writeFile(t, "phrase: |\n  Primeira\n  Segunda\n  Terceira\n"))
	if err != nil {
		t.Fatal(err)
	}
	rows := m.Lines()
	if len(rows) != 3 || rows[0] != "Primeira" || rows[2] != "Terceira" {
		t.Errorf("linhas = %q", rows)
	}
	sem, _ := branding.Load(writeFile(t, `phrase: ""`))
	if len(sem.Lines()) != 0 {
		t.Error("an empty phrase should render no line at all")
	}
}

// With no brand in the context -- a test rendering directly, a path that did not
// go through render -- the screen still has to have a name.
func TestAContextWithNoBrandReturnsTheDefault(t *testing.T) {
	if branding.De(context.Background()).Title != "Brevis" {
		t.Error("contexto vazio deveria devolver the default")
	}
	ctx := branding.IntoContext(context.Background(), branding.Brand{Title: "Acme"})
	if branding.De(ctx).Title != "Acme" {
		t.Error("the brand from the context was ignored")
	}
}

// The attribution is not configurable: there is no YAML field able to remove
// it.
func TestTheAttributionDoesNotComeFromConfiguration(t *testing.T) {
	if branding.Attribution != "Powered by Brevis" {
		t.Errorf("atribuicao = %q", branding.Attribution)
	}
	// Since an unknown field became an error, the guarantee got stronger: the
	// attempt is not ignored, it is refused naming the field.
	if _, err := branding.Load(writeFile(t, "title: Acme\natribuicao: Powered by Acme\n")); err == nil {
		t.Error("a made-up field in the YAML got through in silence")
	}
	m, err := branding.Load(writeFile(t, "title: Acme\n"))
	if err != nil {
		t.Fatal(err)
	}
	if branding.Attribution != "Powered by Brevis" || m.Title != "Acme" {
		t.Error("a field in the YAML must not replace the attribution")
	}
}

// A LIGHT THEME OF THE CLIENT'S OWN IS WRITTEN FOR THE LIGHT THEME ONLY.
//
// `theme:` has always been the console's colours, and the console was dark;
// a client's dark ground written into the light theme would be dark text on
// dark. So `theme_light:` is its own block, under the attribute the server
// writes on <html>, and the dark one never reaches it.
func TestALightThemeIsWrittenUnderTheLightAttribute(t *testing.T) {
	m, err := branding.Load(writeFile(t, `
title: Acme
theme:
  ink: "#101820"
theme_light:
  ink: "#202830"
  accent_strong: "#3a4a10"
`))
	if err != nil {
		t.Fatal(err)
	}
	css := m.CSS()
	i := strings.Index(css, `:root[data-theme="light"]{`)
	if i < 0 {
		t.Fatalf("no light block in:\n%s", css)
	}
	dark, light := css[:i], css[i:]
	if !strings.Contains(light, "--color-ink:#202830") || !strings.Contains(light, "--color-gold-strong:#3a4a10") {
		t.Errorf("the light block is missing the client's light colours:\n%s", light)
	}
	if strings.Contains(light, "#101820") {
		t.Errorf("the dark ink reached the light block:\n%s", light)
	}
	if !strings.Contains(dark, "--color-ink:#101820") {
		t.Errorf("the dark block lost the client's ink:\n%s", dark)
	}
}

// WITHOUT ONE, NOTHING IS WRITTEN FOR LIGHT, and the product's light theme
// applies -- including over a client's `theme:`, which describes a dark
// console. And the hand-written ground gradient is the dark theme's alone.
func TestWithoutALightThemeTheLightOneIsTheProducts(t *testing.T) {
	m, err := branding.Load(writeFile(t, `
title: Acme
theme:
  background: "#101010"
`))
	if err != nil {
		t.Fatal(err)
	}
	css := m.CSS()
	if strings.Contains(css, `data-theme="light"]{--`) {
		t.Errorf("a light block was written with no theme_light:\n%s", css)
	}
	if strings.Contains(css, "}body{") {
		t.Errorf("the ground gradient applies to every theme, the light one included:\n%s", css)
	}
	if !strings.Contains(css, `:root:not([data-theme="light"]) body{`) {
		t.Errorf("the ground gradient is not scoped to the dark theme:\n%s", css)
	}
}

func TestAnInvalidLightColourIsRefused(t *testing.T) {
	if _, err := branding.Load(writeFile(t, `theme_light: {ink: "#fff;} body{display:none"}`)); err == nil {
		t.Error("an injected light colour was accepted")
	}
}

// THE DOCUMENTATION IS READ IN THE READER'S LANGUAGE: English at the root,
// every other under its own prefix.
func TestDocsInFollowsTheLanguage(t *testing.T) {
	for lang, want := range map[string]string{
		"en":    "https://brevis.sh/docs/workflows/",
		"pt-BR": "https://brevis.sh/pt/docs/workflows/",
		"zh-CN": "https://brevis.sh/zh/docs/workflows/",
		"fr":    "https://brevis.sh/fr/docs/workflows/",
	} {
		if got := branding.DocsIn(lang, "workflows/"); got != want {
			t.Errorf("DocsIn(%q) = %q, want %q", lang, got, want)
		}
	}
}
