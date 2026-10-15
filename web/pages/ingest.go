package pages

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/components"
)

// Ingest is what Catalog was, read as the two mechanisms it mixed: gateway
// STREAMS, which ingest continuously, and SDK LOADS, which a workflow step
// lands on its schedule. Both come from the same catalog read; this file only
// regroups it.

// GatewayGroup is one gateway and its streams.
type GatewayGroup struct {
	Name    string
	Streams []Stream
}

// Stream is a gateway stream, the unit of continuous ingest: where events are
// posted, the one destination they land on, and the dead letter that destination's
// refusals go to. THE DEAD LETTER BELONGS TO THE STREAM -- it is never an item
// of its own, which is what Catalog got wrong.
type Stream struct {
	Gateway, Path string
	Sink          Place
	DeadLetter    *Place
	PublishedAt   time.Time

	// Config is the stream as last published; nil for a version-1 manifest,
	// which described nothing but where it lands.
	Config *postgres.WriterConfig
}

// Place is a destination: its URI and its kind (the scheme).
type Place struct {
	URI, Kind string
}

// Field is a declared configuration value, as text; "" when the stream does
// not declare it, which the screens show as "—".
func (s Stream) Field(path string) string {
	if s.Config == nil {
		return ""
	}
	for _, f := range s.Config.Fields {
		if f.Path == path && f.Value != nil {
			return valueText(f.Value)
		}
	}
	return ""
}

// Mode is how the sink writes, as declared: `sink.write`, or the write of an
// auto_table's tables.
func (s Stream) Mode() string {
	if s.Field("sink.type") == "auto_table" {
		return s.Field("sink.into.write")
	}
	return s.Field("sink.write")
}

// Merge says whether the sink merges, which there is no field for: it is a
// write mode. Nil when the stream does not declare a mode -- "no" would be a
// guess.
func (s Stream) Merge() *bool {
	m := s.Mode()
	if m == "" {
		return nil
	}
	merge := m == "merge"
	return &merge
}

// Href is the stream's page: the gateway, then the stream's path as the tail.
func (s Stream) Href() string {
	return "/ingest/streams/" + url.PathEscape(s.Gateway) + "/" + strings.TrimPrefix(s.Path, "/")
}

// StreamsOf regroups the catalog's gateway writers into gateways and their
// streams, both by name. A stream is keyed by (gateway, path); its sink and
// dead letter are the entries its writer lands on in each role.
func StreamsOf(entries []postgres.CatalogEntry) []GatewayGroup {
	type key struct{ gateway, path string }
	streams := map[key]*Stream{}
	var order []key
	for _, e := range entries {
		for _, w := range e.Writers {
			g := w.Gateway
			if g == nil {
				continue
			}
			k := key{g.Name, g.Stream}
			s := streams[k]
			if s == nil {
				s = &Stream{Gateway: g.Name, Path: g.Stream}
				streams[k] = s
				order = append(order, k)
			}
			if g.PublishedAt.After(s.PublishedAt) {
				s.PublishedAt = g.PublishedAt
			}
			place := Place{URI: e.Target, Kind: e.Kind}
			switch g.Role {
			case "dead_letter":
				s.DeadLetter = &place
				if s.Config == nil {
					s.Config = g.Config
				}
			default:
				s.Sink = place
				if g.Config != nil {
					s.Config = g.Config
				}
			}
		}
	}

	byGateway := map[string]*GatewayGroup{}
	var names []string
	for _, k := range order {
		g := byGateway[k.gateway]
		if g == nil {
			g = &GatewayGroup{Name: k.gateway}
			byGateway[k.gateway] = g
			names = append(names, k.gateway)
		}
		g.Streams = append(g.Streams, *streams[k])
	}
	sort.Strings(names)
	out := make([]GatewayGroup, 0, len(names))
	for _, n := range names {
		g := byGateway[n]
		sort.SliceStable(g.Streams, func(i, j int) bool { return g.Streams[i].Path < g.Streams[j].Path })
		out = append(out, *g)
	}
	return out
}

// LoadGroup is one workflow and what its steps land.
type LoadGroup struct {
	Workflow       string
	Cron, Timezone string // empty for a workflow that only runs by hand
	Targets        []LoadTarget
}

// LoadTarget is one step landing on one destination, judged against that
// step's own schedule.
type LoadTarget struct {
	Workflow, Step string
	Place
	Legacy     bool
	Status     catalog.Status
	LastLoaded time.Time
	Rows       *int64

	// Recent is the rows of the destination's last loads, oldest first --
	// only when this step is its only writer: an entry's history is shared,
	// and another step's loads in this step's sparkline would be a lie.
	Recent []*int64

	// Many is set when the step writes more than one destination, so its
	// link has to say which.
	Many bool
}

// Href is the load's page; the destination goes in the query only when the
// step writes more than one.
func (t LoadTarget) Href() string {
	h := "/ingest/loads/" + url.PathEscape(t.Workflow) + "/" + url.PathEscape(t.Step)
	if t.Many {
		h += "?target=" + url.QueryEscape(t.URI)
	}
	return h
}

// LoadsOf regroups the catalog's step writers by the workflow that runs them,
// each workflow's targets by step and destination. `now` judges freshness.
func LoadsOf(entries []postgres.CatalogEntry, now time.Time) []LoadGroup {
	groups := map[string]*LoadGroup{}
	written := map[[2]string]int{}
	for _, e := range entries {
		steps := 0
		for _, w := range e.Writers {
			if w.Gateway == nil {
				steps++
			}
		}
		for _, w := range e.Writers {
			if w.Gateway != nil {
				continue
			}
			g := groups[w.Workflow]
			if g == nil {
				g = &LoadGroup{Workflow: w.Workflow}
				groups[w.Workflow] = g
			}
			if w.HasSchedule && g.Cron == "" {
				g.Cron, g.Timezone = w.Cron, w.Timezone
			}
			t := LoadTarget{
				Workflow: w.Workflow, Step: w.Node, Place: Place{URI: e.Target, Kind: e.Kind},
				Legacy: e.Legacy, Status: verdictOf(e, w, now).Status,
				LastLoaded: w.LastLoaded, Rows: w.LastRows,
			}
			if steps == 1 {
				t.Recent = e.Recent
			}
			g.Targets = append(g.Targets, t)
			written[[2]string{w.Workflow, w.Node}]++
		}
	}

	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]LoadGroup, 0, len(names))
	for _, n := range names {
		g := groups[n]
		for i := range g.Targets {
			g.Targets[i].Many = written[[2]string{g.Workflow, g.Targets[i].Step}] > 1
		}
		sort.SliceStable(g.Targets, func(i, j int) bool {
			a, b := g.Targets[i], g.Targets[j]
			if a.Step != b.Step {
				return a.Step < b.Step
			}
			return a.URI < b.URI
		})
		out = append(out, *g)
	}
	return out
}

// LoadsView is /ingest/loads as the template draws it: the workflows and their
// loads under the filter, and the counts of every load whatever is shown.
type LoadsView struct {
	Groups []LoadGroupView
	Counts map[catalog.Status]int

	// Total is every load and Workflows every workflow that writes one.
	Total, Workflows int

	Kinds     []string
	HasLegacy bool
	Filter    DataFilter
}

// LoadGroupView is one workflow's group under the filter. Hidden is a group
// the search hides every row of: drawn hidden, so deleting a letter brings it
// back.
type LoadGroupView struct {
	LoadGroup
	Rows   []LoadRow
	Hidden bool
}

// LoadRow is one load under the filter.
type LoadRow struct {
	LoadTarget
	Hidden bool
}

// SearchText is what a search looks in, lower-cased: the workflow, the step
// and the destination. The same text on the server and in data-filter-text.
func (t LoadTarget) SearchText() string {
	return strings.ToLower(t.Workflow + "\n" + t.Step + "\n" + t.URI)
}

// BuildLoads is every load, counted.
func BuildLoads(entries []postgres.CatalogEntry, now time.Time) LoadsView {
	v := LoadsView{Counts: map[catalog.Status]int{}}
	for _, g := range LoadsOf(entries, now) {
		gv := LoadGroupView{LoadGroup: g}
		for _, t := range g.Targets {
			gv.Rows = append(gv.Rows, LoadRow{LoadTarget: t})
			v.Counts[t.Status]++
			v.Total++
			if t.Kind != "" && !containsString(v.Kinds, t.Kind) {
				v.Kinds = append(v.Kinds, t.Kind)
			}
			v.HasLegacy = v.HasLegacy || t.Legacy
		}
		v.Groups = append(v.Groups, gv)
	}
	v.Workflows = len(v.Groups)
	sort.Strings(v.Kinds)
	return v
}

// Apply narrows the loads to the filter: status, kind and legacy drop what
// they exclude (and a group left empty); the search hides. Counts and totals
// still describe every load.
func (v LoadsView) Apply(f DataFilter) LoadsView {
	v.Filter = f
	groups := make([]LoadGroupView, 0, len(v.Groups))
	for _, g := range v.Groups {
		var rows []LoadRow
		visible := 0
		for _, r := range g.Rows {
			if !f.keepsLoad(r.LoadTarget) {
				continue
			}
			r.Hidden = f.Q != "" && !strings.Contains(r.SearchText(), strings.ToLower(f.Q))
			if !r.Hidden {
				visible++
			}
			rows = append(rows, r)
		}
		if len(rows) == 0 {
			continue
		}
		g.Rows, g.Hidden = rows, visible == 0
		groups = append(groups, g)
	}
	v.Groups = groups
	return v
}

// Shown is how many loads are drawn visible.
func (v LoadsView) Shown() int {
	n := 0
	for _, g := range v.Groups {
		for _, r := range g.Rows {
			if !r.Hidden {
				n++
			}
		}
	}
	return n
}

func (f DataFilter) keepsLoad(t LoadTarget) bool {
	switch {
	case f.Status == "attention" && t.Status != catalog.Late && t.Status != catalog.Stale:
		return false
	case f.Status != "" && f.Status != "attention" && string(t.Status) != f.Status:
		return false
	case f.Kind != "" && t.Kind != f.Kind:
		return false
	case f.Legacy && !t.Legacy:
		return false
	}
	return true
}

// loadStatusOptions is the status filter over loads: everything, what needs a
// person, then each status present with its count.
func loadStatusOptions(v LoadsView) []components.SegOption {
	return statusOptions(DataView{Counts: v.Counts, Filter: v.Filter})
}

// loadKindOptions and loadLegacyOptions are the kind and label filters.
func loadKindOptions(v LoadsView) []components.SegOption {
	return kindOptions(DataView{Kinds: v.Kinds, Filter: v.Filter})
}

func loadLegacyOptions(v LoadsView) []components.SegOption {
	return legacyOptions(DataView{Filter: v.Filter})
}

// groupSchedule is a workflow's schedule as the group header says it: in
// words where they are sure, the expression otherwise, "manual" with none.
func groupSchedule(g LoadGroup) string {
	if g.Cron == "" {
		return "manual"
	}
	if words := CronText(g.Cron); words != "" {
		return words + zoneSuffix(g.Timezone)
	}
	return g.Cron
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// LoadView is one load's page: the destination's view (TargetView) narrowed
// to the one step, plus who that step is.
type LoadView struct {
	TargetView
	Workflow, Step string
}

// BuildLoad narrows a destination to the loads and the writer of one step:
// another workflow writing the same destination is a different load, and
// its rows in this step's bars would be a lie.
func BuildLoad(d postgres.TargetDetail, workflow, step string, now time.Time) LoadView {
	var writers []postgres.CatalogWriter
	for _, w := range d.Writers {
		if w.Gateway == nil && w.Workflow == workflow && w.Node == step {
			writers = append(writers, w)
		}
	}
	var loads []postgres.TargetLoad
	for _, l := range d.Loads {
		if l.Workflow == workflow && l.Node == step {
			loads = append(loads, l)
		}
	}
	d.Writers, d.Loads = writers, loads
	if len(d.Writers) > 1 || len(d.Writers) == 0 {
		d.Recent = nil
	}
	return LoadView{TargetView: BuildTarget(d, now), Workflow: workflow, Step: step}
}

// Writer is the step's own writer row, with its verdict.
func (v LoadView) Writer() *WriterView {
	if len(v.Row.Writers) == 0 {
		return nil
	}
	return &v.Row.Writers[0]
}

// loadStats are the four numbers at the top of a load's page. "Recent loads"
// and not "recorded": the page reads the last loadsShown, and above that a
// count would be a floor presented as a total.
func loadStats(v LoadView) []components.Stat {
	bytes := "—"
	if len(v.Loads) > 0 && v.Loads[0].Bytes != nil {
		bytes = bytesText(*v.Loads[0].Bytes)
	}
	recent := components.Stat{Label: "Recent loads", Value: fmt.Sprint(len(v.Loads))}
	if len(v.Loads) >= loadsShown {
		recent.Note = fmt.Sprintf("the last %d", loadsShown)
	}
	return []components.Stat{
		{Label: "Last load", Value: components.When(v.Row.LastWhen), Note: components.UTCStamp(v.Row.LastWhen)},
		{Label: "Rows, last load", Value: rowsText(v.Row.LastRows)},
		{Label: "Bytes, last load", Value: bytes},
		recent,
	}
}

// loadsShown is how many loads the destination read returns (its LIMIT).
const loadsShown = 30

// writerSchedule is a step writer's schedule as a sentence.
func writerSchedule(w *WriterView) string {
	if w == nil || !w.HasSchedule {
		return "runs by hand"
	}
	if words := CronText(w.Cron); words != "" {
		return words + zoneSuffix(w.Timezone)
	}
	return w.Cron + " " + w.Timezone
}

// LoadChoice is one destination a step writes, on the page that asks which.
type LoadChoice struct {
	URI, Href string
}

// loadKind is the destination's kind as the eyebrow says it.
func loadKind(v LoadView) string {
	if v.Row.Kind == "" {
		return "label"
	}
	return v.Row.Kind
}

// AttachStreamConfigs gives each gateway writer its stream's configuration,
// from the one read of every stream's (the catalog read leaves them out).
func AttachStreamConfigs(entries []postgres.CatalogEntry, configs map[postgres.StreamKey]*postgres.WriterConfig) {
	for _, e := range entries {
		for _, w := range e.Writers {
			if g := w.Gateway; g != nil && g.Config == nil {
				g.Config = configs[postgres.StreamKey{Gateway: g.Name, Path: g.Stream}]
			}
		}
	}
}

// StreamFilter narrows /ingest/streams: a search over the path, the sink, the
// dead letter and the gateway, and the sink's kind. In the URL, like every
// list's.
type StreamFilter struct {
	Q, Kind string
}

// ParseStreamFilter reads the filter from a query string; a kind it does not
// know is ignored rather than turned into an empty page.
func ParseStreamFilter(q url.Values) StreamFilter {
	f := StreamFilter{Q: strings.TrimSpace(q.Get("q"))}
	if r := []rune(f.Q); len(r) > searchCeiling {
		f.Q = string(r[:searchCeiling])
	}
	if k := q.Get("kind"); filterKinds[k] {
		f.Kind = k
	}
	return f
}

// With is the link to this filter with one field changed, the fields always
// in the same order.
func (f StreamFilter) With(field, value string) string {
	switch field {
	case "kind":
		f.Kind = value
	case "q":
		f.Q = value
	}
	var parts []string
	if f.Kind != "" {
		parts = append(parts, "kind="+url.QueryEscape(f.Kind))
	}
	if f.Q != "" {
		parts = append(parts, "q="+url.QueryEscape(f.Q))
	}
	if len(parts) == 0 {
		return "/ingest/streams"
	}
	return "/ingest/streams?" + strings.Join(parts, "&")
}

// StreamsView is /ingest/streams as drawn: the gateways and their streams
// under the filter, and the counts of every stream whatever is shown.
type StreamsView struct {
	Groups                          []StreamGroupView
	Total, Gateways, WithDeadLetter int
	Kinds                           []string
	Filter                          StreamFilter
}

// StreamGroupView is one gateway under the filter; Count is all its streams.
type StreamGroupView struct {
	Name   string
	Cards  []StreamCard
	Count  int
	Hidden bool
}

// StreamCard is one stream under the filter.
type StreamCard struct {
	Stream
	Hidden bool
}

// SearchText is what a search looks in, lower-cased: the gateway, the path,
// where it lands and where its refusals go.
func (s Stream) SearchText() string {
	parts := []string{s.Gateway, s.Path, s.Sink.URI}
	if s.DeadLetter != nil {
		parts = append(parts, s.DeadLetter.URI)
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

// Key is the stream's identity as one string, for the page's markup.
func (s Stream) Key() string { return s.Gateway + " " + s.Path }

// BuildStreams is every stream, counted.
func BuildStreams(entries []postgres.CatalogEntry) StreamsView {
	v := StreamsView{}
	for _, g := range StreamsOf(entries) {
		gv := StreamGroupView{Name: g.Name, Count: len(g.Streams)}
		for _, s := range g.Streams {
			gv.Cards = append(gv.Cards, StreamCard{Stream: s})
			v.Total++
			if s.DeadLetter != nil {
				v.WithDeadLetter++
			}
			if s.Sink.Kind != "" && !containsString(v.Kinds, s.Sink.Kind) {
				v.Kinds = append(v.Kinds, s.Sink.Kind)
			}
		}
		v.Groups = append(v.Groups, gv)
	}
	v.Gateways = len(v.Groups)
	sort.Strings(v.Kinds)
	return v
}

// Apply narrows the streams to the filter: the kind drops, the search hides.
func (v StreamsView) Apply(f StreamFilter) StreamsView {
	v.Filter = f
	groups := make([]StreamGroupView, 0, len(v.Groups))
	for _, g := range v.Groups {
		var cards []StreamCard
		visible := 0
		for _, c := range g.Cards {
			if f.Kind != "" && c.Sink.Kind != f.Kind {
				continue
			}
			c.Hidden = f.Q != "" && !strings.Contains(c.SearchText(), strings.ToLower(f.Q))
			if !c.Hidden {
				visible++
			}
			cards = append(cards, c)
		}
		if len(cards) == 0 {
			continue
		}
		g.Cards, g.Hidden = cards, visible == 0
		groups = append(groups, g)
	}
	v.Groups = groups
	return v
}

// Shown is how many streams are drawn visible.
func (v StreamsView) Shown() int {
	n := 0
	for _, g := range v.Groups {
		for _, c := range g.Cards {
			if !c.Hidden {
				n++
			}
		}
	}
	return n
}

// streamKindOptions is the destination-kind filter.
func streamKindOptions(v StreamsView) []components.SegOption {
	f := v.Filter
	out := []components.SegOption{{Label: "Any destination", Href: f.With("kind", ""), Current: f.Kind == ""}}
	for _, k := range v.Kinds {
		out = append(out, components.SegOption{Label: k, Href: f.With("kind", k), Current: f.Kind == k})
	}
	return out
}

// streamFlow is the stream drawn as its compact flow: the configuration's,
// when it described itself (#108), else where events are posted and where
// they land. Either way the dead letter is the one the catalog knows, hanging
// off the destination.
func streamFlow(s Stream) *Flow {
	f := FlowOf(s.Config, s.Sink.URI)
	if f == nil {
		f = &Flow{Nodes: []FlowNode{
			{Kind: "source", Title: "Source", Main: "POST " + s.Path},
			{Kind: "destination", Title: "Destination", Main: s.Sink.URI, Detail: s.Sink.Kind},
		}}
	}
	if s.DeadLetter != nil {
		f.DeadLetter = &FlowNode{Kind: "dead_letter", Title: "Dead letter", Main: s.DeadLetter.URI, Detail: "refused batches"}
	}
	return f
}

// StreamView is one stream's page.
type StreamView struct {
	Stream

	// Configured is the stream's configuration card -- settings by section,
	// the file's notes and the history of changes -- read from the sink's
	// detail; nil when the stream described nothing.
	Configured *WriterConfigView

	// SQL says a SQL service answers for the sink, so "Query destination"
	// leads somewhere.
	SQL bool
}

// BuildStream lays out a stream's page. detail is its sink's, which carries
// the configuration's history; without it the page still draws what the
// stream declares.
func BuildStream(s Stream, detail *postgres.TargetDetail) StreamView {
	v := StreamView{Stream: s}
	cfg := s.Config
	if detail != nil {
		for _, w := range detail.Writers {
			if g := w.Gateway; g != nil && g.Name == s.Gateway && g.Stream == s.Path && g.Config != nil {
				cfg = g.Config
				v.PublishedAt = g.PublishedAt
			}
		}
	}
	if cfg != nil {
		v.Config = cfg
		v.Configured = &WriterConfigView{Gateway: s.Gateway, Stream: s.Path, Role: "sink",
			PublishedAt: v.PublishedAt, Described: true, Config: BuildConfig(cfg)}
	}
	return v
}

// Queryable says the sink is something SQL can read: not a topic, not files.
func (v StreamView) Queryable() bool {
	switch v.Sink.Kind {
	case "", "pubsub", "file":
		return false
	}
	return true
}

// QueryHref opens the workbench on the sink.
func (v StreamView) QueryHref() string { return "/sql?target=" + url.QueryEscape(v.Sink.URI) }

// Summary is one paragraph on what the stream does, from what it declares.
func (v StreamView) Summary() string {
	if v.Config != nil {
		return Summary(v.Config)
	}
	return "Events posted to " + v.Path + " land in " + v.Sink.URI + ". The stream was published without its configuration, so this is all it says."
}

// Declared is one setting in the Configuration tab: what it is and its
// value, "" when the stream does not declare it (drawn "—").
type Declared struct {
	Key, Label, Value string
}

// DeclaredGroup is one block of the Configuration tab.
type DeclaredGroup struct {
	Title string
	Rows  []Declared
}

// DeclaredGroups is everything a stream may declare, in one place, each
// setting it does not declare left empty rather than guessed.
func (v StreamView) DeclaredGroups() []DeclaredGroup {
	f := func(key, label string) Declared { return Declared{Key: key, Label: label, Value: v.Field(key)} }
	merge := Declared{Key: "merge", Label: "merge"}
	if m := v.Merge(); m != nil {
		merge.Value = "no"
		if *m {
			merge.Value = "yes"
		}
	}
	dl := Declared{Key: "dead_letter", Label: "where"}
	if v.DeadLetter != nil {
		dl.Value = v.DeadLetter.URI
	}
	return []DeclaredGroup{
		{"Stream", []Declared{f("name", "name"), {Key: "path", Label: "path", Value: v.Path}, f("format", "format"), f("hook", "hook")}},
		{"Buffer", []Declared{f("buffer.flush.every", "flush every"), f("buffer.flush.records", "flush at records"), f("buffer.durability", "durability")}},
		{"Destination", []Declared{{Key: "sink", Label: "where", Value: v.Sink.URI}, f("sink.type", "type"),
			{Key: "mode", Label: "write", Value: v.Mode()}, merge}},
		{"Identity", []Declared{f("identity.provider", "provider"), f("identity.entity", "entity"),
			f("identity.source_key", "source key"), f("identity.record_ts", "record time")}},
		{"Dead letter", []Declared{dl, f("dead_letter.type", "type")}},
	}
}

// Properties are the Overview's side list.
func (v StreamView) Properties() []components.Def {
	or := func(s string) string {
		if s == "" {
			return "—"
		}
		return s
	}
	merge := "—"
	if m := v.Merge(); m != nil {
		merge = "no"
		if *m {
			merge = "yes"
		}
	}
	published := "—"
	if !v.PublishedAt.IsZero() {
		published = components.UTCStamp(&v.PublishedAt)
	}
	return []components.Def{
		{Term: "Gateway", Value: v.Gateway, Mono: true},
		{Term: "Stream", Value: or(v.Field("name")), Mono: true},
		{Term: "Destination", Value: v.Sink.URI, Mono: true},
		{Term: "Write", Value: or(v.Mode()), Mono: true},
		{Term: "Merge", Value: merge, Mono: true},
		{Term: "Published", Value: published, Mono: true},
	}
}

// DeadLetterPanel is the commands and the resend note, when the stream's
// configuration says what its dead letter is.
func (v StreamView) DeadLetterPanel() *DeadLetterView {
	if v.Configured == nil {
		return nil
	}
	return v.Configured.Config.DeadLetter
}

// BuriedMetric is the gateway's counter of what this stream buried, labelled
// by the stream's name as the gateway labels it.
func (v StreamView) BuriedMetric() string {
	if n := v.Field("name"); n != "" {
		return `brevis_gateway_dead_letter_records_total{stream="` + n + `"}`
	}
	return "brevis_gateway_dead_letter_records_total"
}

// FindStream is the stream with this gateway and path, or nil.
func FindStream(groups []GatewayGroup, gateway, path string) *Stream {
	for _, g := range groups {
		if g.Name != gateway {
			continue
		}
		for i := range g.Streams {
			if g.Streams[i].Path == path {
				return &g.Streams[i]
			}
		}
	}
	return nil
}
