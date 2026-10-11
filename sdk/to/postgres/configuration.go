package postgres

import (
	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

var _ core.Configured = Table{}

// Configuration satisfies sdk.Configured: where the table is and how the load
// treats it.
//
// The DSN is parsed and never dialled, and only its host, port and database
// are listed. Its user, its password and every other parameter stay out: the
// description is read by everybody with a console login.
func (t Table) Configuration() []core.Setting {
	var cfg *pgx.ConnConfig
	switch {
	case t.Conn != nil:
		cfg = t.Conn.Config()
	case t.DSN != "":
		cfg, _ = pgx.ParseConfig(t.DSN)
	}
	schema, table, qualified := core.SplitQualified(t.Name, '"')
	var host, database string
	var port int
	if cfg != nil {
		host, port, database = cfg.Host, int(cfg.Port), cfg.Database
		if !qualified {
			schema = core.SearchPathSchema(cfg.RuntimeParams)
		}
	}
	return core.Keep(
		core.Setting{Key: "host", Value: host},
		core.Setting{Key: "port", Value: port},
		core.Setting{Key: "database", Value: database},
		core.Setting{Key: "schema", Value: schema},
		core.Setting{Key: "table", Value: table},
		core.Setting{Key: "create_table", Value: t.CreateTable},
		core.Setting{Key: "create_sql", Value: t.CreateSQL},
		core.Setting{Key: "evolve", Value: core.EvolutionText(t.Evolve)},
	)
}
