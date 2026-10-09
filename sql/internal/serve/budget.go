package serve

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// budgetWindow is how long spending is remembered.
//
// AN HOUR, AND A FIXED ONE rather than a sliding window. A sliding window
// needs every query's size and time kept for its whole length; a fixed one
// needs two numbers per connection. The cost of the simpler shape is stated
// rather than hidden: a budget can be spent twice across a boundary -- once
// at the end of one window and again at the start of the next -- so the real
// worst case over any hour is twice the number. For a ceiling whose purpose
// is to stop a runaway rather than to bill anybody, that is the right trade,
// and it is the kind of thing somebody should read here instead of deriving
// from a surprise.
const budgetWindow = time.Hour

// spending is what each connection has scanned in the current window.
type spending struct {
	mu   sync.Mutex
	used map[string]int64
	from time.Time
}

func newSpending() *spending { return &spending{used: map[string]int64{}, from: time.Now()} }

// allows reports whether `more` bytes may be scanned on this connection, and
// what has been spent so far.
func (s *spending) allows(connection string, more, budget int64) (bool, int64) {
	if budget <= 0 {
		return true, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollLocked()
	used := s.used[connection]
	return used+more <= budget, used
}

// record adds what a warehouse said it actually scanned.
func (s *spending) record(connection string, scanned int64) {
	if scanned <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollLocked()
	s.used[connection] += scanned
}

func (s *spending) rollLocked() {
	if time.Since(s.from) < budgetWindow {
		return
	}
	s.used = map[string]int64{}
	s.from = time.Now()
}

// rewind moves the window's start back, so a test can reach the next one
// without waiting an hour. It is the only thing here a test touches
// directly, and it changes nothing a request can see.
func (s *spending) rewind(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.from = s.from.Add(-d)
}

// overBudget is the refusal, with the numbers in it.
//
// 429 AND NOT 400. The request is not wrong and would be accepted later,
// which is exactly what the status means. A 400 would tell somebody to fix
// their SQL, and there is nothing in the SQL to fix.
func overBudget(scan, used, budget int64) refusal {
	return refusal{http.StatusTooManyRequests, "over-budget", fmt.Sprintf(
		"this query would scan %s, and %s of this connection's %s budget is already spent "+
			"in the current hour. It will run again when the hour turns.",
		BytesText(scan), BytesText(used), BytesText(budget))}
}
