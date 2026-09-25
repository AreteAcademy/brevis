// Package redshift writes a gateway's batches into a Redshift table, through
// S3.
//
// Importing it costs almost nothing on its own -- it speaks Postgres' dialect
// -- but it needs an object store registered for its staging prefix, and
// gateway/store/s3 is what costs (about 3 MB, the AWS SDK and its credential
// chain).
package redshift

import (
	"context"
	"fmt"
	"strings"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/sdk"
	toredshift "github.com/AreteAcademy/brevis/sdk/to/redshift"
)

// Sink is what the YAML calls this driver.
const Sink = "redshift"

// New builds the driver from a config block.
//
// Two hops, not one, and that is Redshift's nature rather than a shortcut: it
// is columnar, a row-by-row INSERT pays the cost of a block, and the only
// workable load is COPY from S3. So every batch becomes an object in the
// staging prefix and then a COPY -- which also means this sink costs an S3
// write per batch, and a stream flushing every second writes 86,400 objects a
// day. Flush wider here than anywhere else.
func New(b gateway.Build) (gateway.Sinker, error) {
	s := b.Sink
	if err := gateway.CheckWrite(s); err != nil {
		return nil, err
	}
	if err := gateway.CheckTable(s); err != nil {
		return nil, err
	}
	// The file's own shape first, and the environment after. A missing DSN
	// reported before a missing `staging` sends the reader to the deployment
	// when the mistake is in the config -- and only one of the two can be
	// fixed by editing the file in front of them.
	if strings.TrimSpace(s.Staging) == "" {
		return nil, fmt.Errorf("`staging` is empty, and Redshift has no inline path: " +
			"a batch is written to S3 and COPYed from there, so this needs an " +
			"s3:// prefix the cluster can read")
	}
	if !strings.HasPrefix(s.Staging, "s3://") {
		return nil, fmt.Errorf("`staging` is %q: Redshift COPYs from S3, so it has "+
			"to be an s3:// prefix", s.Staging)
	}
	if strings.TrimSpace(s.IAMRole) == "" {
		return nil, fmt.Errorf("`iam_role` is empty: the cluster assumes a role to " +
			"read the staging prefix. A role and not a key, because a key in a " +
			"COPY's URL ends up in the cluster's query log")
	}
	dsn, err := gateway.DSNFrom(s)
	if err != nil {
		return nil, err
	}
	store, err := b.Stores.Open(b.Ctx, s.Staging)
	if err != nil {
		return nil, err
	}
	return &sink{
		table: toredshift.Table{
			DSN:     dsn,
			Name:    s.Table,
			Staging: s.Staging,
			IAMRole: s.IAMRole,
			Store:   store,
		},
		dedup: gateway.DedupFor(s.Write),
		name:  "redshift:" + s.Table + " (" + s.Write + ")",
	}, nil
}

type sink struct {
	table toredshift.Table
	dedup sdk.Dedup
	name  string
}

func (r *sink) Describe() string { return r.name }

func (r *sink) Write(ctx context.Context, batch []gateway.Envelope) (int64, error) {
	res, err := r.table.Write(ctx, batch, sdk.WriteOptions{Dedup: r.dedup})
	if res == nil {
		return 0, err
	}
	return res.RowsLoaded, err
}
