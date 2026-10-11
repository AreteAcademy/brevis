package mysql

import (
	"net"
	"strconv"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

var _ core.Configured = Table{}

// Configuration satisfies sdk.Configured: where the table is and how many rows
// go in one INSERT.
//
// The DSN gives its address and database and nothing else; the user, the
// password and the parameters stay out.
func (t Table) Configuration() []core.Setting {
	database, table, qualified := core.SplitQualified(t.Name, '`')
	var host string
	var port int
	if cfg, err := gomysql.ParseDSN(t.DSN); t.DSN != "" && err == nil {
		if h, p, err := net.SplitHostPort(cfg.Addr); err == nil {
			host = h
			port, _ = strconv.Atoi(p)
		}
		if !qualified {
			database = cfg.DBName
		}
	}
	batch := t.BatchSize
	if batch == 0 {
		batch = defaultBatch
	}
	return core.Keep(
		core.Setting{Key: "host", Value: host},
		core.Setting{Key: "port", Value: port},
		core.Setting{Key: "database", Value: database},
		core.Setting{Key: "table", Value: table},
		core.Setting{Key: "batch_size", Value: batch},
		core.Setting{Key: "create_table", Value: t.CreateTable},
		core.Setting{Key: "create_sql", Value: t.CreateSQL},
		core.Setting{Key: "evolve", Value: core.EvolutionText(t.Evolve)},
	)
}
