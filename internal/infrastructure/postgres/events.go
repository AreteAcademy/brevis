package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/AreteAcademy/brevis/internal/domain/events"
)

// EventRepo reads the console's events feed.
type EventRepo struct{ pool *Pool }

func NewEventRepo(p *Pool) *EventRepo { return &EventRepo{pool: p} }

// Event is one thing that happened: an action from the events table, or a
// run starting or ending, read from runs.
type Event struct {
	At      time.Time
	Kind    events.Kind
	Subject string
	// Actor is who did it: the console's user, "cli", or "" for a console
	// with no login and for a run.
	Actor string
	// RunID and Trigger are a run's; nil and "" on the rest.
	RunID   *uuid.UUID
	Trigger string
	Detail  map[string]any

	rank int
	key  string
}

// EventQuery is a page of the feed. Before is the cursor a previous page
// returned, "" for the newest; Source narrows it to one source, "" for all.
type EventQuery struct {
	Before string
	Source events.Source
	Limit  int
}

const (
	// DefaultEventPage is a page when none is asked for.
	DefaultEventPage = 50
	// MaxEventPage is the most one page reads.
	MaxEventPage = 200
)

// ErrBadCursor is a cursor the feed did not write.
var ErrBadCursor = errors.New("not a cursor of the events feed")

// The three branches, and their rank: at the same instant an action sorts
// before a run's end, and a run's end before its start.
const (
	rankStarted = iota
	rankFinished
	rankAction
)

// cursor is where a page ended: the instant, the branch and an id within it.
// Three parts, because several things happen in the same microsecond and the
// order has to be fixed whatever the page size.
type cursor struct {
	At   time.Time `json:"at"`
	Rank int       `json:"r"`
	Key  string    `json:"k"`
}

func (c cursor) String() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func parseCursor(s string) (*cursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrBadCursor
	}
	var c cursor
	if json.Unmarshal(raw, &c) != nil || c.At.IsZero() || c.Rank < rankStarted || c.Rank > rankAction {
		return nil, ErrBadCursor
	}
	return &c, nil
}

// args numbers a query's parameters as they are used: pgx refuses one that
// the SQL never mentions, and which branches are in depends on the filter.
type args []any

func (a *args) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

var (
	noRun  = uuid.Nil
	allRun = uuid.Max
	// endOfTime stands for "no cursor": every row is before it.
	endOfTime = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
)

// recentEventsQuery is three lists merged: each branch reads its own index
// newest first, from the cursor, and stops at the page's size, so the merge
// never sorts more than three pages however long the history.
//
// A branch's bound depends on its rank against the cursor's: a branch ranked
// after the cursor's at the same instant still has that instant to give, one
// ranked before it does not, and the cursor's own branch continues past the
// cursor's id.
func recentEventsQuery(q EventQuery) (string, []any, error) {
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = DefaultEventPage
	case limit > MaxEventPage:
		limit = MaxEventPage
	}
	c, err := parseCursor(q.Before)
	if err != nil {
		return "", nil, err
	}
	var stored []string
	for _, k := range events.Kinds {
		s, _ := events.SourceOf(k)
		if strings.HasPrefix(string(k), "run.") || q.Source != "" && s != q.Source {
			continue
		}
		stored = append(stored, string(k))
	}
	withRuns := q.Source == "" || q.Source == events.FromWorkflow
	if q.Source != "" && q.Source != events.FromWorkflow && q.Source != events.FromStream && q.Source != events.FromEndpoint {
		return "", nil, fmt.Errorf("unknown source %q", q.Source)
	}

	at := endOfTime
	if c != nil {
		at = c.At
	}
	var a args
	atP, limP := a.add(at), a.add(limit+1)

	// bound is the branch's upper key: everything at that instant, nothing,
	// or what is past the cursor's id.
	bound := func(rank int, all, none any, parse func(string) (any, error)) (any, error) {
		switch {
		case c == nil || rank < c.Rank:
			return all, nil
		case rank > c.Rank:
			return none, nil
		}
		return parse(c.Key)
	}
	byUUID := func(s string) (any, error) {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, ErrBadCursor
		}
		return id, nil
	}

	var branches []string
	if len(stored) > 0 {
		id, err := bound(rankAction, int64(math.MaxInt64), int64(0), func(s string) (any, error) {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, ErrBadCursor
			}
			return n, nil
		})
		if err != nil {
			return "", nil, err
		}
		branches = append(branches, fmt.Sprintf(`(
		    SELECT at, %d AS rank, lpad(id::text, 20, '0') AS key, kind, subject, actor,
		           NULL::uuid AS run_id, '' AS trigger, detail
		    FROM events
		    WHERE kind = ANY(%s) AND (at, id) < (%s, %s)
		    ORDER BY at DESC, id DESC LIMIT %s)`, rankAction, a.add(stored), atP, a.add(id), limP))
	}
	if withRuns {
		fin, err := bound(rankFinished, allRun, noRun, byUUID)
		if err != nil {
			return "", nil, err
		}
		branches = append(branches, fmt.Sprintf(`(
		    SELECT terminado_em, %d, id::text,
		           CASE status WHEN 'success' THEN '%s' WHEN 'failed' THEN '%s' ELSE '%s' END,
		           workflow_slug, CASE status WHEN 'canceled' THEN canceled_by ELSE '' END,
		           id AS run_id, trigger_type, '{}'::jsonb
		    FROM runs r
		    WHERE terminado_em IS NOT NULL AND status IN ('success', 'failed', 'canceled')
		      AND (terminado_em, r.id) < (%s, %s)
		    ORDER BY terminado_em DESC, r.id DESC LIMIT %s)`,
			rankFinished, events.RunSucceeded, events.RunFailed, events.RunCanceled, atP, a.add(fin), limP))

		start, err := bound(rankStarted, allRun, noRun, byUUID)
		if err != nil {
			return "", nil, err
		}
		branches = append(branches, fmt.Sprintf(`(
		    SELECT iniciado_em, %d, id::text, '%s', workflow_slug, '', id AS run_id, trigger_type, '{}'::jsonb
		    FROM runs r
		    WHERE iniciado_em IS NOT NULL AND (iniciado_em, r.id) < (%s, %s)
		    ORDER BY iniciado_em DESC, r.id DESC LIMIT %s)`,
			rankStarted, events.RunStarted, atP, a.add(start), limP))
	}
	sql := `SELECT * FROM (` + strings.Join(branches, "\n\t\tUNION ALL ") + `) feed
		ORDER BY at DESC, rank DESC, key DESC LIMIT ` + limP
	return sql, a, nil
}

// RecentEvents is a page of what happened, newest first, and the cursor of
// the next page -- "" when this was the last.
func (r *EventRepo) RecentEvents(ctx context.Context, q EventQuery) ([]Event, string, error) {
	sql, a, err := recentEventsQuery(q)
	if err != nil {
		return nil, "", err
	}
	rows, err := r.pool.Query(ctx, sql, a...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.At, &e.rank, &e.key, &e.Kind, &e.Subject, &e.Actor,
			&e.RunID, &e.Trigger, &e.Detail); err != nil {
			return nil, "", err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	// One row more than the page was read, to know whether there is a next.
	limit := len(out)
	if lim := a[1].(int); limit == lim {
		out = out[:lim-1]
		last := out[len(out)-1]
		key := last.key
		if last.rank == rankAction {
			key = strings.TrimLeft(key, "0")
		}
		return out, cursor{At: last.At, Rank: last.rank, Key: key}.String(), nil
	}
	return out, "", nil
}

// recordEvent writes an event in the action's own transaction: an action
// without its event, or the reverse, would be the feed lying. Unexported on
// purpose -- an event is only ever the side of an action.
//
// detail holds names, counts and flags only, never a configuration.
func recordEvent(ctx context.Context, tx pgx.Tx, kind events.Kind, subject, actor string, detail map[string]any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO events (kind, subject, actor, detail) VALUES ($1, $2, $3, $4)`,
		kind, subject, actor, raw)
	return err
}
