package bigquery

import (
	"testing"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

func TestLocateFollowsTheSamePrecedenceAsTheLoad(t *testing.T) {
	cases := []struct {
		name              string
		envProject, envDS string
		table             Table
		want              string
	}{
		{"everything set", "", "", Table{Project: "acme-prod", Dataset: "bronze", Name: "clicks"},
			"bigquery://acme-prod/bronze/clicks"},
		{"project and dataset from the environment", "env-proj", "env_ds", Table{Name: "clicks"},
			"bigquery://env-proj/env_ds/clicks"},
		{"set beats the environment", "env-proj", "env_ds", Table{Project: "acme-prod", Dataset: "bronze", Name: "clicks"},
			"bigquery://acme-prod/bronze/clicks"},
		{"dataset defaults to landing", "env-proj", "", Table{Name: "clicks"},
			"bigquery://env-proj/landing/clicks"},
		{"a partition decorator is one table", "", "", Table{Project: "acme-prod", Dataset: "bronze", Name: "clicks$20261007"},
			"bigquery://acme-prod/bronze/clicks"},
		{"no project anywhere", "", "", Table{Dataset: "bronze", Name: "clicks"}, ""},
		{"no name", "", "", Table{Project: "acme-prod", Dataset: "bronze"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(core.EnvProject, c.envProject)
			t.Setenv(core.EnvDataset, c.envDS)
			if got := c.table.Locate(); got != c.want {
				t.Fatalf("Locate() = %q, want %q", got, c.want)
			}
			// And the load agrees: the target names what config() would write to.
			if cfg, _, err := c.table.config(core.WriteOptions{}); err == nil {
				want := core.BigQueryTarget(cfg.ProjectID, cfg.Dataset, cfg.Table)
				if got := c.table.Locate(); got != want {
					t.Fatalf("Locate() = %q, the load writes to %q", got, want)
				}
			}
		})
	}
}
