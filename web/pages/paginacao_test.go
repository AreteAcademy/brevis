package pages

import "testing"

func TestPaginationCounts(t *testing.T) {
	p := Pagination{Page: 3, PorPagina: 25, Total: 63}
	if p.Paginas() != 3 {
		t.Errorf("paginas = %d, want 3", p.Paginas())
	}
	if p.First() != 51 || p.Last() != 63 {
		t.Errorf("intervalo = %d–%d, want 51–63", p.First(), p.Last())
	}

	// An empty list must not say "1-0 of 0".
	vazia := Pagination{Page: 1, PorPagina: 25, Total: 0}
	if vazia.First() != 0 || vazia.Paginas() != 1 {
		t.Errorf("vazia: primeiro=%d paginas=%d", vazia.First(), vazia.Paginas())
	}
}

// The window does not list a hundred pages: it shows five, centred on the current
// one, and reaches the edges without shrinking.
func TestThePageWindow(t *testing.T) {
	casos := []struct {
		page, total int
		esperado    []int
	}{
		{1, 250, []int{1, 2, 3, 4, 5}},
		{7, 250, []int{5, 6, 7, 8, 9}},
		{10, 250, []int{6, 7, 8, 9, 10}},
		{1, 50, []int{1, 2}},
	}
	for _, c := range casos {
		p := Pagination{Page: c.page, PorPagina: 25, Total: c.total}
		j := p.Window()
		if len(j) != len(c.esperado) {
			t.Fatalf("pagina %d de %d: %v, want %v", c.page, p.Paginas(), j, c.esperado)
		}
		for i := range j {
			if j[i] != c.esperado[i] {
				t.Fatalf("pagina %d: %v, want %v", c.page, j, c.esperado)
			}
		}
	}
}

// Changing the filter goes back to page 1: staying on page 7 of a result that now
// has two would be an empty screen with no explanation.
func TestChangingTheFilterResetsThePage(t *testing.T) {
	f := Filter{Tag: "acme", Page: 7, PorPagina: DefaultPerPage}
	if u := f.With("state", "failed"); containsText(u, "page=") {
		t.Errorf("URL %q manteve a pagina ao trocar o filtro", u)
	}
	// Navegar entre paginas preserva o resto do filtro.
	u := f.WithPage(3)
	if !containsText(u, "tag=acme") || !containsText(u, "page=3") {
		t.Errorf("URL de pagina = %q", u)
	}
}

// Terceiro clique no mesmo cabecalho remove a ordenacao.
func TestWithAnOrderItTogglesAndThenClears(t *testing.T) {
	f := Filter{PorPagina: DefaultPerPage}
	primeiro := f.WithSort("last")
	if !containsText(primeiro, "sort=last") || containsText(primeiro, "dir=desc") {
		t.Errorf("first click = %q, wanted ascending", primeiro)
	}

	f.Sort = "last"
	if segundo := f.WithSort("last"); !containsText(segundo, "dir=desc") {
		t.Errorf("second click = %q, wanted descending", segundo)
	}

	f.Desc = true
	if terceiro := f.WithSort("last"); containsText(terceiro, "sort=") {
		t.Errorf("third click = %q, wanted no sorting", terceiro)
	}
	if f.Arrow("last") != "↓" || f.Arrow("workflow") != "" {
		t.Error("the arrow only shows on the sorted column")
	}
}

func TestTheRunFilterRemovesThePeriod(t *testing.T) {
	f := RunFilter{State: "failed", De: "2026-09-01T02:00:00Z", Ate: "2026-09-01T03:00:00Z"}
	if !f.Active() {
		t.Error("a filter with a period should count as active")
	}
	if u := f.With("state", ""); containsText(u, "state=") {
		t.Errorf("remover o estado deixou %q", u)
	}
	if !containsText(f.With("state", ""), "from=") {
		t.Error("removing the state must not take the period with it")
	}
	if (RunFilter{}).Active() {
		t.Error("an empty filter is not active")
	}
}

func containsText(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
