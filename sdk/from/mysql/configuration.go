package mysql

import (
	"net"
	"strconv"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

var _ core.Configured = Query{}

// Configuration satisfies sdk.Configured: where the query runs, and the query
// whole -- see from/postgres for why, and why its arguments are only counted.
func (q Query) Configuration() []core.Setting {
	var host, database string
	var port int
	if cfg, err := gomysql.ParseDSN(q.DSN); q.DSN != "" && err == nil {
		if h, p, err := net.SplitHostPort(cfg.Addr); err == nil {
			host = h
			port, _ = strconv.Atoi(p)
		}
		database = cfg.DBName
	}
	return core.Keep(
		core.Setting{Key: "host", Value: host},
		core.Setting{Key: "port", Value: port},
		core.Setting{Key: "database", Value: database},
		core.Setting{Key: "sql", Value: q.SQL},
		core.Setting{Key: "args", Value: len(q.Args)},
		core.Setting{Key: "timeout", Value: core.DurationText(q.Timeout)},
	)
}
