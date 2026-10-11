package postgres

import (
	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

var _ core.Configured = Query{}

// Configuration satisfies sdk.Configured: where the query runs, and the query.
//
// THE SQL WHOLE, decided with the owner (2026-10-11): it is the most useful
// thing a reader of /data can see about this source, and the console is where
// the people who could read it already work. Its arguments are COUNTED, not
// shown: they are values, and they change every run.
//
// The DSN gives its host, port and database and nothing else.
func (q Query) Configuration() []core.Setting {
	var cfg *pgx.ConnConfig
	switch {
	case q.Conn != nil:
		cfg = q.Conn.Config()
	case q.DSN != "":
		cfg, _ = pgx.ParseConfig(q.DSN)
	}
	var host, database string
	var port int
	if cfg != nil {
		host, port, database = cfg.Host, int(cfg.Port), cfg.Database
	}
	return core.Keep(
		core.Setting{Key: "host", Value: host},
		core.Setting{Key: "port", Value: port},
		core.Setting{Key: "database", Value: database},
		core.Setting{Key: "sql", Value: q.SQL},
		core.Setting{Key: "args", Value: len(q.Args)},
		core.Setting{Key: "fetch_size", Value: q.FetchSize},
		core.Setting{Key: "timeout", Value: core.DurationText(q.Timeout)},
	)
}
