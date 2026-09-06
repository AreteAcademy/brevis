// Package postgres is the persistence adapter.
//
// The plan (section 22) defines the database as the operational source of truth.
// Only the plumbing lives here: the connection pool, the migrations and the
// health check. The domain queries go into the phases that use them.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/AreteAcademy/brevis/migrations"
)

// Pool wraps pgxpool. The type exists so the rest of the system depends on
// something of ours, and not on the driver directly.
type Pool struct {
	*pgxpool.Pool
}

// New opens the pool and checks the connection before returning. A pool that
// only fails on first use turns a configuration error into a request error.
func New(ctx context.Context, url string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse da BREVIS_DATABASE_URL: %w", err)
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.HealthCheckPeriod = 30 * time.Second

	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("abrindo o pool: %w", err)
	}

	ctxPing, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := p.Ping(ctxPing); err != nil {
		p.Close()
		return nil, fmt.Errorf("conectando ao postgres: %w", err)
	}
	return &Pool{Pool: p}, nil
}

// Check is the health contract: a ping with a deadline. Without a timeout, a
// slow database would make readiness hang instead of failing.
func (p *Pool) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return p.Ping(ctx)
}

// Migrate applies the embedded migrations. It runs through the `brevis migrate`
// subcommand, never in `serve`: starting the application and migrating the
// schema have a different blast radius, and joining the two turns a casual
// restart into a DDL.
func Migrate(ctx context.Context, url, direcao string) error {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("parsing BREVIS_DATABASE_URL: %w", err)
	}

	db := stdlib.OpenDB(*cfg)
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}

	// "." because the embed.FS is rooted at the migrations/ directory itself.
	const dir = "."
	switch direcao {
	case "up":
		return goose.UpContext(ctx, db, dir)
	case "down":
		return goose.DownContext(ctx, db, dir)
	case "status":
		return goose.StatusContext(ctx, db, dir)
	default:
		return fmt.Errorf("direcao desconhecida: %q (use up, down ou status)", direcao)
	}
}
