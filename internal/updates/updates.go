// Package updates tells the console when a newer Brevis has been released.
//
// It reads the repository's releases from GitHub every few hours and keeps the
// newest ENGINE release (`vX.Y.Z`; the SDK, the gateway and brevis-sql are
// tagged `sdk/v…`, `gateway/v…` and so on, and are another program's news).
// Nothing is sent: it is a plain GET of a public list. An installation that
// must not reach the internet sets BREVIS_UPDATE_CHECK=off.
package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Release is a published version and the page that describes it.
type Release struct {
	Tag       string // "v0.41.0"
	URL       string
	Notes     string // the release notes, as safe HTML
	Published time.Time
}

type listed struct {
	Tag        string    `json:"tag_name"`
	URL        string    `json:"html_url"`
	NotesHTML  string    `json:"body_html"`
	Published  time.Time `json:"published_at"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
}

// Interval is how often the list is read. GitHub allows sixty anonymous reads
// an hour; this uses four a day.
const Interval = 6 * time.Hour

const releasesURL = "https://api.github.com/repos/AreteAcademy/brevis/releases?per_page=30"

// Checker holds the newest release seen.
type Checker struct {
	current [3]int
	fetch   func(ctx context.Context) ([]listed, error)
	log     *slog.Logger

	mu     sync.RWMutex
	latest Release
}

var engineTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

func parse(tag string) ([3]int, bool) {
	if tag != "" && tag[0] != 'v' {
		tag = "v" + tag
	}
	m := engineTag.FindStringSubmatch(tag)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

func newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// New returns a checker for a console running current, or nil when there is
// nothing to compare: a development build ("dev") has no version to be behind.
func New(current string, log *slog.Logger) *Checker {
	v, ok := parse(current)
	if !ok {
		return nil
	}
	return &Checker{current: v, log: log, fetch: fetchGitHub}
}

func fetchGitHub(ctx context.Context) ([]listed, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return nil, err
	}
	// The html media type adds `body_html`: the notes as GitHub renders them.
	req.Header.Set("Accept", "application/vnd.github.html+json")
	req.Header.Set("User-Agent", "brevis-update-check")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", res.Status)
	}
	var out []listed
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Check reads the list once. A failure keeps what was known: an offline host
// is not a console that forgot there was an update.
func (c *Checker) Check(ctx context.Context) {
	rels, err := c.fetch(ctx)
	if err != nil {
		c.log.Debug("could not check for a new Brevis", "error", err)
		return
	}
	var best Release
	var bestV [3]int
	for _, r := range rels {
		v, ok := parse(r.Tag)
		if !ok || r.Draft || r.Prerelease || r.Tag[0] != 'v' {
			continue
		}
		if best.Tag == "" || newer(v, bestV) {
			best, bestV = Release{Tag: r.Tag, URL: r.URL, Notes: Sanitize(r.NotesHTML), Published: r.Published}, v
		}
	}
	c.mu.Lock()
	c.latest = best
	c.mu.Unlock()
}

// Run checks now and then every Interval, until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	c.Check(ctx)
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Check(ctx)
		}
	}
}

// Latest is the newest release seen, and whether it is newer than the one
// running. ok is false until a read has worked (or when it never does: an
// offline host).
func (c *Checker) Latest() (r Release, newer, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, parsed := parse(c.latest.Tag)
	if !parsed {
		return Release{}, false, false
	}
	return c.latest, ComesAfter(v, c.current), true
}

// ComesAfter reports whether version a is newer than b.
func ComesAfter(a, b [3]int) bool { return newer(a, b) }
