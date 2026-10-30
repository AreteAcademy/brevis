package pages

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// CatalogScreen is /data (screen-catalog): every destination the ecosystem
// wrote, worst first, with what the filter keeps.
type CatalogScreen struct {
	// Rows are the destinations the filter keeps, worst first.
	Rows   []DataRow
	Filter DataFilter
	// Kinds are the kinds present; HasLegacy whether a legacy row exists.
	Kinds     []string
	HasLegacy bool
	// Total is every destination -- the legacy ones only when asked for --
	// and Shown those the filter keeps.
	Total, Shown int
	// counts is how many each status would show with the rest of the filter
	// as it is; "" is all, "attention" stale and late together.
	counts map[string]int
	Now    time.Time
}

// Count is how many destinations a status choice would show.
func (v CatalogScreen) Count(status string) int { return v.counts[status] }

// BuildCatalog judges every destination (BuildData) and keeps what the filter
// asks for. A legacy row -- a label, not a URI -- is shown only when asked
// for: it is history, and a list that opens on it buries what is current.
// The band counts each status with the kind, the search and the legacy
// switch as they are, so a count cannot disagree with the list its link opens.
func BuildCatalog(entries []postgres.CatalogEntry, f DataFilter, now time.Time) CatalogScreen {
	d := BuildData(entries, now)
	v := CatalogScreen{Filter: f, Kinds: d.Kinds, HasLegacy: d.HasLegacy, counts: map[string]int{}, Now: now}
	for _, r := range d.Rows {
		if r.Legacy && !f.Legacy {
			continue
		}
		v.Total++
		if !f.keepsDestination(r) {
			continue
		}
		v.counts[""]++
		v.counts[string(r.Status)]++
		if r.Status == catalog.Late || r.Status == catalog.Stale {
			v.counts["attention"]++
		}
		if f.keepsStatus(r.Status) {
			v.Rows = append(v.Rows, r)
		}
	}
	v.Shown = len(v.Rows)
	return v
}

// keepsDestination is everything the catalog filters by but the status.
func (f DataFilter) keepsDestination(r DataRow) bool {
	switch {
	case r.Legacy && !f.Legacy:
		return false
	case f.Kind != "" && r.Kind != f.Kind:
		return false
	case f.Q != "" && !strings.Contains(destinationText(r), strings.ToLower(f.Q)):
		return false
	}
	return true
}

func (f DataFilter) keepsStatus(s catalog.Status) bool {
	switch f.Status {
	case "":
		return true
	case "attention":
		return s == catalog.Late || s == catalog.Stale
	}
	return string(s) == f.Status
}

// destinationText is what the catalog's search looks in: the URI -- its
// table and dataset -- and who writes it, the workflow and step or the
// gateway and its path.
func destinationText(r DataRow) string {
	parts := []string{r.Target}
	for _, w := range r.Writers {
		parts = append(parts, w.Workflow, w.Node)
		if g := w.Gateway; g != nil {
			parts = append(parts, g.Name, g.Stream)
		}
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

// WriterLine is who writes a destination, the first writer alone: the
// palette's hint.
func WriterLine(r DataRow) string {
	w, _ := writerLine(r)
	return w
}

// writerLine is who writes a destination, as a row says it: the first
// writer, and how many more there are.
func writerLine(r DataRow) (string, int) {
	if len(r.Writers) == 0 {
		return "", 0
	}
	return writerName(r.Writers[0].CatalogWriter), len(r.Writers) - 1
}

// writerName is a writer as a line of text: `workflow · step`, or `gateway
// name · path`.
func writerName(w postgres.CatalogWriter) string {
	if g := w.Gateway; g != nil {
		return "gateway " + g.Name + " · " + g.Stream
	}
	return w.Workflow + " · " + w.Node
}

// runQueued is whether a writer of the destination has a run queued or going,
// which the row says under its status: the fix may already be on its way.
func runQueued(r DataRow) bool {
	for _, w := range r.Writers {
		if w.RunInFlight != nil {
			return true
		}
	}
	return false
}

// autoTable says the destination is a gateway's table-per-event-type: its
// URI ends in `*`.
func autoTable(target string) bool { return strings.HasSuffix(target, "/*") }

// targetHref is a destination's drawer on the catalog.
func targetHref(target string) string { return "/data/target?u=" + url.QueryEscape(target) }

// catalogStatuses are the band's choices after All and Needs attention, in
// the order of their gravity.
var catalogStatuses = []catalog.Status{
	catalog.Stale, catalog.Late, catalog.OnTime, catalog.Continuous,
	catalog.Paused, catalog.Unscheduled, catalog.Unidentified,
}

// statusTip is the rule behind a status, as the band's tooltip says it.
func statusTip(ctx context.Context, s string) string {
	switch s {
	case "attention":
		return i18n.T(ctx, "catalog.tip.attention")
	case "stale":
		return i18n.T(ctx, "catalog.tip.stale")
	case "late":
		return i18n.T(ctx, "catalog.tip.late")
	case "on_time":
		return i18n.T(ctx, "catalog.tip.on_time")
	case "continuous":
		return i18n.T(ctx, "catalog.tip.continuous")
	case "paused":
		return i18n.T(ctx, "catalog.tip.paused")
	case "unscheduled":
		return i18n.T(ctx, "catalog.tip.unscheduled")
	case "unidentified":
		return i18n.T(ctx, "catalog.tip.unidentified")
	}
	return ""
}

// kindLogo is the logo a destination's kind is drawn with.
func kindLogo(kind string) string {
	switch kind {
	case "gs":
		return "gcs"
	case "":
		return "file"
	}
	return kind
}

// freshnessWord is the state word's key for a catalog status: "unscheduled"
// is said as "no schedule".
func freshnessWord(s string) string {
	if s == "unscheduled" {
		return "no_schedule"
	}
	return s
}

// legacyFlip is the legacy switch's other position.
func legacyFlip(on bool) string {
	if on {
		return ""
	}
	return "1"
}

// peakOf is the largest of a destination's recent counts, for scaling bars.
func peakOf(counts []*int64) int64 {
	var peak int64
	for _, c := range counts {
		if c != nil && *c > peak {
			peak = *c
		}
	}
	return peak
}

// barHeight is a count's bar as a share of the tallest, never under 12% so a
// small load still shows.
func barHeight(n, peak int64) int {
	if peak <= 0 {
		return 12
	}
	return max(12, int(n*100/peak))
}

// splitDestination cuts a destination for its row: everything up to the
// dataset, faint, and the table, which is what a reader looks for --
// `bigquery://acme/bronze/` and `orders`. A prefix (ending in `/`) is all
// where; a legacy label splits at its last dot.
func splitDestination(t string) (where, what string) {
	if !strings.Contains(t, "://") {
		return SplitTarget(t)
	}
	if strings.HasSuffix(t, "/") {
		return t, ""
	}
	i := strings.LastIndex(t, "/")
	return t[:i+1], t[i+1:]
}
