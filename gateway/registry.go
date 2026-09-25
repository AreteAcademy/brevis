package gateway

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/AreteAcademy/brevis/sdk"
)

// Build is what a sink's constructor is handed.
//
// A struct and not three arguments, because a driver added later will want
// something these do not carry, and growing a struct does not break the five
// drivers that came before it.
type Build struct {
	// Ctx bounds construction, which for a cloud driver means resolving
	// credentials. It is the startup context, not a request's.
	Ctx context.Context

	// Sink is the YAML block, whole. The driver reads the fields that are its
	// own and REFUSES the ones it needs and did not get -- which is why the
	// per-driver checks live in the driver rather than in config.go.
	Sink Sink

	// Stores is the object-store backends this binary carries, keyed by
	// scheme. Nil when none were registered, which is the ordinary case for a
	// gateway that writes to local paths.
	Stores *Stores

	// Sinks is the registry this sink came from, so a driver that ROUTES can
	// build the one it routes into -- through the same registry, which is what
	// makes a binary that did not compile in BigQuery unable to route into it.
	Sinks *Sinks

	// Stream and Gateway name where this sink sits, so a driver can stamp them
	// onto what it writes. A table fed by several routes still says which one
	// wrote each line.
	Stream  string
	Gateway string

	// Target is the table a routing driver wants created, when it wants one.
	// Nil is the ordinary case: a sink named in the YAML writes to a table
	// somebody already made.
	Target *Target
}

// Target is the shape a driver should create, when the caller knows it and the
// YAML does not.
//
// It exists for `auto_table`, which turns a string in a payload into a table
// and therefore has to say what that table looks like. Everything here is the
// ROUTER's decision and never the producer's: a producer that could choose the
// schema could choose a partition column it controls.
type Target struct {
	// Schema is the columns, with types. It is also what a later `evolve`
	// compares against.
	Schema sdk.Schema

	// PartitionBy names the column a created table is partitioned on, by day.
	// Empty creates an unpartitioned table, which for a landing table is a
	// query bill that grows forever.
	PartitionBy string

	// ClusterBy names the columns a created table is clustered on.
	ClusterBy []string

	// Create lets the driver create the table when it is absent. Off by
	// default everywhere else, because a loader that creates tables by
	// accident turns a typo into a second table nobody is reading.
	Create bool
}

// BuildSink resolves one sink through a registry.
//
// Exported because a routing driver lives in its own package and has to build
// the destination it routes into. It is the same path New takes, so a nested
// sink is refused the same way and at the same moment as a named one.
func BuildSink(b Build) (Sinker, error) {
	if b.Sinks == nil {
		return nil, fmt.Errorf("no sink registry: a routing driver needs one to " +
			"build what it routes into")
	}
	fn, err := b.Sinks.get(b.Sink.Type)
	if err != nil {
		return nil, err
	}
	return fn(b)
}

// SinkFunc builds one destination.
type SinkFunc func(Build) (Sinker, error)

// Sinks is what this binary knows how to write to.
//
// It is the hook registry's twin, and for the twin reason. A `switch` naming
// every constructor -- which is what this was -- makes the linker keep every
// driver, so a gateway that only ever writes to Postgres still carried the AWS
// SDK, the Google stack and Arrow: 48.9 MB against 10.0 for the same gateway
// with only what it uses.
//
// The import list is the selection. That is how database/sql has always worked,
// and it is already the rule one level down: sdk/store/s3 says "importing it
// costs you the AWS SDK" precisely so a fetcher reading GCS does not pay for it.
//
// The published image registers all six. A binary of your own registers what it
// needs -- and anybody with a hook is compiling one already, so for them this
// costs nothing.
type Sinks struct {
	mu sync.RWMutex
	by map[string]SinkFunc
}

func NewSinks() *Sinks { return &Sinks{by: map[string]SinkFunc{}} }

// Register adds one. A duplicate name is REFUSED rather than overwritten, the
// same as a hook: a silently replaced registration is a bug that shows up in
// production, when the wrong driver runs.
func (s *Sinks) Register(name string, fn SinkFunc) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("a sink needs a type name")
	}
	if fn == nil {
		return fmt.Errorf("sink %q is nil", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.by[name]; taken {
		return fmt.Errorf("a sink called %q is already registered", name)
	}
	s.by[name] = fn
	return nil
}

// MustRegister is Register for a main with nowhere to return an error.
func (s *Sinks) MustRegister(name string, fn SinkFunc) {
	if err := s.Register(name, fn); err != nil {
		panic(err)
	}
}

// Names lists what this binary carries, sorted.
func (s *Sinks) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.by))
	for n := range s.by {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// get resolves a type name, and a miss says what THIS binary has.
//
// "this binary" and not a fixed list, which is the honest form: a slim build
// genuinely does not implement bigquery, and telling its operator that bigquery
// exists somewhere else would send them looking for a config mistake they did
// not make.
func (s *Sinks) get(name string) (SinkFunc, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if fn, ok := s.by[name]; ok {
		return fn, nil
	}
	if len(s.by) == 0 {
		return nil, fmt.Errorf("sink type %q: this binary registers no sink at all. "+
			"They are Go, compiled in: register them on a gateway.Sinks and pass it "+
			"to gateway.Main -- see gateway/example", name)
	}
	names := make([]string, 0, len(s.by))
	for n := range s.by {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("sink type %q is not one this binary carries (it has: %s). "+
		"Sinks are compiled in, so this is a build that left it out rather than a "+
		"destination that does not exist", name, strings.Join(names, ", "))
}

// StoreFunc opens an object-store backend.
type StoreFunc func(context.Context) (sdk.Store, error)

// Stores is the object-store backends this binary carries, by URL scheme.
//
// Separate from Sinks because a scheme is not a destination: `files` is one
// sink that writes to a directory, to gs:// and to s3://, and which of the
// three a deployment needs is not what its sink type says. Keeping them apart
// is what lets a slim build have `files` for its dead letter without the AWS
// SDK -- 10.0 MB against 13.5.
type Stores struct {
	mu sync.RWMutex
	by map[string]StoreFunc
}

func NewStores() *Stores { return &Stores{by: map[string]StoreFunc{}} }

// Register adds a backend for one scheme: "s3", "gs".
func (s *Stores) Register(scheme string, fn StoreFunc) error {
	if strings.TrimSpace(scheme) == "" {
		return fmt.Errorf("a store needs a scheme")
	}
	if fn == nil {
		return fmt.Errorf("store %q is nil", scheme)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.by[scheme]; taken {
		return fmt.Errorf("a store for %q is already registered", scheme)
	}
	s.by[scheme] = fn
	return nil
}

// MustRegister is Register for a main with nowhere to return an error.
func (s *Stores) MustRegister(scheme string, fn StoreFunc) {
	if err := s.Register(scheme, fn); err != nil {
		panic(err)
	}
}

// Open returns the backend a path needs, or nil for a local path.
//
// The refusal is the useful part: a path with a scheme this binary has no
// backend for fails HERE, at startup, naming the scheme and what is carried.
// Before the registry existed the same mistake failed on the first WRITE --
// and for a dead letter that means "the dead letter refused them too, and they
// are lost".
func (s *Stores) Open(ctx context.Context, path string) (sdk.Store, error) {
	scheme, _, found := strings.Cut(path, "://")
	if !found {
		return nil, nil // a local path needs no backend
	}
	if s == nil {
		return nil, fmt.Errorf("the path %q is %s and this binary carries no object "+
			"store at all. Register one -- gateway/store/s3 or gateway/store/gcs -- "+
			"or point it at a local directory", path, scheme)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn, ok := s.by[scheme]
	if !ok {
		have := make([]string, 0, len(s.by))
		for k := range s.by {
			have = append(have, k)
		}
		sort.Strings(have)
		if len(have) == 0 {
			return nil, fmt.Errorf("the path %q is %s and this binary carries no "+
				"object store at all", path, scheme)
		}
		return nil, fmt.Errorf("the path %q is %s and this binary carries no backend "+
			"for it (it has: %s)", path, scheme, strings.Join(have, ", "))
	}
	return fn(ctx)
}

// The helpers below are what the driver packages share, and they are exported
// for exactly that: a driver lives in its own package now, and a rule that
// every table sink has to follow cannot live in only one of them.

// CheckWrite settles how rows land. One function for every table-shaped sink,
// because the question is the same everywhere and the answer has to be too: a
// `merge` that meant something different in MySQL than in Postgres would be one
// word with two meanings in the same file.
//
// In all four it is the idempotent insert, and the FIRST delivery wins:
//
//	postgres  INSERT … ON CONFLICT (ingestion_id) DO NOTHING
//	mysql     INSERT IGNORE
//	bigquery  MERGE … WHEN NOT MATCHED THEN INSERT
//	redshift  MERGE … WHEN NOT MATCHED THEN INSERT
func CheckWrite(s Sink) error {
	switch s.Write {
	case WriteAppend, WriteMerge:
		return nil
	case WriteUpsert:
		return fmt.Errorf("`write: %s` is not implemented yet. %s ignores a "+
			"redelivery (the first delivery wins); %s would apply it (the last "+
			"delivery wins). They differ on whether a correction overwrites, so "+
			"this refuses rather than giving you one under the other's name",
			WriteUpsert, WriteMerge, WriteUpsert)
	case "":
		// Not defaulted. Appending a redelivery into a table somebody counts,
		// and merging into a log that wanted every arrival, are both wrong --
		// and which is which is a property of the table, not of this gateway.
		return fmt.Errorf("`write` is empty (use %s or %s): appending a "+
			"redelivery into a table somebody counts and merging into a log "+
			"that wanted every arrival are both wrong, and only the table's "+
			"owner knows which it is", WriteAppend, WriteMerge)
	default:
		return fmt.Errorf("`write` is %q (use %s or %s)", s.Write, WriteAppend, WriteMerge)
	}
}

// CheckTable requires a destination to write into.
func CheckTable(s Sink) error {
	if strings.TrimSpace(s.Table) == "" {
		return fmt.Errorf("`table` is empty (schema-qualified: landing.clicks)")
	}
	return nil
}

// DedupFor turns the config's word into the SDK's mode.
//
// CheckWrite already refused anything that is not one of the two words, so the
// default here is reached only by a driver that forgot to call it -- and
// DedupNone is the safe half of that mistake: it appends where it should have
// merged, which a UNIQUE index turns into a loud failure rather than a silent
// duplicate.
func DedupFor(write string) sdk.Dedup {
	if write == WriteMerge {
		return sdk.DedupMerge
	}
	return sdk.DedupNone
}

// DSNFrom reads the connection string out of the environment variable the
// config named. The config carries the NAME; a DSN carries a password and the
// file is in git.
func DSNFrom(s Sink) (string, error) {
	if strings.TrimSpace(s.DSNFrom) == "" {
		return "", fmt.Errorf("`dsn_from` is empty: name the environment variable " +
			"holding the connection string, never the string")
	}
	dsn, set := os.LookupEnv(s.DSNFrom)
	if !set || strings.TrimSpace(dsn) == "" {
		return "", fmt.Errorf("%s is empty, and it is where the connection string "+
			"for %s was meant to be", s.DSNFrom, s.Table)
	}
	return dsn, nil
}
