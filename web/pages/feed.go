package pages

import (
	"context"
	"strings"
	"time"

	evkind "github.com/AreteAcademy/brevis/internal/domain/events"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// FeedShown is how many events the Overview's log draws: the last hours of a
// busy installation, not its history -- that is what the run list is for.
const FeedShown = 50

// FeedLine is one event as the Overview's log draws it: when, from where, a
// sentence with its subject apart -- drawn as code -- who did it, where it
// leads, and a run's state for its dot.
type FeedLine struct {
	At             time.Time
	Source         string
	Before, After  string
	Subject, Actor string
	Href           string
	State          string
}

// FeedOf is the events as lines, in the order they came. A kind this version
// does not know -- written by a newer one -- is skipped rather than guessed.
func FeedOf(ctx context.Context, evs []postgres.Event) []FeedLine {
	out := make([]FeedLine, 0, len(evs))
	for _, e := range evs {
		source, ok := evkind.SourceOf(e.Kind)
		if !ok {
			continue
		}
		before, after := eventSentence(ctx, e.Kind)
		l := FeedLine{At: e.At, Source: sourceLabel(ctx, source), Before: before, After: after,
			Subject: e.Subject, Actor: e.Actor, State: runState(e.Kind)}
		switch {
		case e.RunID != nil:
			l.Href = "/runs/" + e.RunID.String()
		case source == evkind.FromWorkflow:
			l.Href = "/workflows/" + e.Subject
		}
		out = append(out, l)
	}
	return out
}

// subjectMark stands for the subject inside a sentence, so the catalog can
// put it where its language wants it and the page can still draw it as code.
const subjectMark = "\x00"

// eventSentence is what happened, in the reader's language, split around its
// subject. One literal key per kind: the catalog's tests read the keys the
// code asks for, and a key built at run time is one they cannot see.
func eventSentence(ctx context.Context, k evkind.Kind) (before, after string) {
	var s string
	switch k {
	case evkind.WorkflowPublished:
		s = i18n.T(ctx, "event.workflow.published", "subject", subjectMark)
	case evkind.WorkflowRemoved:
		s = i18n.T(ctx, "event.workflow.removed", "subject", subjectMark)
	case evkind.SchedulePaused:
		s = i18n.T(ctx, "event.schedule.paused", "subject", subjectMark)
	case evkind.ScheduleResumed:
		s = i18n.T(ctx, "event.schedule.resumed", "subject", subjectMark)
	case evkind.RunStarted:
		s = i18n.T(ctx, "event.run.started", "subject", subjectMark)
	case evkind.RunSucceeded:
		s = i18n.T(ctx, "event.run.succeeded", "subject", subjectMark)
	case evkind.RunFailed:
		s = i18n.T(ctx, "event.run.failed", "subject", subjectMark)
	case evkind.RunCanceled:
		s = i18n.T(ctx, "event.run.canceled", "subject", subjectMark)
	case evkind.StreamPublished:
		s = i18n.T(ctx, "event.stream.published", "subject", subjectMark)
	case evkind.GatewayUnpublished:
		s = i18n.T(ctx, "event.gateway.unpublished", "subject", subjectMark)
	case evkind.EndpointsPublished:
		s = i18n.T(ctx, "event.endpoints.published", "subject", subjectMark)
	case evkind.EndpointsUnpublished:
		s = i18n.T(ctx, "event.endpoints.unpublished", "subject", subjectMark)
	}
	before, after, _ = strings.Cut(s, subjectMark)
	return before, after
}

// sourceLabel names where an event comes from, as the menu names it.
func sourceLabel(ctx context.Context, s evkind.Source) string {
	switch s {
	case evkind.FromStream:
		return i18n.T(ctx, "event.source.stream")
	case evkind.FromEndpoint:
		return i18n.T(ctx, "event.source.endpoint")
	}
	return i18n.T(ctx, "event.source.workflow")
}

// runState is a run event's state, for its dot; "" for the rest, which have
// no state to show.
func runState(k evkind.Kind) string {
	switch k {
	case evkind.RunStarted:
		return "running"
	case evkind.RunSucceeded:
		return "success"
	case evkind.RunFailed:
		return "failed"
	case evkind.RunCanceled:
		return "canceled"
	}
	return ""
}
