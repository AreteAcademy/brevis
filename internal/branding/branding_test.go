package branding_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/branding"
)

func escrever(t *testing.T, conteudo string) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "brand.yaml")
	if err := os.WriteFile(caminho, []byte(conteudo), 0o600); err != nil {
		t.Fatal(err)
	}
	return caminho
}

// Arquivo ausente e o caso NORMAL — a instalacao padrao nao tem nenhum. Falhar
// aqui derrubaria o container por causa de uma customizacao opcional.
func TestArquivoAusenteUsaOPadrao(t *testing.T) {
	m, err := branding.Load(filepath.Join(t.TempDir(), "nao-existe.yaml"))
	if err != nil {
		t.Fatalf("ausencia virou erro: %v", err)
	}
	if m.Title != "Brevis" {
		t.Errorf("titulo = %q", m.Title)
	}
	if m.CSS() != "" {
		t.Error("tema padrao nao deveria emitir CSS — a folha compilada ja o tem")
	}
}

// Campo ausente herda o padrao: um arquivo de duas linhas e um arquivo valido.
func TestCamposAusentesHerdamOPadrao(t *testing.T) {
	m, err := branding.Load(escrever(t, "title: Acme Dados\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Acme Dados" {
		t.Errorf("titulo = %q", m.Title)
	}
	if m.Subtitle != branding.Default().Subtitle {
		t.Errorf("subtitulo perdido: %q", m.Subtitle)
	}
	if m.Theme.Sucesso != "#4c7a56" {
		t.Errorf("cor herdada perdida: %q", m.Theme.Sucesso)
	}
}

func TestTemaCustomizadoViraCSS(t *testing.T) {
	m, err := branding.Load(escrever(t, `
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
	for _, esperado := range []string{
		"--color-ink:#101820",
		"--color-gold:#c02a2a",
		"--color-state-success:#0f8f4f",
		// Derivadas seguem a tinta e o destaque escolhidos, com alfa.
		"--color-line:#101820" + "1a",
		"--color-gold-wash:#c02a2a" + "14",
	} {
		if !strings.Contains(css, esperado) {
			t.Errorf("CSS sem %q:\n%s", esperado, css)
		}
	}
}

// Os valores vao para dentro de um <style>. String livre ali poderia fechar a
// declaracao e injetar CSS — num painel de operacao, capaz de esconder um estado
// de falha atras de um seletor.
func TestCorInvalidaEhRecusada(t *testing.T) {
	venenos := []string{
		`theme: {ink: "red"}`,
		`theme: {ink: "#12345"}`,
		`theme: {accent: "#fff;} body{display:none} .x{color:#fff"}`,
		`theme: {success: "url(http://exemplo/x)"}`,
	}
	for _, v := range venenos {
		if _, err := branding.Load(escrever(t, v)); err == nil {
			t.Errorf("aceitou cor invalida: %s", v)
		}
	}
}

func TestTituloVazioEhRecusado(t *testing.T) {
	if _, err := branding.Load(escrever(t, `title: "   "`)); err == nil {
		t.Error("titulo em branco deixaria a barra lateral sem nome")
	}
}

func TestYamlQuebradoVoltaAoPadrao(t *testing.T) {
	m, err := branding.Load(escrever(t, "title: [isto: nao\n  fecha"))
	if err == nil {
		t.Error("yaml invalido deveria ser reportado")
	}
	if m.Title != "Brevis" {
		t.Errorf("erro deveria devolver o padrao utilizavel, veio %q", m.Title)
	}
}

// A quebra de linha e do autor: virar espaco mudaria o ritmo do texto.
func TestFrasePreservaAsLinhas(t *testing.T) {
	m, err := branding.Load(escrever(t, "phrase: |\n  Primeira\n  Segunda\n  Terceira\n"))
	if err != nil {
		t.Fatal(err)
	}
	linhas := m.Lines()
	if len(linhas) != 3 || linhas[0] != "Primeira" || linhas[2] != "Terceira" {
		t.Errorf("linhas = %q", linhas)
	}
	sem, _ := branding.Load(escrever(t, `phrase: ""`))
	if len(sem.Lines()) != 0 {
		t.Error("frase vazia nao deveria render linha nenhuma")
	}
}

// Sem marca no contexto — um teste que renderiza direto, um caminho que nao
// passou pelo render — a tela precisa ter nome mesmo assim.
func TestContextoSemMarcaDevolveOPadrao(t *testing.T) {
	if branding.De(context.Background()).Title != "Brevis" {
		t.Error("contexto vazio deveria devolver o padrao")
	}
	ctx := branding.IntoContext(context.Background(), branding.Brand{Title: "Acme"})
	if branding.De(ctx).Title != "Acme" {
		t.Error("marca do contexto foi ignorada")
	}
}

// A atribuicao nao e configuravel: nao existe campo de YAML capaz de removê-la.
func TestAtribuicaoNaoVemDaConfiguracao(t *testing.T) {
	if branding.Attribution != "Powered by Brevis" {
		t.Errorf("atribuicao = %q", branding.Attribution)
	}
	// Desde que campo desconhecido virou erro, a garantia ficou mais forte: a
	// tentativa nao e ignorada, e recusada nomeando o campo.
	if _, err := branding.Load(escrever(t, "title: Acme\natribuicao: Powered by Acme\n")); err == nil {
		t.Error("um campo inventado no YAML passou calado")
	}
	m, err := branding.Load(escrever(t, "title: Acme\n"))
	if err != nil {
		t.Fatal(err)
	}
	if branding.Attribution != "Powered by Brevis" || m.Title != "Acme" {
		t.Error("um campo no YAML nao pode substituir a atribuicao")
	}
}
