package bigquery

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// bigquery.Table is an adapter: it translates its fields into the LoadConfig
// the load package consumes. A LoadConfig field it never writes is a
// adjustment the facade's consumer has no way to make -- and nothing breaks,
// nada avisa.
//
// Aconteceu de verdade: a fase 0 parou de repassar Provider e Entity, e toda
// table created since then came out with no cost-attribution labels. No
// test saw it, because no row count changes.
//
// This test reads both files and compares. It is crude on purpose: it
// fails when somebody adds a field to LoadConfig and forgets to wire it up
// here, which is exactly when you want to be bothered.
func TestEveryLoadConfigFieldIsReachable(t *testing.T) {
	campos := camposDe(t, "../../internal/core/types.go", "LoadConfig")
	escritos := escritosPor(t, "bigquery.go")

	// These the adapter does not write, and for stated reasons.
	naoSeAplica := map[string]string{
		"Format":   "sempre ndjson: é o único formato que o load escreve",
		"Provider": "vem do lote, não do Target -- ver Write",
		"Entity":   "vem do lote, não do Target -- ver Write",
	}

	var faltando []string
	for _, c := range campos {
		if escritos[c] {
			continue
		}
		if _, ok := naoSeAplica[c]; ok {
			continue
		}
		faltando = append(faltando, c)
	}

	if len(faltando) > 0 {
		sort.Strings(faltando)
		t.Errorf("LoadConfig tem %s, e bigquery.Table não os define nem os declara "+
			"inaplicáveis. Quem usa a fachada não consegue ajustá-los, e nada avisa.",
			strings.Join(faltando, ", "))
	}
}

var (
	campoRe   = regexp.MustCompile(`(?m)^\t([A-Z][A-Za-z0-9]*)\s`)
	escritaRe = regexp.MustCompile(`(?m)^\t+([A-Z][A-Za-z0-9]*):`)
	atribRe   = regexp.MustCompile(`cfg\.([A-Z][A-Za-z0-9]*)\s*(,|=)`)
)

func camposDe(t *testing.T, caminho, tipo string) []string {
	t.Helper()
	data, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("lendo %s: %v", caminho, err)
	}
	s := string(data)
	i := strings.Index(s, "type "+tipo+" struct {")
	if i < 0 {
		t.Fatalf("%s não achado em %s", tipo, caminho)
	}
	corpo := s[i:]
	corpo = corpo[:strings.Index(corpo, "\n}")]

	var out []string
	for _, m := range campoRe.FindAllStringSubmatch(corpo, -1) {
		out = append(out, m[1])
	}
	return out
}

func escritosPor(t *testing.T, caminho string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("lendo %s: %v", caminho, err)
	}
	s := string(data)

	out := map[string]bool{}
	for _, m := range escritaRe.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	for _, m := range atribRe.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}
