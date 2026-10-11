package bigquery

import "github.com/AreteAcademy/brevis/sdk/internal/core"

var _ core.Configured = Table{}

// Configuration satisfies sdk.Configured: the table and how it is loaded,
// resolved the way the load resolves it -- set, then the environment, then the
// default -- through the same config(), so the page cannot name a different
// table from the one written.
//
// A table with no project set and none in the environment lists what it has.
func (b Table) Configuration() []core.Setting {
	cfg, _, err := b.config(core.WriteOptions{})
	if err != nil {
		return core.Keep(
			core.Setting{Key: "project", Value: b.Project},
			core.Setting{Key: "dataset", Value: b.Dataset},
			core.Setting{Key: "table", Value: b.Name},
		)
	}
	return core.Keep(
		core.Setting{Key: "project", Value: cfg.ProjectID},
		core.Setting{Key: "dataset", Value: cfg.Dataset},
		core.Setting{Key: "table", Value: cfg.Table},
		core.Setting{Key: "create_table", Value: cfg.CreateTable},
		core.Setting{Key: "create_sql", Value: cfg.CreateSQL},
		core.Setting{Key: "evolve", Value: core.EvolutionText(cfg.Evolve)},
		core.Setting{Key: "cluster_by", Value: cfg.ClusterBy},
		core.Setting{Key: "partition_expiration", Value: core.DurationText(cfg.PartitionExpiration)},
		core.Setting{Key: "require_partition_filter", Value: cfg.RequirePartitionFilter},
		core.Setting{Key: "staging_bucket", Value: core.SafeURL(cfg.StagingBucket)},
		core.Setting{Key: "staging_prefix", Value: core.SafeURL(cfg.StagingPrefix)},
		core.Setting{Key: "inline_limit", Value: cfg.ThresholdForGCS},
		core.Setting{Key: "inline_limit_bytes", Value: cfg.ThresholdBytesForGCS},
		core.Setting{Key: "keep_staged_file", Value: cfg.KeepStagedFile},
	)
}
