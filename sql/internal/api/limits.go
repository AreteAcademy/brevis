package api

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Limits bound one request: how long, how many rows, how many bytes billed.
//
// FILE-WIDE, AND AN ENDPOINT MAY ONLY LOWER THEM. The file's numbers are what
// whoever deploys the API agreed to; an endpoint that could raise its own
// would make the file's a suggestion.
type Limits struct {
	Timeout        time.Duration `yaml:"timeout"`
	MaxRows        int           `yaml:"max_rows"`
	MaxBytesBilled ByteSize      `yaml:"max_bytes_billed"`
}

const (
	defaultTimeout = 10 * time.Second

	// defaultMaxBytesBilled is 1 GiB. A public endpoint over BigQuery with no
	// ceiling is a bill anybody holding a key can write, so the absence of a
	// number is a number. Postgres has nothing to bill and ignores it.
	defaultMaxBytesBilled = ByteSize(1 << 30)

	// hardMaxRows is the most any file may ask for. An answer is held in
	// memory, compressed and cached whole; a million rows is an export, and
	// this is not that.
	hardMaxRows = 100000

	// perTenantInFlight is how many requests one tenant may have waiting on
	// the warehouse at once. Fixed, for now -- see the plan.
	perTenantInFlight = 4
)

func (l *Limits) check(where string) error {
	if l.Timeout < 0 {
		return fmt.Errorf("%s `timeout` is %s; it bounds a request and cannot be negative", where, l.Timeout)
	}
	if l.MaxRows < 0 || l.MaxRows > hardMaxRows {
		return fmt.Errorf("%s `max_rows` is %d; from 1 to %d", where, l.MaxRows, hardMaxRows)
	}
	if l.MaxBytesBilled < 0 {
		return fmt.Errorf("%s `max_bytes_billed` cannot be negative", where)
	}
	return nil
}

// withDefaults fills what the file left out.
func (l Limits) withDefaults() Limits {
	if l.Timeout == 0 {
		l.Timeout = defaultTimeout
	}
	if l.MaxRows == 0 {
		l.MaxRows = DefaultMaxRows
	}
	if l.MaxBytesBilled == 0 {
		l.MaxBytesBilled = defaultMaxBytesBilled
	}
	return l
}

// lowered is an endpoint's limits over the file's, refused where it raises.
func (l Limits) lowered(endpoint string, own *Limits) (Limits, error) {
	if own == nil {
		return l, nil
	}
	if err := own.check("endpoint " + endpoint + ":"); err != nil {
		return l, err
	}
	raise := func(what string) error {
		return fmt.Errorf("endpoint %s raises `%s` over the file's; an endpoint may only lower a limit", endpoint, what)
	}
	if own.Timeout != 0 {
		if own.Timeout > l.Timeout {
			return l, raise("timeout")
		}
		l.Timeout = own.Timeout
	}
	if own.MaxRows != 0 {
		if own.MaxRows > l.MaxRows {
			return l, raise("max_rows")
		}
		l.MaxRows = own.MaxRows
	}
	if own.MaxBytesBilled != 0 {
		if own.MaxBytesBilled > l.MaxBytesBilled {
			return l, raise("max_bytes_billed")
		}
		l.MaxBytesBilled = own.MaxBytesBilled
	}
	return l, nil
}

// ByteSize is a number of bytes, written `1GiB`, `512MiB` or a bare number.
type ByteSize int64

var byteUnits = []struct {
	suffix string
	n      int64
}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}}

// UnmarshalYAML reads binary units only. `1GB` is refused rather than read
// as either 10^9 or 2^30: a ceiling on money should not be off by 7%
// depending on which the reader assumed.
func (b *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	raw := strings.TrimSpace(node.Value)
	for _, u := range byteUnits {
		if num, found := strings.CutSuffix(raw, u.suffix); found {
			n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
			if err != nil {
				break
			}
			*b = ByteSize(n * u.n)
			return nil
		}
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		*b = ByteSize(n)
		return nil
	}
	return fmt.Errorf("%q is not a size: a whole number of B, KiB, MiB, GiB or TiB", raw)
}

func (b ByteSize) String() string {
	for _, u := range byteUnits {
		if int64(b) >= u.n && int64(b)%u.n == 0 {
			return fmt.Sprintf("%d %s", int64(b)/u.n, u.suffix)
		}
	}
	return fmt.Sprintf("%d B", int64(b))
}

// slots are each tenant's requests in flight.
type slots struct {
	mu   sync.Mutex
	used map[string]int
}

// take reserves one of a tenant's slots, or reports there is none. It never
// waits: a request queued behind four slow ones is a fifth slow one, and the
// caller is better told to come back.
func (s *slots) take(tenant string) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used == nil {
		s.used = map[string]int{}
	}
	if s.used[tenant] >= perTenantInFlight {
		return nil, false
	}
	s.used[tenant]++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.used[tenant]--; s.used[tenant] == 0 {
			delete(s.used, tenant)
		}
	}, true
}
