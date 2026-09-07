package execution

import (
	"strings"
	"testing"
)

func TestJanelaGuardaTudoQuandoCabe(t *testing.T) {
	var j janela
	for _, l := range []string{"primeira", "segunda", "terceira"} {
		j.Escrever(l)
	}
	if got := j.String(); got != "primeira\nsegunda\nterceira\n" {
		t.Errorf("saida = %q", got)
	}
}

// The ceiling is what stops a `while true; do echo` from filling Postgres's
// disk.
func TestJanelaRespeitaOTeto(t *testing.T) {
	var j janela
	linha := strings.Repeat("x", 200)
	for i := 0; i < 5000; i++ { // ~1 MB
		j.Escrever(linha)
	}
	if n := len(j.String()); n > TetoDoLog+512 {
		t.Errorf("guardou %d bytes; o teto e %d", n, TetoDoLog)
	}
}

// As duas pontas precisam sobreviver: o comeco traz o comando e a configuracao,
// the end carries the reason for the failure. Keeping only one of them loses
// half the
// diagnostico.
func TestJanelaGuardaAsDuasPontas(t *testing.T) {
	var j janela
	j.Escrever("COMECO-DA-SAIDA")
	enchimento := strings.Repeat("y", 500)
	for i := 0; i < 3000; i++ {
		j.Escrever(enchimento)
	}
	j.Escrever("ERRO-NO-FIM")

	got := j.String()
	if !strings.Contains(got, "COMECO-DA-SAIDA") {
		t.Error("perdeu o comeco — some o comando que rodou")
	}
	if !strings.Contains(got, "ERRO-NO-FIM") {
		t.Error("perdeu o fim — some o motivo da falha")
	}
}

// Truncating in silence makes the reader conclude the program stopped there.
func TestJanelaAvisaOQueCortou(t *testing.T) {
	var j janela
	for i := 0; i < 4000; i++ {
		j.Escrever(strings.Repeat("z", 300))
	}
	if !strings.Contains(j.String(), "omitted by the") {
		t.Error("cortou sem avisar")
	}
}
