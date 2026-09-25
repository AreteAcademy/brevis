// Package bigquery writes a gateway's batches into a BigQuery table.
//
// Importing it costs the most of any sink here -- around 6 MB, because it
// brings the BigQuery client, the Storage Write API and Arrow. A gateway that
// does not write to BigQuery should not import it, which is the whole reason
// the drivers were split into packages.
package bigquery

import (
	"context"
	"fmt"
	"strings"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
	tobq "github.com/AreteAcademy/brevis/sdk/to/bigquery"
)

// Sink is what the YAML calls this driver.
const Sink = "bigquery"

// New builds the driver from a config block.
//
// No dsn_from: BigQuery authenticates with the pod's own credentials, which is
// what a workload identity is for. A connection string here would be a second
// way to do what the platform already does, and a second place for a secret.
//
//	append  rows loaded directly, or staged through GCS above the driver's
//	        inline limit -- the driver decides by batch size, and a gateway's
//	        batches are usually small enough to go inline.
//	merge   the batch staged and MERGEd on ingestion_id,
//	        WHEN NOT MATCHED THEN INSERT. First delivery wins, same as
//	        everywhere else.
func New(b gateway.Build) (gateway.Sinker, error) {
	s := b.Sink
	if err := gateway.CheckWrite(s); err != nil {
		return nil, err
	}
	if err := gateway.CheckTable(s); err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.Project) == "" {
		return nil, fmt.Errorf("`project` is empty (the GCP project holding the dataset)")
	}
	if strings.TrimSpace(s.Dataset) == "" {
		return nil, fmt.Errorf("`dataset` is empty")
	}
	if strings.Contains(s.Table, ".") {
		// BigQuery's name is three parts and they are three FIELDS here.
		// Accepting "landing.clicks" would make a table literally called
		// "landing.clicks" inside the declared dataset.
		return nil, fmt.Errorf("`table` is %q: for BigQuery the name has no dots -- "+
			"the project and the dataset are their own fields", s.Table)
	}
	t := tobq.Table{
		Project:       s.Project,
		Dataset:       s.Dataset,
		Name:          s.Table,
		StagingBucket: s.StagingBucket,
	}
	if b.Target != nil {
		create := b.Target.Create
		t.CreateTable = &create
		t.ClusterBy = b.Target.ClusterBy
	}
	return &sink{
		table:  t,
		target: b.Target,
		dedup:  gateway.DedupFor(s.Write),
		name: fmt.Sprintf("bigquery:%s.%s.%s (%s)",
			s.Project, s.Dataset, s.Table, s.Write),
	}, nil
}

type sink struct {
	table  tobq.Table
	dedup  sdk.Dedup
	target *gateway.Target
	name   string
}

func (b *sink) Describe() string { return b.name }

func (b *sink) Write(ctx context.Context, batch []gateway.Envelope) (int64, error) {
	opt := sdk.WriteOptions{Dedup: b.dedup}
	if b.target != nil {
		opt.Schema = b.target.Schema
		opt.DedupKey = b.target.DedupKey
		opt.PartitionBy = b.target.PartitionBy
	}
	res, err := b.table.Write(ctx, batch, opt)
	if res == nil {
		return 0, err
	}
	return res.RowsLoaded, err
}
