package postgres

import (
	"encoding/json"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// TestToJSONRowByRow covers §3.1's table from the plan, one case per line.
//
// It is not inference: it is a written, reviewable table, and every choice here
// has
// a concrete cost when it is wrong -- which is why each case says what it is.
func TestToJSONRowByRow(t *testing.T) {
	casos := []struct {
		name  string
		input any
		want  string // the JSON that comes out
		why   string
	}{
		{
			"NULL vira null", nil, `null`,
			"nil e ausencia; qualquer outro valor inventaria dado",
		},
		{
			"NUMERIC vira string",
			pgtype.Numeric{Int: big.NewInt(123456789012345678), Exp: -2, Valid: true},
			`"1234567890123456.78"`,
			"float64 perde centavos em valores grandes, e o prejuizo aparece meses depois",
		},
		{
			"NUMERIC negativo",
			pgtype.Numeric{Int: big.NewInt(-1050), Exp: -2, Valid: true},
			`"-10.50"`,
			"",
		},
		{
			"NUMERIC nulo", pgtype.Numeric{}, `null`, "",
		},
		{
			"DATE sem hora",
			pgtype.Date{Time: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), Valid: true},
			`"2026-09-05"`,
			"00:00:00 e uma hora que ninguem escreveu, e ela anda um dia ao converter de fuso",
		},
		{
			"TIMESTAMPTZ em RFC 3339",
			pgtype.Timestamptz{Time: time.Date(2026, 9, 5, 12, 30, 0, 0, time.UTC), Valid: true},
			`"2026-09-05T12:30:00Z"`,
			"",
		},
		{
			"TIMESTAMPTZ noutro fuso vira UTC",
			pgtype.Timestamptz{
				Time:  time.Date(2026, 9, 5, 9, 30, 0, 0, time.FixedZone("BRT", -3*3600)),
				Valid: true,
			},
			`"2026-09-05T12:30:00Z"`,
			"dois registros do mesmo instante em fusos diferentes tem de sair iguais",
		},
		{
			"BYTEA em base64", []byte{0xde, 0xad, 0xbe, 0xef}, `"3q2+7w=="`,
			"encoding/json ja faz, e o alternativo seria um array de numeros",
		},
		{
			"JSONB aninhado",
			json.RawMessage(`{"a":[1,2],"b":{"c":true}}`),
			`{"a":[1,2],"b":{"c":true}}`,
			"reserializar viraria string com JSON dentro, e quem consome decodificaria duas vezes",
		},
		{
			"JSONB invalido vira string",
			json.RawMessage(`{isso nao e json`),
			`"{isso nao e json"`,
			"o dado existe; recusar aqui perderia o registro inteiro por causa de uma coluna",
		},
		{
			"UUID em texto",
			pgtype.UUID{Bytes: [16]byte{0x17, 0x8d, 0x0b, 0x49, 0xde, 0xce, 0x57, 0x38,
				0xb8, 0xeb, 0xf5, 0xca, 0xe2, 0x22, 0x1a, 0xea}, Valid: true},
			`"178d0b49-dece-5738-b8eb-f5cae2221aea"`,
			"cru viraria um array de 16 numeros",
		},
		{
			"UUID nulo", pgtype.UUID{}, `null`, "",
		},
		{
			"array vira array",
			[]any{int64(1), int64(2), nil},
			`[1,2,null]`,
			"",
		},
		{
			"array de NUMERIC converte elemento a elemento",
			[]any{pgtype.Numeric{Int: big.NewInt(150), Exp: -2, Valid: true}},
			`["1.50"]`,
			"sem recursao, o elemento sairia como objeto do pgtype",
		},
		{
			"INET em texto", net.ParseIP("10.0.0.1"), `"10.0.0.1"`, "",
		},
		{
			"texto passa direto", "ola", `"ola"`, "",
		},
		{
			"inteiro passa direto", int64(42), `42`, "",
		},
		{
			"booleano passa direto", true, `true`, "",
		},
	}

	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(ParaJSON(c.input))
			if err != nil {
				t.Fatalf("serializando: %v", err)
			}
			if string(b) != c.want {
				msg := "ParaJSON(%v) = %s, want %s"
				if c.why != "" {
					msg += "\n  " + c.why
				}
				t.Errorf(msg, c.input, b, c.want)
			}
		})
	}
}

// TestNumericLosesNoPrecision is the case that motivates the most important row
// of the
// table: a value a float64 cannot represent.
func TestNumericLosesNoPrecision(t *testing.T) {
	// 9007199254740993 = 2^53 + 1, the first integer a float64 cannot hold.
	n := pgtype.Numeric{Int: big.NewInt(9007199254740993), Exp: 0, Valid: true}

	got := ParaJSON(n)
	if got != "9007199254740993" {
		t.Errorf("ParaJSON = %v (%T), want a string exata", got, got)
	}

	// And the proof of the opposite, measured at run time: through a float64 the
	// value changes. Written with a variable and not a constant on purpose --
	// as a constant Go compares at compile time with arbitrary
	// arbitraria, e a demonstracao vira sempre verdadeira.
	exato := int64(9007199254740993)
	pelaFloat := int64(float64(exato))
	if pelaFloat == exato {
		t.Fatal("float64 representou 2^53+1 nesta plataforma; a escolha de string precisa " +
			"ser revista, ou este teste deixou de provar o que diz")
	}
	if b, _ := json.Marshal(float64(exato)); string(b) == "9007199254740993" {
		t.Error("o caminho por float64 nao perdeu precisao; o teste nao esta provando nada")
	}
}
