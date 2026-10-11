package redshift

import (
	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/sdk/internal/core"
)

var _ core.Configured = Table{}

// Configuration satisfies sdk.Configured: where the table is, where a batch is
// staged, and the role the cluster assumes to read it.
//
// The DSN gives its host, port and database and nothing else -- Postgres's
// rules, which Redshift's DSN follows. The role is an ARN, not a key: the
// driver refuses access keys outright.
func (t Table) Configuration() []core.Setting {
	schema, table, qualified := core.SplitQualified(t.Name, '"')
	var host, database string
	var port int
	if cfg, err := pgx.ParseConfig(t.DSN); t.DSN != "" && err == nil {
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
		core.Setting{Key: "staging", Value: core.SafeURL(t.Staging)},
		core.Setting{Key: "iam_role", Value: t.IAMRole},
		core.Setting{Key: "store", Value: core.TypeName(t.Store)},
		core.Setting{Key: "keep_staged_file", Value: t.KeepStagedFile},
	)
}
