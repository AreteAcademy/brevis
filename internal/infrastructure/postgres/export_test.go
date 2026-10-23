package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/AreteAcademy/brevis/migrations"
)

// RecentStatesSQL is the query itself, for a test to EXPLAIN (#109).
const RecentStatesSQL = recentStatesSQL

// MigrateDownTo rolls the schema back to `version`, for a test of one
// migration that must not assume it is the last one: Migrate's "down" undoes
// whichever came last, which is a different migration the day another lands.
func MigrateDownTo(ctx context.Context, url string, version int64) error {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return err
	}
	db := stdlib.OpenDB(*cfg)
	defer func() { _ = db.Close() }()
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.DownToContext(ctx, db, ".", version)
}
