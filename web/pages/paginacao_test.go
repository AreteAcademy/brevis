package pages

import "testing"

func TestPaginacaoContas(t *testing.T) {
	p := Pagination{Page: 3, PorPagina: 25, Total: 63}
	if p.Paginas() != 3 {
		t.Errorf("paginas = %d, quero 3", p.Paginas())
	}
	if p.First() != 51 || p.Last() != 63 {
		t.Errorf("intervalo = %d–%d, quero 51–63", p.First(), p.Last())
	}

	// Lista vazia nao pode dizer "1–0 de 0".
	vazia := Pagination{Page: 1, PorPagina: 25, Total: 0}
	if vazia.First() != 0 || vazia.Paginas() != 1 {
		t.Errorf("vazia: primeiro=%d paginas=%d", vazia.First(), vazia.Paginas())
	}
}

// A janela nao lista cem paginas: mostra cinco, centradas na atual, e encosta
// nas bordas sem encolher.
func TestJanelaDePaginas(t *testing.T) {
	casos := []struct {
		pagina, total int
		esperado      []int
	}{
		{1, 250, []int{1, 2, 3, 4, 5}},
		{7, 250, []int{5, 6, 7, 8, 9}},
		{10, 250, []int{6, 7, 8, 9, 10}},
		{1, 50, []int{1, 2}},
	}
	for _, c := range casos {
		p := Pagination{Page: c.pagina, PorPagina: 25, Total: c.total}
		j := p.Window()
		if len(j) != len(c.esperado) {
			t.Fatalf("pagina %d de %d: %v, quero %v", c.pagina, p.Paginas(), j, c.esperado)
		}
		for i := range j {
			if j[i] != c.esperado[i] {
				t.Fatalf("pagina %d: %v, quero %v", c.pagina, j, c.esperado)
			}
		}
	}
}

// Trocar de filtro volta para a pagina 1: continuar na pagina 7 de um resultado
// que agora tem duas seria uma tela vazia sem explicacao.
func TestTrocarFiltroReiniciaPagina(t *testing.T) {
	f := Filter{Tag: "acme", Page: 7, PorPagina: DefaultPerPage}
	if u := f.With("state", "failed"); contemTexto(u, "page=") {
		t.Errorf("URL %q manteve a pagina ao trocar o filtro", u)
	}
	// Navegar entre paginas preserva o resto do filtro.
	u := f.WithPage(3)
	if !contemTexto(u, "tag=acme") || !contemTexto(u, "page=3") {
		t.Errorf("URL de pagina = %q", u)
	}
}

// Terceiro clique no mesmo cabecalho remove a ordenacao.
func TestComOrdemAlternaEDepoisLimpa(t *testing.T) {
	f := Filter{PorPagina: DefaultPerPage}
	primeiro := f.WithSort("last")
	if !contemTexto(primeiro, "sort=last") || contemTexto(primeiro, "dir=desc") {
		t.Errorf("primeiro clique = %q, queria ascendente", primeiro)
	}

	f.Sort = "last"
	if segundo := f.WithSort("last"); !contemTexto(segundo, "dir=desc") {
		t.Errorf("segundo clique = %q, queria descendente", segundo)
	}

	f.Desc = true
	if terceiro := f.WithSort("last"); contemTexto(terceiro, "sort=") {
		t.Errorf("terceiro clique = %q, queria sem ordenacao", terceiro)
	}
	if f.Arrow("last") != "↓" || f.Arrow("workflow") != "" {
		t.Error("a seta so aparece na coluna ordenada")
	}
}

func TestFiltroDeRunsRemovePeriodo(t *testing.T) {
	f := RunFilter{State: "failed", De: "2026-09-01T02:00:00Z", Ate: "2026-09-01T03:00:00Z"}
	if !f.Active() {
		t.Error("filtro com periodo deveria contar como ativo")
	}
	if u := f.With("state", ""); contemTexto(u, "state=") {
		t.Errorf("remover o estado deixou %q", u)
	}
	if !contemTexto(f.With("state", ""), "from=") {
		t.Error("remover o estado nao pode levar o periodo junto")
	}
	if (RunFilter{}).Active() {
		t.Error("filtro vazio nao esta ativo")
	}
}

func contemTexto(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
