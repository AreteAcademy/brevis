package mysql

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

// TestInsertSQLNamesTheColumns: the SQL asserted as a pure function, with no
// client.
// The reason is concrete -- BigQuery's MERGE shipped with a POSITIONAL match
// and
// cost v0.12.0 because it was built inside a method that held a client.
func TestInsertSQLNamesTheColumns(t *testing.T) {
	got := InsertSQL("pedidos", []string{"ingestion_id", "valor"}, 2, "")
	expected := "INSERT INTO `pedidos` (`ingestion_id`, `valor`) VALUES (?,?), (?,?)"
	if got != expected {
		t.Errorf("SQL:\n  got  %s\n  want %s", got, expected)
	}
}

// TestInsertSQLDedupIgnoresOnlyDuplicates: the dedup is a no-op ON DUPLICATE KEY
// UPDATE, never INSERT IGNORE, which would also swallow truncation, NOT NULL and
// bad-date errors.
func TestInsertSQLDedupIgnoresOnlyDuplicates(t *testing.T) {
	got := InsertSQL("t", []string{"a", "ingestion_id"}, 1, "ingestion_id")
	want := "INSERT INTO `t` (`a`, `ingestion_id`) VALUES (?,?) ON DUPLICATE KEY UPDATE `ingestion_id` = `ingestion_id`"
	if got != want {
		t.Errorf("SQL:\n  got  %s\n  want %s", got, want)
	}
	if strings.Contains(got, "IGNORE") {
		t.Errorf("INSERT IGNORE hides every row error:\n%s", got)
	}
	if plain := InsertSQL("t", []string{"a"}, 1, ""); strings.Contains(plain, "DUPLICATE") {
		t.Errorf("without dedup there must be no ON DUPLICATE KEY:\n%s", plain)
	}
}

// TestBatchSizeStaysUnderThePlaceholderLimit: 1000 rows x 100 columns is 100000
// placeholders, over MySQL's 65535.
func TestBatchSizeStaysUnderThePlaceholderLimit(t *testing.T) {
	for _, c := range []struct{ configured, columns, want int }{
		{0, 1, defaultBatch},
		{0, 100, 655},
		{1000, 65, 1000},
		{1000, 66, 992},
		{10, 100, 10},
		{0, 70000, 1},
	} {
		if got := batchSize(c.configured, c.columns); got != c.want {
			t.Errorf("batchSize(%d, %d) = %d, want %d", c.configured, c.columns, got, c.want)
		}
	}
}

// TestQualifyQuotesEachPart: quoting the whole "database.table" would create a
// table
// literally called "database.table".
func TestQualifyQuotesEachPart(t *testing.T) {
	if got := qualify("landing.pedidos"); got != "`landing`.`pedidos`" {
		t.Errorf("qualify = %s", got)
	}
	if got := qualify("pedidos"); got != "`pedidos`" {
		t.Errorf("qualify = %s", got)
	}
}

// TestQuoteEscapesABacktick: a backtick inside the name would close the
// identifier and the
// resto viraria SQL.
func TestQuoteEscapesABacktick(t *testing.T) {
	if got := quote("a`b"); got != "`a``b`" {
		t.Errorf("quote = %s", got)
	}
}

// TestInsertSQLReservedWord: a column called `order` is legitimate.
func TestInsertSQLReservedWord(t *testing.T) {
	got := InsertSQL("t", []string{core.MetadataID, "order"}, 1, "")
	if !strings.Contains(got, "`order`") {
		t.Errorf("palavra reservada sem crase:\n%s", got)
	}
}

// TestSplitName cobre o banco implicito.
func TestSplitName(t *testing.T) {
	if b, tb := splitName("landing.pedidos"); b != "landing" || tb != "pedidos" {
		t.Errorf("= (%q, %q)", b, tb)
	}
	if b, tb := splitName("pedidos"); b != "" || tb != "pedidos" {
		t.Errorf("= (%q, %q); banco vazio significa o do DSN", b, tb)
	}
}

// TestWithParseTimeIsNecessary: without parseTime=true the driver returns
// DATETIME
// as []byte, and every instant would become base64 in the JSON -- silently,
// because []byte is a legitimate value.
func TestWithParseTime(t *testing.T) {
	casos := map[string]string{
		"u:s@tcp(h:3306)/db":                 "u:s@tcp(h:3306)/db?parseTime=true",
		"u:s@tcp(h:3306)/db?charset=utf8":    "u:s@tcp(h:3306)/db?charset=utf8&parseTime=true",
		"u:s@tcp(h:3306)/db?parseTime=false": "u:s@tcp(h:3306)/db?parseTime=false",
	}
	for input, quero := range casos {
		if got := comParseTime(input); got != quero {
			t.Errorf("comParseTime(%q) = %q, esperado %q", input, got, quero)
		}
	}
}
