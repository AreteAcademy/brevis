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
	Name    string   `yaml:"name"`
	Listen  Listen   `yaml:"listen"`
	Streams []Stream `yaml:"streams"`
}

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

	Buffer Buffer `yaml:"buffer"`
	Sink   Sink   `yaml:"sink"`

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
}

type Flush struct {
	// Every is the age at which a partial batch goes anyway, so a slow stream
	// is not a stream that never lands.
	Every time.Duration `yaml:"every"`

	// Records is the size that sends one immediately.
	Records int `yaml:"records"`
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

	SinkPubSub   = "pubsub"
	SinkFiles    = "files"
	SinkPostgres = "postgres"
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

	if len(c.Streams) == 0 {
		return fmt.Errorf("no `streams`: a gateway with no endpoint listens for nothing")
	}

	paths := map[string]string{}
	names := map[string]bool{}
	for i := range c.Streams {
		s := &c.Streams[i]
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

func (s *Sink) check() error {
	switch s.Type {
	case "":
		return fmt.Errorf("`type` is empty (use %s, %s or %s)",
			SinkPubSub, SinkPostgres, SinkFiles)
	case SinkPubSub:
		if strings.TrimSpace(s.Project) == "" {
			return fmt.Errorf("`project` is empty")
		}
		if strings.TrimSpace(s.Topic) == "" {
			return fmt.Errorf("`topic` is empty")
		}
	case SinkPostgres:
		if strings.TrimSpace(s.DSNFrom) == "" {
			return fmt.Errorf("`dsn_from` is empty: name the environment variable " +
				"holding the connection string, never the string")
		}
		if strings.TrimSpace(s.Table) == "" {
			return fmt.Errorf("`table` is empty (schema-qualified: landing.clicks)")
		}
		switch s.Write {
		case WriteAppend, WriteMerge:
		case WriteUpsert:
			return fmt.Errorf("`write: %s` is not implemented yet. %s ignores a "+
				"redelivery (ON CONFLICT DO NOTHING, first delivery wins); %s would "+
				"apply it (DO UPDATE, last delivery wins). They differ on whether a "+
				"correction overwrites, so this refuses rather than giving you one "+
				"under the other's name", WriteUpsert, WriteMerge, WriteUpsert)
		case "":
			// Not defaulted. Appending a redelivery into a table somebody
			// counts, and merging into a log that wanted every arrival, are
			// both wrong -- and which is which is a property of the table, not
			// of this gateway.
			return fmt.Errorf("`write` is empty (use %s or %s): appending a "+
				"redelivery into a table somebody counts and merging into a log "+
				"that wanted every arrival are both wrong, and only the table's "+
				"owner knows which it is", WriteAppend, WriteMerge)
		default:
			return fmt.Errorf("`write` is %q (use %s or %s)", s.Write, WriteAppend, WriteMerge)
		}

	case SinkFiles:
		// A local directory, gs:// or s3://: to.Files reads all three, so the
		// dead letter can be a folder on a laptop and a bucket in production
		// without the gateway learning a second idea of what a path is.
		if strings.TrimSpace(s.Path) == "" {
			return fmt.Errorf("`path` is empty (a directory, gs:// or s3://)")
		}
	default:
		return fmt.Errorf("`type` is %q, and only %s, %s and %s are implemented",
			s.Type, SinkPubSub, SinkPostgres, SinkFiles)
	}
	for _, a := range s.Attributes {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("`sink.attributes` holds an empty name")
		}
	}
	return nil
}
