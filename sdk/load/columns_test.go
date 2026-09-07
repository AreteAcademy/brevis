package load

import (
	"strings"
	"testing"

	"cloud.google.com/go/bigquery"
)

// Criterion 2: a declared column neither Transform nor Metadata delivers.
func TestColumnsRefusesAColumnTheTableLacks(t *testing.T) {
	schema := bigquery.Schema{
		{Name: "ingestion_id", Type: bigquery.StringFieldType},
		{Name: "payload", Type: bigquery.JSONFieldType},
	}

	err := checkDeclaredAgainstTable(
		[]string{"ingestion_id", "payload", "provider"}, schema, "bronze.pedidos")
	if err == nil {
		t.Fatal("uma coluna declarada que a tabela não tem é uma carga que não funciona")
	}
	for _, want := range []string{"provider", "bronze.pedidos", "ingestion_id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("o erro não menciona %q (precisa nomear os dois lados): %v", want, err)
		}
	}
}

// Asymmetric, like reconcile: a table column the declaration omits stays
// NULL, and a landing table legitimately has those.
func TestColumnsAcceptsAnUndeclaredTableColumn(t *testing.T) {
	schema := bigquery.Schema{
		{Name: "ingestion_id", Type: bigquery.StringFieldType},
		{Name: "payload", Type: bigquery.JSONFieldType},
		{Name: "source_key", Type: bigquery.StringFieldType},
	}
	if err := checkDeclaredAgainstTable([]string{"ingestion_id", "payload"}, schema, "t"); err != nil {
		t.Errorf("coluna da tabela que a declaração não lista fica NULL, e isso é legítimo: %v", err)
	}
}
