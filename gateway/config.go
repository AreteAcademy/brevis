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

	SinkPubSub = "pubsub"
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
	if s.Buffer.Flush.Records < 1 {
		return fmt.Errorf("`buffer.flush.records` is %d", s.Buffer.Flush.Records)
	}

	return s.Sink.check()
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
		return fmt.Errorf("`sink.type` is empty (use %s)", SinkPubSub)
	case SinkPubSub:
	default:
		return fmt.Errorf("`sink.type` is %q, and only %q is implemented",
			s.Type, SinkPubSub)
	}
	if strings.TrimSpace(s.Project) == "" {
		return fmt.Errorf("`sink.project` is empty")
	}
	if strings.TrimSpace(s.Topic) == "" {
		return fmt.Errorf("`sink.topic` is empty")
	}
	for _, a := range s.Attributes {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("`sink.attributes` holds an empty name")
		}
	}
	return nil
}
