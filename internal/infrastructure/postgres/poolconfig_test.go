package postgres

import (
	"testing"
	"time"
)

func TestPoolConfigAppliesOptions(t *testing.T) {
	const url = "postgres://u:p@localhost:5432/db"

	def, err := poolConfig(url, PoolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, set := def.ConnConfig.RuntimeParams["statement_timeout"]; set {
		t.Error("no timeout asked, yet statement_timeout was set")
	}

	got, err := poolConfig(url, PoolOptions{MaxConns: 25, StatementTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxConns != 25 {
		t.Errorf("MaxConns = %d, want 25", got.MaxConns)
	}
	if v := got.ConnConfig.RuntimeParams["statement_timeout"]; v != "30000" {
		t.Errorf("statement_timeout = %q, want 30000 (ms)", v)
	}
}
