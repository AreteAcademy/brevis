package mysql

import (
	"encoding/json"
	"testing"
	"time"
)

// TestToJSONRowByRow covers the type table, one case per line.
//
// database/sql returns []byte for nearly everything when read into an `any`, so
// the
// conversion comes from the column's DECLARED TYPE. Without it every DECIMAL
// would become
// base64 in the JSON, and so would an INT.
func TestToJSONRowByRow(t *testing.T) {
	casos := []struct {
		name      string
		value     any
		declarado string
		quero     string
		porque    string
	}{
		{"NULL", nil, "VARCHAR", `null`, ""},
		{
			"DECIMAL vira string", []byte("123456789012345678.99"), "DECIMAL",
			`"123456789012345678.99"`,
			"float64 perde centavos em valores grandes",
		},
		{"BIGINT vira numero", []byte("42"), "BIGINT", `42`, "sem isto viraria base64"},
		{"INT vira numero", []byte("7"), "INT", `7`, ""},
		{"DOUBLE vira numero", []byte("1.5"), "DOUBLE", `1.5`, ""},
		{"VARCHAR vira texto", []byte("ola"), "VARCHAR", `"ola"`, ""},
		{
			"JSON aninhado", []byte(`{"a":[1,2]}`), "JSON", `{"a":[1,2]}`,
			"reserializar viraria string com JSON dentro",
		},
		{
			"JSON invalido vira string", []byte(`{quebrado`), "JSON", `"{quebrado"`,
			"o dado existe; recusar perderia o registro inteiro por uma coluna",
		},
		{"BLOB em base64", []byte{0xde, 0xad}, "BLOB", `"3q0="`, ""},
		{"VARBINARY em base64", []byte{0xde, 0xad}, "VARBINARY", `"3q0="`, ""},
		{"DATE como texto", []byte("2026-09-05"), "DATE", `"2026-09-05"`, ""},
		{
			"DATETIME sem parseTime", []byte("2026-09-05 12:30:00"), "DATETIME",
			`"2026-09-05T12:30:00Z"`,
			"este e o caminho que existe quando o DSN nao tem parseTime",
		},
		{"ENUM vira texto", []byte("ativo"), "ENUM", `"ativo"`, ""},
	}

	for _, c := range casos {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(ToJSON(c.value, c.declarado))
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != c.quero {
				t.Errorf("= %s, esperado %s\n  %s", b, c.quero, c.porque)
			}
		})
	}
}

// TestTheTwoInstantPathsAgree e a razao de o fallback de texto
// exist, and the proof that it is not dead code: with or without parseTime, the
// mesmo instante sai igual.
//
// This test also prevents the lying comment that had been written -- that
// without parseTime the instant would become base64. It does not; what changes
// is the cost.
func TestTheTwoInstantPathsAgree(t *testing.T) {
	instante := time.Date(2026, 9, 5, 12, 30, 0, 0, time.UTC)

	comParse := ToJSON(instante, "DATETIME")
	semParse := ToJSON([]byte("2026-09-05 12:30:00"), "DATETIME")

	if comParse != semParse {
		t.Errorf("os caminhos divergem: com parseTime %v, sem %v", comParse, semParse)
	}
	if comParse != "2026-09-05T12:30:00Z" {
		t.Errorf("= %v", comParse)
	}
}

// TestADateGainsNoTime: 00:00:00 is a time nobody wrote, and it walks a day on
// the first timezone conversion.
func TestADateGainsNoTime(t *testing.T) {
	d := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	if got := ToJSON(d, "DATE"); got != "2026-09-05" {
		t.Errorf("DATE = %v, expected no time", got)
	}
}
