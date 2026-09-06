package redshift

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
)

// executor returns how to run SQL on the cluster.
//
// Redshift speaks Postgres' protocol, so pgx serves -- and it is already a
// dependency of the Postgres driver. A consumer importing this package pays for
// pgx and does not pay for the MySQL driver or the BigQuery one.
func (t Table) executor(ctx context.Context) (SQLExecutor, func(), error) {
	if t.Executor != nil {
		return t.Executor, func() {}, nil
	}
	cfg, err := pgx.ParseConfig(t.DSN)
	if err != nil {
		return nil, nil, fmt.Errorf("redshift: DSN is not valid")
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("redshift: connecting: %w", esconderDSN(err, t.DSN))
	}
	return conexao{conn}, func() { _ = conn.Close(context.WithoutCancel(ctx)) }, nil
}

type conexao struct{ conn *pgx.Conn }

func (c conexao) Exec(ctx context.Context, sql string) error {
	_, err := c.conn.Exec(ctx, sql)
	return err
}

// remove deletes the staging file.
//
// core.Store has no Delete: it was designed for from.Files and to.Files, which
// never delete. Rather than adding a method to the interface -- and forcing every
// third-party store to implement it because of one driver -- the driver asks
// whether that store knows how to delete.
func (t Table) remove(ctx context.Context, bucket, chave string) error {
	type apagador interface {
		Delete(ctx context.Context, bucket, key string) error
	}
	d, sabe := t.Store.(apagador)
	if !sabe {
		return fmt.Errorf("this store does not delete; set KeepStagedFile to silence this")
	}
	return d.Delete(ctx, bucket, chave)
}

func esconderDSN(err error, dsn string) error {
	if err == nil || dsn == "" || !strings.Contains(err.Error(), dsn) {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), dsn, "REDACTED"))
}

// avisarSobra says the staging file was left behind.
//
// It does not bring the load down: the rows are already in, and trading a good
// load for a cleanup error would trade a small problem for a big one. But it does
// not stay quiet -- one orphaned object per run becomes a bill at the end of the
// month.
func avisarSobra(ctx context.Context, uri string, err error) {
	slog.WarnContext(ctx, "redshift: the staged file was left behind",
		"object", uri,
		"why", err,
		"effect", "it costs storage until something removes it")
}
