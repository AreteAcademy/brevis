package api_test

import (
	"strings"
	"testing"
)

// shellOf is the shell alone -- the sidebar, the top bar and the dialogs
// every page carries -- without the page's own content, which its screen's
// module translates.
func shellOf(body string) string {
	main, end := strings.Index(body, "<main"), strings.Index(body, "</main>")
	if main < 0 || end < 0 {
		return body
	}
	return body[:main] + body[end:]
}

// THE SHELL SPEAKS THE READER'S LANGUAGE: the sections and their hints, the
// switcher, the palette's opener, the account menu, the drawer's controls,
// the theme and shortcut buttons, the shortcuts list and the palette.
func TestTheShellIsDrawnInTheReadersLanguage(t *testing.T) {
	ui := dataUI(catalogFake{}).WithEndpoints(endpointsFake{})
	for lang, want := range map[string][]string{
		"pt-BR": {
			`aria-label="Monitorar"`, ">Visão geral<", ">Execuções<", ">Tempo real<", ">Cargas batch<",
			`aria-label="Dados"`, ">Projetos<", "Buscar ou ir para…", "Documentação",
			`aria-label="Fechar a navegação"`, `aria-label="Abrir a navegação"`,
			`aria-label="Tema"`, `aria-label="Automático"`, "Atalhos de teclado",
			"Ir para Visão geral", `placeholder="Buscar workflows, destinos, endpoints…"`,
			"A saúde de tudo, o que falhou e o que dispara a seguir",
			// The palette's footer: each key, then what it does.
			">navegar<", ">abrir<", ">em nova aba<",
		},
		"es": {
			`aria-label="Monitorear"`, ">Resumen<", ">Ejecuciones<", ">Tiempo real<",
			`aria-label="Datos"`, ">Proyectos<", "Buscar o ir a…", "Documentación",
			`aria-label="Tema"`, `aria-label="Automático"`, "Atajos de teclado", "Ir a Resumen",
		},
	} {
		body := shellOf(pageIn(t, ui, "/ingest/loads", lang, "", false))
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: the shell lacks %s", lang, w)
			}
		}
	}
}

// AND NO ENGLISH IS LEFT IN IT. Every sentence the shell said in English
// before this module, looked for in the Portuguese shell: one that survives
// is a string somebody forgot to move to the catalog.
func TestNoEnglishIsLeftInTheTranslatedShell(t *testing.T) {
	body := shellOf(pageIn(t, dataUI(catalogFake{}).WithEndpoints(endpointsFake{}), "/ingest/loads", "pt-BR", "", false))
	for _, en := range []string{
		">Overview<", ">Runs<", ">Streams<", ">Loads<", `aria-label="Monitor"`, `aria-label="Orchestrate"`, `aria-label="Ingest"`,
		">Projects<", "Search or jump to", "Documentation", ">Account<", "Close navigation", "Open navigation",
		"Switch to the", `aria-label="Keyboard shortcuts"`, "Go to ", "This list", "None of these fire",
		"Search the console", "Search workflows", " navigate<", " open<", "in a new tab", "Every project and",
		"The health of everything",
	} {
		if strings.Contains(body, en) {
			t.Errorf("the Portuguese shell still says %q", en)
		}
	}
}
