package gateway

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/to"
	topg "github.com/AreteAcademy/brevis/sdk/to/postgres"
	"github.com/AreteAcademy/brevis/sdk/to/pubsub"
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
type filesSink struct {
	files to.Files
	name  string
}

func newFilesSink(s Sink) *filesSink {
	return &filesSink{
		files: to.Files{Path: s.Path},
		name:  "files:" + s.Path,
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
	dsn, set := os.LookupEnv(s.DSNFrom)
	if !set || strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("%s is empty, and it is where the connection string "+
			"for %s was meant to be", s.DSNFrom, s.Table)
	}

	dedup := sdk.DedupNone
	if s.Write == WriteMerge {
		dedup = sdk.DedupMerge
	}
	return &postgresSink{
		table: topg.Table{DSN: dsn, Name: s.Table},
		dedup: dedup,
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

// build resolves a sink. It is the only place a type name becomes an
// implementation, so an unknown one cannot reach the request path.
func build(s Sink) (Sinker, error) {
	switch s.Type {
	case SinkPubSub:
		return newPubSubSink(s), nil
	case SinkPostgres:
		return newPostgresSink(s)
	case SinkFiles:
		return newFilesSink(s), nil
	default:
		return nil, fmt.Errorf("sink type %q", s.Type)
	}
}
