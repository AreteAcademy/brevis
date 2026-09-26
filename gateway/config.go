package gateway

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole gateway, and the file is the contract.
type Config struct {
	Name     string   `yaml:"name"`
	Listen   Listen   `yaml:"listen"`
	Metrics  Metering `yaml:"metrics"`
	Shutdown Shutdown `yaml:"shutdown"`
	Streams  []Stream `yaml:"streams"`
}

// Shutdown is how long a clean stop may take.
//
// It matters more than it looks, because the gateway answers 202 before
// anything is written: what is in a buffer at SIGTERM is delivered by the
// drain, and a drain that runs out of time loses events a producer was told
// had been accepted.
type Shutdown struct {
	// Drain is the budget for emptying every stream's buffer.
	//
	// Zero derives it from the flush windows -- see DrainBudget -- because the
	// two are the same quantity seen from opposite ends: a 300-second window
	// can be holding 300 seconds of events when the signal arrives, and a
	// fixed thirty was a number chosen when every window was one second.
	Drain time.Duration `yaml:"drain"`
}

// Metering is where the Prometheus exposition is served.
type Metering struct {
	// Addr is its OWN address, and that is the whole design of this field.
	//
	// The ingest port is public by construction -- it is where clients POST --
	// so `/metrics` on it would publish every stream name, path and
	// destination to whoever finds the path. On a second address it is never
	// in the Ingress at all.
	//
	// A POINTER, because the three states are three: absent takes the default,
	// `addr: ""` serves nothing, and a value is the value. A plain string
	// would collapse the first two, and "I turned metrics off" and "I did not
	// mention metrics" must not be the same sentence.
	Addr *string `yaml:"addr"`

	addr string // resolved by check
}

// Address is where to serve, resolved. Empty means serve nothing.
func (m Metering) Address() string { return m.addr }

type Listen struct {
	Addr string `yaml:"addr"`

	// Auth is who may write. Outside BREVIS_ENV=local it is required.
	Auth Auth `yaml:"auth"`

	// MaxBody caps one request. Zero takes the default; a body past it is
	// refused with 413 before a byte is parsed, because the alternative is a
	// client deciding this process's memory.
	MaxBody Size `yaml:"max_body"`
}

// Stream is one endpoint and where what arrives at it goes.
type Stream struct {
	Name string `yaml:"name"`

	// Path is where it listens. It must start with "/" and must not collide
	// with another stream's: two streams on one path is a silent winner.
	Path string `yaml:"path"`

	// Format says how to read the body. Declared, never sniffed: guessing is
	// how a batch of a thousand becomes one row holding an array.
	//
	//	json    one object
	//	array   a JSON array of objects
	//	ndjson  one object per line
	Format string `yaml:"format"`

	Identity Identity `yaml:"identity"`

	// Hook names a function registered in THIS binary, not a file. The hook is
	// Go and compiled in -- see the registry for what that buys and what it
	// costs. Empty means the event goes through untouched.
	Hook string `yaml:"hook"`

	// StampLoadedAt writes ingestion_loaded_at onto the payload: when the
	// gateway received it, UTC, RFC3339.
	//
	// Opt-in, because it adds a field to every record and a topic's
	// subscribers and a table's columns both notice. The same column name and
	// the same format the SDK writes, so a row this gateway lands and a row a
	// pipeline lands stay the same shape.
	StampLoadedAt bool `yaml:"stamp_loaded_at"`

	// Oversize is what happens to an event too big for the destination.
	Oversize *Oversize `yaml:"oversize"`

	Buffer Buffer `yaml:"buffer"`
	Sink   Sink   `yaml:"sink"`

	// maxBodyFloor is listen.max_body, copied in by the config's own check so
	// the stream can refuse a byte ceiling that could never admit one request.
	maxBodyFloor Size

	// Retry is how hard the sink is tried before a batch is given up on.
	Retry Retry `yaml:"retry"`

	// DeadLetter is where a batch goes when the sink will not take it.
	//
	// Without one, a sink that refuses is a log line and nothing else -- which
	// is losing data quietly, and the one failure an ingestion service exists
	// to not have. A stream that declares none is REFUSED rather than defaulted
	// to silence: where the unacceptable goes is the operator's decision, and
	// having to make it is the point.
	DeadLetter Sink `yaml:"dead_letter"`
}

// Oversize is the claim-check path: an event too large is written somewhere
// whole, and what continues into the stream is a reduced version that points
// back at it.
//
// It is a named pattern -- Enterprise Integration Patterns calls it Claim
// Check -- and the reason it beats a flat refusal is that a 413 loses the
// event. An oversized payload is usually the most interesting one somebody
// has: it is the request with the whole document attached, and refusing it
// throws away the case worth debugging.
type Oversize struct {
	// LargerThan is the per-EVENT ceiling, measured on the encoded payload
	// after the hook has run. Distinct from listen.max_body, which caps a
	// whole REQUEST and refuses with 413 before anything is parsed.
	LargerThan Size `yaml:"larger_than"`

	// Archive is where the whole event goes. It is an ordinary sink, so this
	// is a directory on a laptop and a bucket in production with no second
	// idea of what a path is.
	Archive Sink `yaml:"archive"`

	// Hook names a second hook, which receives the oversized event and
	// returns the reduced one. It is Go, compiled in, like every other hook:
	// which fields are heavy is domain knowledge and a YAML file cannot hold
	// it.
	//
	// Without one the event is ARCHIVED AND DROPPED from the stream -- not
	// lost, because the archive has it whole, and counted, because an event
	// that silently stops arriving is the worst outcome here.
	Hook string `yaml:"hook"`
}

// The fields an archived event carries into the stream, so the reduced record
// can be traced back to the whole one.
//
// Stamped rather than left implicit. The alternative -- agreeing out of band
// that the id is also the object's name -- works until somebody changes the
// prefix, and then nothing says where to look.
const (
	ColumnOversize        = "_oversize"
	ColumnOversizeArchive = "_oversize_archive"
	ColumnOversizeBytes   = "_oversize_bytes"
)

// Retry is the sink's second, third and fourth chance.
type Retry struct {
	// Attempts counts the FIRST try. 1 means no retry at all.
	Attempts int `yaml:"attempts"`

	// Backoff is the wait before the second attempt; it doubles from there,
	// with jitter. Bounded by MaxBackoff.
	Backoff    time.Duration `yaml:"backoff"`
	MaxBackoff time.Duration `yaml:"max_backoff"`
}

// Identity is what becomes the ingestion_id, which is what makes a retried POST
// land once.
//
// The four are the frozen formula's four, in its order. Provider and Entity are
// constants of the stream; SourceKey and RecordTS name FIELDS of the event,
// because they are the record's own identity and only it has them.
type Identity struct {
	Provider  string `yaml:"provider"`
	Entity    string `yaml:"entity"`
	SourceKey string `yaml:"source_key"`
	RecordTS  string `yaml:"record_ts"`
}

// Buffer is the gap between accepting and delivering, and how honest a 200 is.
type Buffer struct {
	// Durability is what a 200 means. Today only "memory" exists and it says
	// so: a tier that silently behaved like another would be the worst
	// possible default for this field.
	Durability string `yaml:"durability"`

	Flush Flush `yaml:"flush"`

	// Workers is how many batches this stream delivers at once.
	//
	// It exists because a delivery is I/O: one worker means a stream is as fast
	// as one round trip to Pub/Sub or one COPY, no matter how many cores the
	// pod has. It is bounded because the opposite -- a goroutine per batch --
	// is a sink outage turning into unbounded memory and a thundering herd on
	// the way back up.
	//
	// Zero takes the default, like every other number in this file. There is
	// no way to ask for none, because a stream with no worker accepts events
	// and delivers nothing.
	Workers int `yaml:"workers"`

	// Queue is how many full batches may wait for a worker.
	//
	// This is the shock absorber: a sink that pauses for a second does not
	// reach the caller at all, it fills queue slots. When they are gone, the
	// gateway says so -- see MaxRecords.
	//
	// Zero takes the default. A queue of none is expressible only as 1, which
	// is near enough: with one worker busy, the second batch waits either way.
	Queue int `yaml:"queue"`

	// MaxRecords is the ceiling on events held in memory for this stream.
	// Past it the gateway answers 503 instead of accepting.
	//
	// A 503 is the honest answer and an accepted event that is never delivered
	// is not. It is also SAFE to retry here in a way it is not for most
	// services: the ingestion_id is a frozen function of the event, so the same
	// POST sent again is the same record, and a `merge` sink absorbs it.
	MaxRecords int `yaml:"max_records"`

	// MaxBytes is the same ceiling in BYTES, and it is the one that actually
	// stops an OOM.
	//
	// A count cannot see this. `max_records: 40000` is 80 MB of 2 KB events
	// and 80 GB of 2 MB ones, and until this field existed the config had no
	// way to say which this stream holds -- so a producer who started sending
	// the whole document instead of its id could take the pod down, and
	// nothing in the file could have expressed the limit.
	//
	// It bounds the BUFFER and not one batch: `flush.size` shapes what leaves,
	// this bounds what is held. They are different numbers -- with `queue: 64`
	// there can be many batches in flight beyond what this counts.
	//
	// Zero leaves it off, which is what every existing config gets.
	MaxBytes Size `yaml:"max_bytes"`
}

type Flush struct {
	// Every is the age at which a partial batch goes anyway, so a slow stream
	// is not a stream that never lands.
	Every time.Duration `yaml:"every"`

	// Records is the count that sends one immediately.
	Records int `yaml:"records"`

	// Size is the BYTE count that sends one immediately. Whichever of the
	// three is crossed first wins; zero leaves this one off.
	//
	// It is measured on what arrived, not on what the batch weighs in memory
	// -- a map[string]any is several times its JSON -- and a hook that
	// inflates an event is not counted. An approximation, and the right one:
	// it costs nothing to take, it moves with the payload, and the thing it
	// guards against is a producer whose records grew tenfold.
	//
	// On BigQuery this is a CEILING and not a target. That destination allows
	// 1,500 load jobs per table per day, which is why `every` has a 60-second
	// floor -- and a size that fires every few seconds walks straight past
	// that floor, because the floor only governs the timer. Nothing refuses it
	// here, because the arrival rate is not knowable at load; instead every
	// flush is counted by what triggered it, so `trigger="size"` climbing on a
	// BigQuery stream is visible on a dashboard.
	Size Size `yaml:"size"`
}

// Sink is the destination.
type Sink struct {
	Type string `yaml:"type"`

	// Project and Topic are Pub/Sub's. Named here rather than in a nested block
	// per type because one sink is declared per stream, and a block that holds
	// one thing is a level of nesting that buys nothing.
	Project string `yaml:"project"`
	Topic   string `yaml:"topic"`

	// Attributes names FIELDS of the event that become message attributes.
	//
	// The driver takes a function, and a YAML file cannot carry one. What it
	// can carry is which of the client's own fields travel beside the payload
	// -- which keeps the rule the driver was built on: the topic's contract
	// belongs to whoever owns the topic, and nothing is invented here.
	Attributes []string `yaml:"attributes"`

	// Dataset is BigQuery's. Project is shared with Pub/Sub above -- both are
	// the GCP project, and giving them one name keeps a file from saying the
	// same thing twice.
	Dataset string `yaml:"dataset"`

	// StagingBucket is where BigQuery stages a batch larger than the inline
	// limit. Empty lets the driver default it to <project>-brevis-staging.
	StagingBucket string `yaml:"staging_bucket"`

	// Staging is Redshift's S3 prefix, and it is REQUIRED for that sink:
	// there is no inline path into Redshift. It is a columnar database and a
	// row-by-row INSERT pays the cost of a block, so the only workable load is
	// COPY from S3.
	Staging string `yaml:"staging"`

	// IAMRole is the role the Redshift cluster assumes to read that prefix.
	//
	// A role and not an access key, which the driver will not accept at all: a
	// key in a COPY's URL ends up in the cluster's query log, and plenty of
	// people read that.
	IAMRole string `yaml:"iam_role"`

	// Path is where `files` writes: a directory, gs:// or s3://.
	Path string `yaml:"path"`

	// DSNFrom names the ENVIRONMENT VARIABLE holding the connection string,
	// never the string. A DSN carries a password and this file is in git --
	// the same split `secrets:` makes in a workflow.
	DSNFrom string `yaml:"dsn_from"`

	// Table is schema-qualified: `landing.clicks`.
	Table string `yaml:"table"`

	// Write is `append` or `merge`. Required for a table, because the right
	// answer depends on what the table is FOR and only its owner knows.
	Write string `yaml:"write"`

	// OrderingKey names the field whose value orders the messages. Empty is no
	// ordering, which is the default and what every other destination gives.
	OrderingKey string `yaml:"ordering_key"`

	// TableFrom exists only so the word can be REFUSED by name.
	//
	// v1 let the stream choose which field named the table. v2 does not: the
	// envelope names it in `table_name`, always, and a setting that could point
	// somewhere else would make the one fixed contract negotiable per stream.
	//
	// Keeping the field means a file carrying it gets a sentence saying what
	// replaced it. Deleting it would make `table_from` an unknown key, and
	// KnownFields would refuse the file with "field table_from not found" --
	// true, and no help at all to somebody upgrading.
	TableFrom string `yaml:"table_from"`

	// Naming bounds what a producer may ask for. Without it, a table name is a
	// namespace nobody reviewed and a typo is a new table rather than an error.
	Naming Naming `yaml:"naming"`

	// Shape decides what the RECORD contributes to the row: `document` puts it
	// whole in one JSON column, `columns` gives each field a column of its
	// own.
	//
	// One contract for the producer either way -- the envelope never changes.
	// What changes is what the operator's table looks like, which is their
	// decision and not the producer's.
	Shape string `yaml:"shape"`

	// Metastore caches what is known about a table, so a per-event write does
	// not become a per-event lookup.
	Metastore MetastoreConfig `yaml:"metastore"`

	// Into is the real destination, one per table. A nested sink, because
	// `auto_table` routes and does not write: the four columns it lands are
	// the same in Postgres (JSONB), MySQL (JSON), BigQuery (JSON) and Redshift
	// (SUPER), so which of them receives the rows is a separate choice from
	// the routing.
	Into *Sink `yaml:"into"`
}

// Metastore is the cache of what is known about each table.
//
// A CACHE and not a source of truth: the destination settles whether a table
// exists, and N replicas racing to create one is the normal case rather than
// the edge -- `AlreadyExists` is success. This only reduces the race.
type MetastoreConfig struct {
	// Type is the backend: `memory`, `redis` or `memcached`.
	//
	// `memory` is the default and needs nothing, which is the design rather
	// than a limitation: a gateway that cannot start without Redis is a
	// gateway with a new hard dependency for a cache. With ONE replica it is
	// also the right answer.
	//
	// With several it gives each its own, so the debounce debounces nothing
	// and `max_new_per_hour` bounds a process. That is the only reason the
	// other two exist, and it is worth saying plainly: a shared backend does
	// not make the gateway correct, it makes N replicas cheaper. The
	// destination settles whether a table exists either way.
	//
	// Whether THIS binary carries one is a separate question, answered by the
	// registry at startup, by name -- a slim build genuinely links neither.
	Type string `yaml:"type"`

	// TTL is how long the cache may be wrong, and `0` means never.
	//
	// A POINTER, because the three states are three -- the same reason
	// `metrics.addr` is one. Absent takes the default; `ttl: 0` means the
	// entry does not expire; a value is the value. A plain Duration collapses
	// the first two, and "I did not mention ttl" and "I want this kept
	// forever" must not be the same sentence.
	//
	// What the timer was defending against: a table dropped by hand outside
	// the gateway makes every entry a lie. But the CLOCK is not what recovers
	// from that -- `router.Write` is. A write that the destination refuses
	// calls `invalidate`, which drops the entry immediately and lets the
	// pipe's own retry come back against a cold cache. A dropped table costs
	// one failed attempt, not a minute.
	//
	// So expiry is not the safety net it reads as, and it has a price: a miss
	// charges `naming.max_new_per_hour` for a table that has existed for
	// weeks, because `sinkFor` charges on BELIEF and not on creation. With a
	// shared backend and a short TTL, a pod restarting a minute after a
	// table's last event gets no benefit from the shared store at all -- which
	// is the case the shared store exists for.
	//
	// Keeping the default at a minute anyway: an installation that has not
	// thought about this should get the conservative behaviour, and the
	// installation that has can say so in one field. Issue #35 is the
	// argument, made by a consumer with 52 tables and a limit of 20.
	TTL *time.Duration `yaml:"ttl"`

	// AddrFrom names the ENVIRONMENT VARIABLE holding a shared backend's
	// address, never the address: it carries a password often enough, and this
	// file is in git. Unused by `memory`, and REQUIRED by the other two --
	// a shared backend with no address is a config that would quietly fall
	// back to being alone.
	AddrFrom string `yaml:"addr_from"`
}

// The metastore backends.
const (
	MetastoreMemory    = "memory"
	MetastoreRedis     = "redis"
	MetastoreMemcached = "memcached"
)

// DefaultMetastoreTTL is how long a cache entry lives when the file names no
// other. See MetastoreConfig.TTL for why it is a minute, and why `ttl: 0` --
// which is a different thing from saying nothing -- means never.
const DefaultMetastoreTTL = 60 * time.Second

func (m *MetastoreConfig) check() error {
	switch m.Type {
	case "", MetastoreMemory:
		m.Type = MetastoreMemory
	case MetastoreRedis, MetastoreMemcached:
		// No longer refused: they are backends now. Whether THIS binary
		// carries one is the registry's answer, at New, by name.
		if strings.TrimSpace(m.AddrFrom) == "" {
			return fmt.Errorf("`metastore.type` is %s and `addr_from` is empty: "+
				"name the environment variable holding its address, never the "+
				"address -- it carries a password often enough", m.Type)
		}
	default:
		return fmt.Errorf("`metastore.type` is %q (use %s, %s or %s)",
			m.Type, MetastoreMemory, MetastoreRedis, MetastoreMemcached)
	}
	if m.TTL != nil && *m.TTL < 0 {
		return fmt.Errorf("`metastore.ttl` is %s", *m.TTL)
	}
	return nil
}

// CacheTTL is how long an entry lives. Zero means it does not expire.
//
// Derived from the pointer and not from a field that `check` fills in, so a
// Config built in code -- a test, a consumer embedding the gateway -- gets the
// same answer as one that came through Load. A resolved field would have made
// "check did not run" and "the operator asked for never" the same zero, which
// is the exact confusion the pointer exists to prevent.
func (m MetastoreConfig) CacheTTL() time.Duration {
	if m.TTL == nil {
		return DefaultMetastoreTTL
	}
	return *m.TTL
}

// Naming is the boundary a producer writes inside.
//
// Every field here is a refusal, and that is the point: `auto_table` turns a
// string in a payload into DDL, so the string is the whole of the attack
// surface. A gateway that creates whatever it is asked to create is a
// production dataset anybody in the cluster can fill.
type Naming struct {
	// Pattern is the regular expression a name must match WHOLE. Empty takes
	// defaultTableName, which is deliberately narrow.
	Pattern string `yaml:"pattern"`

	// Allow is an optional list of prefixes. Empty allows any name the pattern
	// accepts; non-empty means a name must also start with one of these.
	Allow []string `yaml:"allow"`

	// MaxNewPerHour caps how many tables this stream may CREATE in an hour.
	// A runaway producer stops at this number rather than at BigQuery's
	// per-project quota, which is shared with everything else in the project.
	//
	// Zero takes the default. It bounds creations, never writes: a table that
	// already exists is never rate limited.
	MaxNewPerHour int `yaml:"max_new_per_hour"`
}

// Load reads and checks a config. Every refusal names the field and what is
// accepted: a gateway that starts on a file it half understood is one that
// drops events for a reason nobody can see.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the operator's own file
	if err != nil {
		return nil, err
	}
	var c Config

	// KnownFields: a typo in a key is a setting that silently does nothing,
	// and in this file that means a durability, a flush or an ordering key the
	// operator believes is in force.
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

const (
	FormatJSON   = "json"
	FormatArray  = "array"
	FormatNDJSON = "ndjson"

	DurabilityMemory = "memory"

	// SinkAutoTable routes each event to the table its own payload names,
	// creating the table when it is absent. It writes nothing itself: `into`
	// names the destination that does.
	SinkAutoTable = "auto_table"

	SinkPubSub   = "pubsub"
	SinkFiles    = "files"
	SinkPostgres = "postgres"
	SinkBigQuery = "bigquery"
	SinkMySQL    = "mysql"
	SinkRedshift = "redshift"
)

// How a row is written. The words are the ones a data engineer already uses,
// and each maps to what the driver actually does rather than to an intention.
const (
	// WriteAppend adds rows and never looks at what is there. In Postgres that
	// is COPY FROM STDIN, which is its fast path: no per-row round trip, no
	// index lookup per row.
	//
	// Two deliveries of the same event land twice. That is correct for a log
	// and wrong for a table anybody counts, which is why it is named rather
	// than defaulted.
	WriteAppend = "append"

	// WriteMerge stages the batch into a temporary table and inserts it onto
	// the target with ON CONFLICT (ingestion_id) DO NOTHING, all inside one
	// transaction: BEGIN, CREATE TEMP … ON COMMIT DROP, COPY, INSERT, COMMIT.
	// A crash between any two of those leaves the table as it was.
	//
	// The FIRST delivery of an event wins. A redelivery is ignored, not
	// applied -- so a correction that arrives under the same ingestion_id does
	// not overwrite what is there. That is the right behaviour for an event,
	// which happened once and does not change, and the wrong one for a row
	// that carries a mutable state.
	//
	// It needs a UNIQUE index on ingestion_id, and the driver refuses without
	// one rather than silently appending -- which is the failure this mode
	// exists to prevent.
	WriteMerge = "merge"

	// WriteUpsert would be the other half: ON CONFLICT (ingestion_id) DO
	// UPDATE, where the LAST delivery wins. It is named here so the config can
	// refuse the word instead of accepting it and behaving like merge.
	//
	// Not implemented: it is a Dedup mode the SDK does not have, and adding
	// one means every writer -- BigQuery, MySQL, Redshift, Files, Pub/Sub --
	// answers for it explicitly, or it becomes a silent append in whichever
	// one was missed.
	WriteUpsert = "upsert"
)

// Defaults, and each one is a number with a reason.
const (
	defaultAddr = ":8080"
	// 9090 is what the engine serves its own on, so an operator running both
	// has one number to remember rather than two.
	defaultMetricsAddr = ":9090"
	// 1 MiB: one event, not a file. A body larger than this is a batch that
	// wants ndjson and a flush, or it is a mistake.
	defaultMaxBody Size = 1 << 20
	// A second is the longest a stream at one event a minute waits to land.
	defaultFlushEvery = time.Second
	// 500 messages is Pub/Sub's own publish-batch ceiling; sending more in one
	// call buys nothing because the client splits it again.
	defaultFlushRecords = 500

	// Four attempts over roughly seven seconds. Enough to ride out a restart
	// or a leader election on the other side, and short enough that a sink
	// which is actually down is known to be down rather than waited on.
	defaultAttempts   = 4
	defaultBackoff    = 500 * time.Millisecond
	defaultMaxBackoff = 10 * time.Second

	// Four deliveries at once. A batch is one round trip and mostly waiting, so
	// this is not a CPU number; it is how many outstanding calls a sink is
	// asked to carry, and four keeps a small pod from looking like a burst to
	// whatever is on the other side.
	// BigQueryFlushFloor is the shortest flush window a BigQuery sink may use.
	//
	// BigQuery allows 1,500 load jobs per table per DAY. The default window is
	// one second, which is 86,400 -- 57x the quota, exhausted in about
	// twenty-five minutes. It is not a problem for Pub/Sub, and the number came
	// from a Pub/Sub config, which is exactly how it would arrive here.
	//
	// Sixty seconds is 1,440 a day: inside the quota with room for a retry, and
	// it is Google's own framing of the limit. The real answer is the Storage
	// Write API, which is not written yet; until it is, the config refuses a
	// window it cannot honour rather than letting the first deploy find out.
	BigQueryFlushFloor = 60 * time.Second

	// drainFloor is the shortest drain budget, whatever the windows say.
	//
	// A stream flushing every second still has a queue of up to `queue`
	// batches behind it, and each delivery is a round trip that can retry.
	// Thirty seconds was the fixed value before this was derived, and it stays
	// as the floor.
	drainFloor = 30 * time.Second

	// httpShutdownBudget is how long the listener gets to stop accepting.
	//
	// Small, and separate from the drain's, which is the whole point: these
	// used to share one context, in this order, so a single slow reader -- a
	// large ndjson body, a client on a bad connection -- spent the drain's
	// budget before the drain began. In the worst case Close received an
	// already-expired context and the queue was abandoned.
	httpShutdownBudget = 5 * time.Second

	defaultWorkers = 4
	// Sixty-four batches waiting. At the default batch size that is 32,000
	// events of shock absorption -- roughly a sink pausing for a minute at
	// 500 events a second -- before any caller is told to slow down.
	defaultQueue = 64
	// The buffer's ceiling as a multiple of one batch. Twenty batches of room
	// above the flush size: enough that a brief handoff failure is invisible,
	// small enough that the number of events a crash can lose stays a number
	// the operator can state.
	defaultMaxRecordsFactor = 20
)

func (c *Config) check() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("the gateway has no `name`")
	}
	if c.Listen.Addr == "" {
		c.Listen.Addr = defaultAddr
	}
	if c.Listen.MaxBody == 0 {
		c.Listen.MaxBody = defaultMaxBody
	}
	// A file that did not mention metrics gets them, because an ingestion
	// service nobody can see is the failure mode this field exists against.
	// A file that said `addr: ""` meant it.
	c.Metrics.addr = defaultMetricsAddr
	if c.Metrics.Addr != nil {
		c.Metrics.addr = strings.TrimSpace(*c.Metrics.Addr)
	}
	if c.Metrics.addr != "" && c.Metrics.addr == c.Listen.Addr {
		// The same port would put the exposition behind the ingest mux, which
		// is the one place it must not be: that port is public by design, and
		// a scrape endpoint there publishes every stream name and destination.
		return fmt.Errorf("`metrics.addr` and `listen.addr` are both %q: the "+
			"exposition has to be on its own address, because the ingest port "+
			"is public and a /metrics on it would publish every stream name, "+
			"path and destination", c.Metrics.addr)
	}
	// The environment decides whether an open endpoint is allowed, and it is
	// read here rather than taken as a field: a config file that could declare
	// itself local would be a file that turns off authentication.
	env := os.Getenv("BREVIS_ENV")
	if env == "" {
		env = EnvLocal
	}
	if err := c.Listen.Auth.check(env); err != nil {
		return err
	}

	for i := range c.Streams {
		// `auto_table` turns a string in a payload into DDL. On an
		// unauthenticated endpoint that is any caller creating tables in a
		// production dataset, without limit, and a typo becoming a table
		// rather than an error. A NetworkPolicy does not cover it: that limits
		// who reaches the port, not which table name they ask for.
		if c.Streams[i].Sink.Type == SinkAutoTable && c.Listen.Auth.Type == "" {
			return fmt.Errorf("stream %q uses `auto_table` and `listen.auth` is "+
				"empty. A producer that can name a table can create one, so this "+
				"needs authentication even where an ordinary stream would not",
				c.Streams[i].Name)
		}
	}

	if len(c.Streams) == 0 {
		return fmt.Errorf("no `streams`: a gateway with no endpoint listens for nothing")
	}

	paths := map[string]string{}
	names := map[string]bool{}
	for i := range c.Streams {
		s := &c.Streams[i]
		s.maxBodyFloor = c.Listen.MaxBody
		if err := s.check(); err != nil {
			return fmt.Errorf("stream %q: %w", s.Name, err)
		}
		if names[s.Name] {
			return fmt.Errorf("two streams are called %q", s.Name)
		}
		names[s.Name] = true
		// Two streams on one path is a silent winner, and which one wins
		// depends on the order of a map somewhere.
		if other, taken := paths[s.Path]; taken {
			return fmt.Errorf("streams %q and %q both listen on %q", other, s.Name, s.Path)
		}
		paths[s.Path] = s.Name
	}
	return nil
}

func (s *Stream) check() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("it has no `name`")
	}
	if !strings.HasPrefix(s.Path, "/") {
		return fmt.Errorf("`path` is %q; it has to start with /", s.Path)
	}

	switch s.Format {
	case "":
		s.Format = FormatJSON
	case FormatJSON, FormatArray, FormatNDJSON:
	default:
		return fmt.Errorf("`format` is %q (use %s, %s or %s)",
			s.Format, FormatJSON, FormatArray, FormatNDJSON)
	}

	if err := s.Identity.check(); err != nil {
		return err
	}

	switch s.Buffer.Durability {
	case "":
		s.Buffer.Durability = DurabilityMemory
	case DurabilityMemory:
	default:
		// The tiers that do not exist yet are refused BY NAME, and that is on
		// purpose: somebody writing `disk` believes their events survive a
		// crash, and accepting the word while behaving like `memory` is the
		// one failure this field exists to prevent.
		return fmt.Errorf("`buffer.durability` is %q, and only %q is implemented. "+
			"`disk` and `synchronous` are planned; until they exist this refuses "+
			"rather than accepting the word and behaving like %q",
			s.Buffer.Durability, DurabilityMemory, DurabilityMemory)
	}
	if s.Buffer.Flush.Every == 0 {
		s.Buffer.Flush.Every = defaultFlushEvery
	}
	if s.Buffer.Flush.Records == 0 {
		s.Buffer.Flush.Records = defaultFlushRecords
	}
	if s.Buffer.Workers == 0 {
		s.Buffer.Workers = defaultWorkers
	}
	if s.Buffer.Workers < 0 {
		return fmt.Errorf("`buffer.workers` is %d: a stream with no worker "+
			"accepts events and delivers none", s.Buffer.Workers)
	}
	if s.Buffer.Queue == 0 {
		s.Buffer.Queue = defaultQueue
	}
	if s.Buffer.Queue < 0 {
		return fmt.Errorf("`buffer.queue` is %d", s.Buffer.Queue)
	}
	if s.Buffer.MaxRecords == 0 {
		s.Buffer.MaxRecords = s.Buffer.Flush.Records * defaultMaxRecordsFactor
	}
	if s.Buffer.MaxRecords < s.Buffer.Flush.Records {
		// Below the flush size the buffer could never reach a full batch, so
		// every request past the ceiling would be refused while the events
		// already held wait for the timer. The config says what it means
		// instead of behaving that way.
		return fmt.Errorf("`buffer.max_records` is %d and `buffer.flush.records` "+
			"is %d: the ceiling is below one batch, so a batch could never fill",
			s.Buffer.MaxRecords, s.Buffer.Flush.Records)
	}
	if s.Buffer.Flush.Records < 1 {
		return fmt.Errorf("`buffer.flush.records` is %d", s.Buffer.Flush.Records)
	}
	if s.Buffer.Flush.Size < 0 {
		return fmt.Errorf("`buffer.flush.size` is %s", s.Buffer.Flush.Size)
	}
	if s.Buffer.MaxBytes < 0 {
		return fmt.Errorf("`buffer.max_bytes` is %s", s.Buffer.MaxBytes)
	}
	if s.Buffer.MaxBytes > 0 && s.Buffer.Flush.Size > s.Buffer.MaxBytes {
		// The buffer could never reach one batch's worth, so every request
		// past the ceiling would be refused while what is held waits for the
		// timer. The same shape as the records check above, and the same
		// answer: say it rather than behave that way.
		return fmt.Errorf("`buffer.max_bytes` is %s and `buffer.flush.size` is %s: "+
			"the ceiling is below one batch, so a batch could never fill by size",
			s.Buffer.MaxBytes, s.Buffer.Flush.Size)
	}
	if s.Buffer.MaxBytes > 0 && s.Buffer.MaxBytes < s.maxBodyFloor {
		// One request has to fit. listen.max_body caps a body at 1 MiB by
		// default, so a buffer ceiling under that refuses a request the
		// listener already accepted -- a 503 nothing can clear, on every
		// attempt, forever.
		return fmt.Errorf("`buffer.max_bytes` is %s and `listen.max_body` is %s: "+
			"one request could never be admitted, so this stream would answer 503 "+
			"to everything",
			s.Buffer.MaxBytes, s.maxBodyFloor)
	}

	if s.Retry.Attempts == 0 {
		s.Retry.Attempts = defaultAttempts
	}
	if s.Retry.Attempts < 1 {
		return fmt.Errorf("`retry.attempts` is %d; 1 means try once and give up",
			s.Retry.Attempts)
	}
	if s.Retry.Backoff == 0 {
		s.Retry.Backoff = defaultBackoff
	}
	if s.Retry.MaxBackoff == 0 {
		s.Retry.MaxBackoff = defaultMaxBackoff
	}
	if s.Retry.MaxBackoff < s.Retry.Backoff {
		return fmt.Errorf("`retry.max_backoff` (%s) is below `retry.backoff` (%s)",
			s.Retry.MaxBackoff, s.Retry.Backoff)
	}

	if err := s.Sink.check(); err != nil {
		return err
	}

	// A window that cannot hold the quota is refused rather than accepted, for
	// the reason `durability: disk` is: somebody who wrote `1s` believes their
	// rows land in a second, and letting them find out through
	// `Exceeded rate limits` an hour later is the failure this prevents.
	if s.Sink.usesBigQuery() && s.Buffer.Flush.Every < BigQueryFlushFloor {
		return fmt.Errorf("`buffer.flush.every` is %s and this stream writes to "+
			"BigQuery, which allows 1,500 load jobs per table per day. That window "+
			"is %.0f a day, and the quota is gone in about %.0f minutes. Use %s or "+
			"more",
			s.Buffer.Flush.Every, (24*time.Hour).Seconds()/s.Buffer.Flush.Every.Seconds(),
			(1500 * s.Buffer.Flush.Every).Minutes(), BigQueryFlushFloor)
	}

	if o := s.Oversize; o != nil {
		if o.LargerThan == 0 {
			return fmt.Errorf("`oversize.larger_than` is empty: name the size " +
				"past which an event is archived instead of delivered")
		}
		if err := o.Archive.check(); err != nil {
			return fmt.Errorf("`oversize.archive`: %w", err)
		}
	}

	// Refused rather than defaulted. A stream with no dead letter loses a
	// refused batch to a log line, and an operator who never chose that is an
	// operator who does not know it is happening.
	if s.DeadLetter.Type == "" {
		return fmt.Errorf("no `dead_letter`: a batch the sink refuses has nowhere " +
			"to go but a log line, which is losing data quietly. Declare one -- " +
			"`{type: files, path: ./dead-letter/}` is enough to start")
	}
	if err := s.DeadLetter.check(); err != nil {
		return fmt.Errorf("`dead_letter`: %w", err)
	}
	return nil
}

func (i *Identity) check() error {
	var missing []string
	for name, v := range map[string]string{
		"provider": i.Provider, "entity": i.Entity,
		"source_key": i.SourceKey, "record_ts": i.RecordTS,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	// All four, always. The formula is frozen over exactly these, and an
	// ingestion_id computed over three of them is a different id -- so a
	// missing one is not a smaller identity, it is another one.
	return fmt.Errorf("`identity` is missing %s. All four are required: the "+
		"ingestion_id is a UUID v5 over provider|entity|source_key|record_ts and "+
		"the formula is frozen, so leaving one out produces a DIFFERENT id rather "+
		"than a weaker one", strings.Join(missing, ", "))
}

// usesBigQuery reports whether this sink lands in BigQuery, directly or
// through a router.
func (s Sink) usesBigQuery() bool {
	if s.Type == SinkBigQuery {
		return true
	}
	return s.Into != nil && s.Into.usesBigQuery()
}

// check is what is true of EVERY sink, and nothing more.
//
// The per-driver rules -- BigQuery needs a dataset, Redshift needs an s3://
// staging prefix and a role, a table sink needs `write` -- moved into the
// drivers when they moved into packages. That is not tidying: config.go cannot
// name them any more, because which drivers exist is a property of the BINARY
// now, and a slim build genuinely does not implement bigquery.
//
// So an unknown type is refused by the registry at New rather than here, with
// a message naming what THIS binary carries. Both still run before the
// listener opens, so nothing reaches a request that a config half understood.
func (s *Sink) check() error {
	if strings.TrimSpace(s.Type) == "" {
		return fmt.Errorf("`type` is empty")
	}
	if s.Type == SinkAutoTable {
		if err := s.Metastore.check(); err != nil {
			return err
		}
	}
	for _, a := range s.Attributes {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("`sink.attributes` holds an empty name")
		}
	}
	return nil
}

// DrainBudget is how long the drain may take.
//
// Declared wins. Otherwise it is the longest flush window, floored at thirty
// seconds.
//
// The window is the right quantity because it bounds what the buffer can be
// HOLDING when the signal arrives -- a 300-second stream can have 300 seconds
// of accepted events in memory, and the old fixed thirty was chosen when every
// window was one second.
//
// One window and not two: the drain does not wait for anything. It takes the
// pending batch immediately and the workers deliver in parallel, so draining is
// strictly faster than the accumulating was. Giving it as long as the fill took
// is already generous, and doubling it only lengthens how long a pod that is
// genuinely stuck takes to die -- which is a rollout everybody waits on.
//
// It is still a heuristic. What actually bounds the drain is the queue depth
// and the cost of one delivery, and neither is knowable here: a load job takes
// seconds, a file takes none. An installation that has measured its own should
// declare `shutdown.drain` and stop relying on this.
func (c *Config) DrainBudget() time.Duration {
	if c.Shutdown.Drain > 0 {
		return c.Shutdown.Drain
	}
	longest := time.Duration(0)
	for i := range c.Streams {
		if w := c.Streams[i].Buffer.Flush.Every; w > longest {
			longest = w
		}
	}
	if longest > drainFloor {
		return longest
	}
	return drainFloor
}

// GracePeriod is what terminationGracePeriodSeconds has to be at least.
//
// The gateway cannot read its own manifest, and Kubernetes' default is thirty
// seconds -- which equals the old fixed drain budget exactly, so there was no
// margin at all and a SIGKILL arrived while the drain was still inside its own
// deadline. It can at least state the number it needs, at boot, in the log
// whoever deploys it is already reading.
func (c *Config) GracePeriod() time.Duration {
	return c.DrainBudget() + httpShutdownBudget + 5*time.Second
}
