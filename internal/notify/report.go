package notify

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Report is the periodic summary. It is NOT an Alert, and the two are kept
// apart on purpose.
//
// They share a delivery channel and nothing else. An alert is triggered by an
// EVENT, is about one run, uses data already recorded, and is wanted NOW; a
// report is triggered by a SCHEDULE, is about everything in a window, is
// aggregated, and is wanted on Monday. One table serving both would make one
// access pattern serve two, and one delivery path carry two SLAs -- which is
// why a report does not go through the alerts outbox: losing an alert is an
// outage nobody hears about, and missing one week's summary is next week's
// summary.
type Report struct {
	From, To    time.Time
	Environment string

	Runs      int
	Succeeded int
	Failed    int

	// Workflows carries the per-pipeline numbers, and it is deliberately not a
	// map: the order is part of the message.
	Workflows []WorkflowInsight
}

// WorkflowInsight is one pipeline's week.
type WorkflowInsight struct {
	Slug   string
	Runs   int
	Failed int

	// Durations of the runs that FINISHED. A run still going has no duration,
	// and counting it as zero would drag every average down at exactly the
	// moment something is stuck.
	Min, Avg, Max time.Duration

	// Rows and Bytes come from the SDK's stage numbers, summed over the
	// SUCCESSFUL attempts only. Counting a failed attempt's rows would report
	// work that was rolled back, and counting every attempt of a retried step
	// would report the same rows twice.
	//
	// Zero for a step that is not an SDK one, which is most of them, and that
	// is why the message leaves the column out rather than printing 0.
	Rows  int64
	Bytes int64
}

// SuccessRate is a percentage, or -1 when there were no runs. A rate of 100%
// out of nothing is the most reassuring number a report can print and the least
// true one.
func (w WorkflowInsight) SuccessRate() float64 {
	if w.Runs == 0 {
		return -1
	}
	return float64(w.Runs-w.Failed) / float64(w.Runs) * 100
}

// SuccessRate for the whole window. Same rule.
func (r Report) SuccessRate() float64 {
	if r.Runs == 0 {
		return -1
	}
	return float64(r.Succeeded) / float64(r.Runs) * 100
}

// Worst returns the pipelines with failures, most first. They are the reason
// somebody opens the message.
func (r Report) Worst(limit int) []WorkflowInsight {
	var out []WorkflowInsight
	for _, w := range r.Workflows {
		if w.Failed > 0 {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Failed != out[j].Failed {
			return out[i].Failed > out[j].Failed
		}
		return out[i].Slug < out[j].Slug
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Slowest returns the pipelines that took longest, by their maximum. The
// maximum and not the average, because a report exists to surface the run that
// nearly did not finish, and an average hides it behind thirty fast ones.
func (r Report) Slowest(limit int) []WorkflowInsight {
	out := append([]WorkflowInsight(nil), r.Workflows...)
	sort.Slice(out, func(i, j int) bool { return out[i].Max > out[j].Max })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Reporter delivers a Report. A separate interface from Notificador because the
// two carry different promises: a lost alert is somebody not being woken up, a
// missed report is a week without a summary.
type Reporter interface {
	Report(ctx context.Context, r Report) error
}

// Report posts the summary.
func (s *Slack) Report(ctx context.Context, r Report) error {
	if s.Webhook == "" {
		return nil
	}
	return s.post(ctx, s.reportMessage(r))
}

func (s *Slack) reportMessage(r Report) map[string]any {
	window := fmt.Sprintf("%s → %s",
		r.From.Local().Format("02/01"), r.To.Local().Format("02/01/2006"))

	summary := fmt.Sprintf("*%d* runs · *%d* succeeded · *%d* failed", r.Runs, r.Succeeded, r.Failed)
	if rate := r.SuccessRate(); rate >= 0 {
		summary += fmt.Sprintf("  (%.1f%%)", rate)
	} else {
		// The window was empty. Saying so is the point: a report that renders
		// "100%" out of nothing is the most reassuring number it can print and
		// the least true one, and an empty window usually means the scheduler
		// was down, not that everything was fine.
		summary = "*No run in this window.* Either nothing was scheduled, or nothing ran."
	}

	blocks := []bloco{
		{"type": "header", "text": bloco{
			"type": "plain_text", "emoji": true,
			"text": ":bar_chart: Brevis — pipelines" + s.environmentSuffix(),
		}},
		{"type": "context", "elements": []bloco{{"type": "mrkdwn", "text": window}}},
		{"type": "section", "text": bloco{"type": "mrkdwn", "text": summary}},
	}

	if worst := r.Worst(5); len(worst) > 0 {
		blocks = append(blocks, bloco{"type": "section", "text": bloco{
			"type": "mrkdwn", "text": "*Failed most:*\n" + list(worst, func(w WorkflowInsight) string {
				return fmt.Sprintf("`%s` — %d of %d", w.Slug, w.Failed, w.Runs)
			}),
		}})
	}

	if slowest := r.Slowest(5); len(slowest) > 0 && slowest[0].Max > 0 {
		blocks = append(blocks, bloco{"type": "section", "text": bloco{
			"type": "mrkdwn", "text": "*Took longest:*\n" + list(slowest, func(w WorkflowInsight) string {
				return fmt.Sprintf("`%s` — max %s, avg %s", w.Slug,
					duration(w.Max), duration(w.Avg))
			}),
		}})
	}

	if moved := volume(r); moved != "" {
		blocks = append(blocks, bloco{"type": "section", "text": bloco{
			"type": "mrkdwn", "text": "*Moved:*\n" + moved,
		}})
	}

	// Where the numbers this message does NOT carry live.
	//
	// CPU, memory and pod restarts are not here and are not zero either: the
	// engine does not collect them, and the honest source is the cluster's own
	// metrics. Saying so is what keeps somebody from reading their absence as
	// "nothing to report".
	blocks = append(blocks, bloco{"type": "context", "elements": []bloco{
		{"type": "mrkdwn", "text": "Infrastructure numbers are not in here — they live in your collector, scraped from `/metrics`."},
	}})

	return map[string]any{
		"text":   fmt.Sprintf("Brevis: %d runs, %d failed", r.Runs, r.Failed),
		"blocks": blocks,
	}
}

// volume renders rows and bytes, and renders NOTHING when neither exists.
//
// Most steps are not SDK steps and report neither. A "Moved: 0 rows" line every
// week is the number that is always zero, and it teaches the reader to skip the
// section that would matter the week it is not zero.
func volume(r Report) string {
	var rows, bytes int64
	for _, w := range r.Workflows {
		rows += w.Rows
		bytes += w.Bytes
	}
	switch {
	case rows == 0 && bytes == 0:
		return ""
	case bytes == 0:
		return fmt.Sprintf("%s rows", thousands(rows))
	case rows == 0:
		return humanBytes(bytes)
	}
	return fmt.Sprintf("%s rows · %s", thousands(rows), humanBytes(bytes))
}

func list[T any](items []T, render func(T) string) string {
	lines := make([]string, 0, len(items))
	for _, it := range items {
		lines = append(lines, "• "+render(it))
	}
	return strings.Join(lines, "\n")
}

func duration(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	case d >= time.Minute:
		return fmt.Sprintf("%.0fm", d.Minutes())
	case d >= time.Second:
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return d.String()
}

func thousands(n int64) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
