package gateway

import (
	"context"
	"fmt"
	"os"
	"strings"

	"cloud.google.com/go/storage"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/AreteAcademy/brevis/sdk"
	gcsstore "github.com/AreteAcademy/brevis/sdk/store/gcs"
	s3store "github.com/AreteAcademy/brevis/sdk/store/s3"
	"github.com/AreteAcademy/brevis/sdk/to"
	tobq "github.com/AreteAcademy/brevis/sdk/to/bigquery"
	tomysql "github.com/AreteAcademy/brevis/sdk/to/mysql"
	topg "github.com/AreteAcademy/brevis/sdk/to/postgres"
	"github.com/AreteAcademy/brevis/sdk/to/pubsub"
	toredshift "github.com/AreteAcademy/brevis/sdk/to/redshift"
)

// Envelope is what a sink receives, re-exported so an implementation of Sinker
// does not have to import the SDK to name the type it is handed.
type Envelope = sdk.Envelope

// Sinker is one destination, and it is the SDK's own Writer narrowed to what
// this needs.
//
// Narrowed rather than reused whole so a test can supply one without a cloud
// account, and so the gateway never grows a second idea of what a destination
// is: every implementation here IS an sdk driver.
type Sinker interface {
	Write(ctx context.Context, batch []sdk.Envelope) (int64, error)
	Describe() string
}

// pubsubSink publishes to a topic somebody else owns.
//
// The driver does the work; this is the translation from a YAML file to the
// two functions it takes. Nothing about the payload is touched: what a
// subscriber receives is the event as it arrived, after the hook, and the
// attributes the file named -- which is the rule that driver was built on.
type pubsubSink struct {
	topic pubsub.Topic
	name  string
}

func newPubSubSink(s Sink) *pubsubSink {
	t := pubsub.Topic{Project: s.Project, Name: s.Topic}

	// A YAML file cannot carry a function, so it carries the names of fields
	// and the function is built here. The client still decides the contract --
	// these are their own fields -- and the gateway invents no attribute of its
	// own, which is the correction that shaped this driver.
	if len(s.Attributes) > 0 {
		fields := append([]string(nil), s.Attributes...)
		t.Attributes = func(e sdk.Envelope) map[string]string {
			row, ok := e.Payload.(map[string]any)
			if !ok {
				return nil
			}
			attrs := make(map[string]string, len(fields))
			for _, f := range fields {
				// A field the event does not carry is ABSENT, not empty. An
				// empty attribute and a missing one mean different things to a
				// subscriber filtering on them.
				if v, present := row[f]; present {
					attrs[f] = text(v)
				}
			}
			if len(attrs) == 0 {
				return nil
			}
			return attrs
		}
	}

	if s.OrderingKey != "" {
		field := s.OrderingKey
		t.OrderingKey = func(e sdk.Envelope) string {
			row, ok := e.Payload.(map[string]any)
			if !ok {
				return ""
			}
			return text(row[field])
		}
	}

	return &pubsubSink{topic: t, name: fmt.Sprintf("pubsub:%s/%s", s.Project, s.Topic)}
}

func (p *pubsubSink) Describe() string { return p.name }

func (p *pubsubSink) Write(ctx context.Context, batch []sdk.Envelope) (int64, error) {
	res, err := p.topic.Write(ctx, batch, sdk.WriteOptions{})
	if res == nil {
		return 0, err
	}
	// RowsLoaded on the error path too: a publish that failed halfway wrote
	// some, and reporting zero would have the operator re-send what already
	// landed.
	return res.RowsLoaded, err
}

// filesSink writes NDJSON objects to a directory, a bucket or a prefix.
//
// It is `to.Files`, which already speaks local paths, gs:// and s3://. So a
// dead letter is a folder on a laptop and a bucket in production without this
// package learning a second idea of what a path is.
//
// The Store is what makes the second half of that true, and it was missing:
// `to.Files` takes the object-store backend as a field rather than choosing one
// inside, so a path with a scheme and no Store failed at WRITE time with "the
// Path needs a s3 Store". For an ordinary sink that is a retry and a dead
// letter. For the DEAD LETTER itself it is "the dead letter refused them too,
// and they are lost" -- the exact outcome it exists to prevent, arriving only
// on the first bad day in production.
type filesSink struct {
	files to.Files
	name  string
}

func newFilesSink(ctx context.Context, s Sink) (*filesSink, error) {
	store, err := storeFor(ctx, s.Path)
	if err != nil {
		return nil, err
	}
	f := to.Files{Path: s.Path}
	if store != nil {
		f.Store = store
	}
	return &filesSink{files: f, name: "files:" + s.Path}, nil
}

// storeFor builds the object-store backend a path needs, or nil for a local
// one.
//
// At CONSTRUCTION and not on first use: a gateway whose credentials are wrong
// should fail to go ready, not go ready and lose the first batch it buries.
func storeFor(ctx context.Context, path string) (sdk.Store, error) {
	switch {
	case strings.HasPrefix(path, "s3://"):
		cfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("the path %q is S3 and the AWS credentials did "+
				"not load: %w", path, err)
		}
		return s3store.New(awss3.NewFromConfig(cfg, s3Options)), nil

	case strings.HasPrefix(path, "gs://"):
		client, err := storage.NewClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("the path %q is GCS and the client did not "+
				"open: %w", path, err)
		}
		return gcsstore.New(client), nil
	}
	return nil, nil
}

// EnvS3Endpoint points S3 at something that is not Amazon's: MinIO, Ceph, R2,
// or a mock in a test. It is AWS's own variable name, so a deployment that
// already sets it for other tools needs nothing new here.
const EnvS3Endpoint = "AWS_ENDPOINT_URL_S3"

// s3Options turns on path-style addressing when the endpoint is not Amazon's.
//
// Virtual-host addressing puts the bucket in the HOSTNAME -- bucket.s3.amazonaws.com
// -- which every S3-compatible server either does not do or needs DNS for.
// Path style is the form they all accept, and it is only switched on when an
// endpoint was named, so nothing changes for Amazon.
func s3Options(o *awss3.Options) {
	if os.Getenv(EnvS3Endpoint) != "" {
		o.UsePathStyle = true
	}
}

func (f *filesSink) Describe() string { return f.name }

func (f *filesSink) Write(ctx context.Context, batch []sdk.Envelope) (int64, error) {
	res, err := f.files.Write(ctx, batch, sdk.WriteOptions{})
	if res == nil {
		return 0, err
	}
	return res.RowsLoaded, err
}

// postgresSink writes rows into a table.
//
// The two write modes are the driver's two paths, not a layer of this
// package's own:
//
//	append  COPY FROM STDIN -- Postgres's fast path. No per-row round trip and
//	        no index lookup per row, which is why it is the one to reach for
//	        when the table is a log.
//	merge   the batch staged into a TEMP table and inserted onto the target
//	        with ON CONFLICT (ingestion_id) DO NOTHING, in ONE transaction:
//	        BEGIN, CREATE TEMP … ON COMMIT DROP, COPY, INSERT, COMMIT. A crash
//	        between any two of those leaves the table as it was, which is the
//	        ACID part of this and the reason the staging table exists.
//
// `merge` needs a UNIQUE index on ingestion_id and the driver refuses without
// one rather than silently appending -- which is the failure the mode exists to
// prevent. The refusal travels the normal way: the batch is retried, then
// buried in the dead letter with the reason, so the events are not lost while
// somebody creates the index.
//
// What makes merge mean anything is that ingestion_id is the SAME id a batch
// fetcher computes for the same record: the frozen UUID v5 over
// provider|entity|source_key|record_ts. So a row this gateway lands and a row a
// pipeline lands are one row, with no reconciliation between them.
type postgresSink struct {
	table topg.Table
	dedup sdk.Dedup
	name  string
}

func newPostgresSink(s Sink) (*postgresSink, error) {
	dsn, err := dsnFrom(s)
	if err != nil {
		return nil, err
	}
	return &postgresSink{
		table: topg.Table{DSN: dsn, Name: s.Table},
		dedup: dedupFor(s.Write),
		name:  "postgres:" + s.Table + " (" + s.Write + ")",
	}, nil
}

func (p *postgresSink) Describe() string { return p.name }

func (p *postgresSink) Write(ctx context.Context, batch []sdk.Envelope) (int64, error) {
	// No Columns. A pipeline declares them because its rows have one shape it
	// controls; a gateway's batch is whatever N clients posted in the last
	// flush window, and the two shapes are allowed to differ.
	//
	// The driver resolves the column list from the table itself, intersected
	// with what the batch carries, BEFORE touching the server -- so a field the
	// table does not have is refused with the message that fixes it instead of
	// failing mid-COPY with `column "x" of relation "y" does not exist`, and a
	// field one event omits is written as NULL.
	res, err := p.table.Write(ctx, batch, sdk.WriteOptions{Dedup: p.dedup})
	if res == nil {
		return 0, err
	}
	// RowsLoaded on the error path too, for the reason the Pub/Sub sink does
	// it: reporting zero for a partial write has the operator re-send what
	// already landed.
	return res.RowsLoaded, err
}

// mysqlSink writes rows into a MySQL table.
//
// The modes are the same two words, and they mean the same two things. What
// differs is the machinery underneath, and it is worth knowing which:
//
//	append  a multi-row INSERT per block, inside a transaction. MySQL has no
//	        COPY, so this is an order of magnitude below Postgres on the same
//	        hardware -- the batch sizes here matter more than they do there.
//	merge   INSERT IGNORE, which needs a UNIQUE index on ingestion_id. The
//	        driver refuses without one, for the reason Postgres does: without
//	        the index there is nothing to match and every redelivery lands
//	        again.
type mysqlSink struct {
	table tomysql.Table
	dedup sdk.Dedup
	name  string
}

func newMySQLSink(s Sink) (*mysqlSink, error) {
	dsn, err := dsnFrom(s)
	if err != nil {
		return nil, err
	}
	return &mysqlSink{
		table: tomysql.Table{DSN: dsn, Name: s.Table},
		dedup: dedupFor(s.Write),
		name:  "mysql:" + s.Table + " (" + s.Write + ")",
	}, nil
}

func (m *mysqlSink) Describe() string { return m.name }

func (m *mysqlSink) Write(ctx context.Context, batch []sdk.Envelope) (int64, error) {
	res, err := m.table.Write(ctx, batch, sdk.WriteOptions{Dedup: m.dedup})
	if res == nil {
		return 0, err
	}
	return res.RowsLoaded, err
}

// bigquerySink writes rows into a BigQuery table.
//
// No connection string: it authenticates with the pod's own credentials, which
// is what a workload identity is for. A DSN here would be a second way to do
// what the platform already does, and a second place for a secret to live.
//
//	append  rows loaded directly, or staged through GCS above the driver's
//	        inline limit -- the driver decides by batch size, and a gateway's
//	        batches are small enough that most go inline.
//	merge   the batch staged and MERGEd on ingestion_id,
//	        WHEN NOT MATCHED THEN INSERT. First delivery wins, same as
//	        everywhere else.
type bigquerySink struct {
	table tobq.Table
	dedup sdk.Dedup
	name  string
}

func newBigQuerySink(s Sink) *bigquerySink {
	return &bigquerySink{
		table: tobq.Table{
			Project:       s.Project,
			Dataset:       s.Dataset,
			Name:          s.Table,
			StagingBucket: s.StagingBucket,
		},
		dedup: dedupFor(s.Write),
		name: fmt.Sprintf("bigquery:%s.%s.%s (%s)",
			s.Project, s.Dataset, s.Table, s.Write),
	}
}

func (b *bigquerySink) Describe() string { return b.name }

func (b *bigquerySink) Write(ctx context.Context, batch []sdk.Envelope) (int64, error) {
	res, err := b.table.Write(ctx, batch, sdk.WriteOptions{Dedup: b.dedup})
	if res == nil {
		return 0, err
	}
	return res.RowsLoaded, err
}

// redshiftSink writes rows into a Redshift table, through S3.
//
// Two hops, not one, and that is Redshift's nature rather than a shortcut: it
// is columnar, a row-by-row INSERT pays the cost of a block, and the only
// workable load is COPY from S3. So every batch becomes an object in the
// staging prefix and then a COPY -- which also means this sink costs an S3
// write per batch, and a stream flushing every second writes 86,400 objects a
// day. Flush wider here than elsewhere.
type redshiftSink struct {
	table toredshift.Table
	dedup sdk.Dedup
	name  string
}

func newRedshiftSink(ctx context.Context, s Sink) (*redshiftSink, error) {
	dsn, err := dsnFrom(s)
	if err != nil {
		return nil, err
	}
	store, err := storeFor(ctx, s.Staging)
	if err != nil {
		return nil, err
	}
	return &redshiftSink{
		table: toredshift.Table{
			DSN:     dsn,
			Name:    s.Table,
			Staging: s.Staging,
			IAMRole: s.IAMRole,
			Store:   store,
		},
		dedup: dedupFor(s.Write),
		name:  "redshift:" + s.Table + " (" + s.Write + ")",
	}, nil
}

func (r *redshiftSink) Describe() string { return r.name }

func (r *redshiftSink) Write(ctx context.Context, batch []sdk.Envelope) (int64, error) {
	res, err := r.table.Write(ctx, batch, sdk.WriteOptions{Dedup: r.dedup})
	if res == nil {
		return 0, err
	}
	return res.RowsLoaded, err
}

// dedupFor turns the config's word into the SDK's mode. One function, so the
// four table sinks cannot drift on what `merge` means.
//
// The config already refused anything that is not one of the two words, so the
// default here is reached only by a sink that forgot to call checkWrite -- and
// DedupNone is the safe half of that mistake: it appends where it should have
// merged, which a UNIQUE index turns into a loud failure rather than a silent
// duplicate.
func dedupFor(write string) sdk.Dedup {
	if write == WriteMerge {
		return sdk.DedupMerge
	}
	return sdk.DedupNone
}

// dsnFrom reads the connection string out of the environment variable the
// config named. The config carries the NAME; a DSN carries a password and the
// file is in git.
func dsnFrom(s Sink) (string, error) {
	dsn, set := os.LookupEnv(s.DSNFrom)
	if !set || strings.TrimSpace(dsn) == "" {
		return "", fmt.Errorf("%s is empty, and it is where the connection string "+
			"for %s was meant to be", s.DSNFrom, s.Table)
	}
	return dsn, nil
}

// build resolves a sink. It is the only place a type name becomes an
// implementation, so an unknown one cannot reach the request path.
//
// Everything that can fail does so here: a missing connection string, absent
// cloud credentials, a staging prefix nobody can write. A gateway that goes
// ready and discovers this on the first batch is a gateway that loses it.
func build(ctx context.Context, s Sink) (Sinker, error) {
	switch s.Type {
	case SinkPubSub:
		return newPubSubSink(s), nil
	case SinkPostgres:
		return newPostgresSink(s)
	case SinkMySQL:
		return newMySQLSink(s)
	case SinkBigQuery:
		return newBigQuerySink(s), nil
	case SinkRedshift:
		return newRedshiftSink(ctx, s)
	case SinkFiles:
		return newFilesSink(ctx, s)
	default:
		return nil, fmt.Errorf("sink type %q", s.Type)
	}
}
