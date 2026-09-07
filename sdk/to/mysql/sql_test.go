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
	got := InsertSQL("pedidos", []string{"ingestion_id", "valor"}, 2, false)
	esperado := "INSERT INTO `pedidos` (`ingestion_id`, `valor`) VALUES (?,?), (?,?)"
	if got != esperado {
		t.Errorf("SQL:\n  got  %s\n  want %s", got, esperado)
	}
}

// TestInsertSQLIgnoresOnDedup: INSERT IGNORE is MySQL's dedup.
func TestInsertSQLIgnoresOnDedup(t *testing.T) {
	got := InsertSQL("t", []string{"a"}, 1, true)
	if !strings.HasPrefix(got, "INSERT IGNORE INTO") {
		t.Errorf("dedup nao virou INSERT IGNORE:\n%s", got)
	}
	if semDedup := InsertSQL("t", []string{"a"}, 1, false); strings.Contains(semDedup, "IGNORE") {
		t.Errorf("sem dedup virou IGNORE:\n%s", semDedup)
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
	got := InsertSQL("t", []string{core.MetadataID, "order"}, 1, false)
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
	for entrada, quero := range casos {
		if got := comParseTime(entrada); got != quero {
			t.Errorf("comParseTime(%q) = %q, esperado %q", entrada, got, quero)
		}
	}
}
