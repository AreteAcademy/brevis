package pages

import (
	"context"
	"fmt"
	"github.com/AreteAcademy/brevis/web/i18n"
	"github.com/a-h/templ"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/endpointcall"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// EndpointsView is Data → Endpoints (screen-endpoints): every published
// brevis-sql-api path the filter keeps, worst first by the data it reads,
// and the counts of every endpoint whatever is shown.
type EndpointsView struct {
	Rows []EndpointRow

	// Total, APIs, Fresh and Stale count every endpoint; Caches and
	// Connections are what the APIs published -- "redis", "warehouse
	// (bigquery)" -- for the summary.
	Total, APIs, Fresh, Stale int
	Caches, Connections       []string
	// APINames are the APIs present, sorted: the API filter's choices.
	APINames []string

	Filter EndpointFilter
	Now    time.Time
}

// EndpointFilter narrows the list: Q searches the path, the API, the tables
// read and the parameters.
type EndpointFilter struct {
	// Q searches the path, the API, the parameters and the tables read; API
	// is one API's name; State is "fresh" or "stale". "" is all.
	Q, API, State string
}

// ParseEndpointFilter reads the filter from a query string; a state it does
// not know is ignored rather than turned into an empty page.
func ParseEndpointFilter(q url.Values) EndpointFilter {
	f := EndpointFilter{Q: strings.TrimSpace(q.Get("q")), API: strings.TrimSpace(q.Get("api"))}
	if r := []rune(f.Q); len(r) > searchCeiling {
		f.Q = string(r[:searchCeiling])
	}
	if s := q.Get("state"); s == "fresh" || s == "stale" {
		f.State = s
	}
	return f
}

// With is the link to this filter with one field changed, the fields always
// in the same order.
func (f EndpointFilter) With(field, value string) string {
	switch field {
	case "api":
		f.API = value
	case "state":
		f.State = value
	case "q":
		f.Q = value
	}
	var parts []string
	if f.API != "" {
		parts = append(parts, "api="+url.QueryEscape(f.API))
	}
	if f.State != "" {
		parts = append(parts, "state="+url.QueryEscape(f.State))
	}
	if f.Q != "" {
		parts = append(parts, "q="+url.QueryEscape(f.Q))
	}
	if len(parts) == 0 {
		return "/data/endpoints"
	}
	return "/data/endpoints?" + strings.Join(parts, "&")
}

// EndpointRow is one published endpoint.
type EndpointRow struct {
	API, Name, Path string
	// Keyed is true when each request reads as its key's tenant.
	Keyed       bool
	Params      []string
	Reads       []string
	PublishedAt time.Time
	// Required names the parameters a request must carry.
	Required map[string]bool

	// About is the first line of the note above the endpoint in its file.
	About string

	// Freshness is the worst of the tables it reads, by the catalog: "stale"
	// (late or stale), "fresh", or "" when the catalog knows none of them.
	Freshness string

	// Read is the catalog's row of the first table it reads, when the
	// catalog knows it: when it was rebuilt, and by whom.
	Read *DataRow

	// TTL is how long an answer is cached, from the API's manifest; "" with
	// no cache on record.
	TTL string
}

// SearchText is what a search looks in, lower-cased: the path, the API, the
// parameters and the tables it reads.
func (r EndpointRow) SearchText() string {
	parts := append([]string{r.Path, r.API, r.Name}, r.Params...)
	parts = append(parts, r.Reads...)
	return strings.ToLower(strings.Join(parts, "\n"))
}

// Href is the endpoint's own page.
func (r EndpointRow) Href() string {
	return "/data/endpoints/" + url.PathEscape(r.API) + "/" + url.PathEscape(r.Name)
}

// TargetHref is a read target's page: /data/target, which forwards it to the
// stream or the load that owns it now that Catalog is Streams and Loads.
func TargetHref(target string) string { return "/data/target?u=" + url.QueryEscape(target) }

func rowOf(e postgres.PublishedEndpoint) EndpointRow {
	r := EndpointRow{API: e.API, Name: e.Name, Path: e.Path, Keyed: e.Tenant == "key",
		Reads: e.Reads, PublishedAt: e.PublishedAt, About: NoteHead(e.Endpoint.Notes[""])}
	if c := e.Manifest.Cache; c != nil {
		r.TTL = c.TTL
	}
	for _, p := range e.Endpoint.Params {
		r.Params = append(r.Params, p.Name)
		if p.Required {
			if r.Required == nil {
				r.Required = map[string]bool{}
			}
			r.Required[p.Name] = true
		}
	}
	return r
}

// freshnessRank orders the list: stale first, then fresh, then unknown.
var freshnessRank = map[string]int{"stale": 0, "fresh": 1, "": 2}

// BuildEndpoints is every endpoint under the filter (screen-endpoints T2),
// judged by the catalog's view of the tables it reads -- the worst of them
// -- worst first, then by API and path. The search drops what it does not
// match, on the server, so a searched list is a link.
func BuildEndpoints(all []postgres.PublishedEndpoint, entries []postgres.CatalogEntry, f EndpointFilter, now time.Time) EndpointsView {
	v := EndpointsView{Filter: f, Now: now}
	byTarget := map[string]DataRow{}
	for _, row := range BuildData(entries, now).Rows {
		byTarget[row.Target] = row
	}
	apis := map[string]bool{}
	q := strings.ToLower(strings.TrimSpace(f.Q))
	if r := []rune(q); len(r) > searchCeiling {
		q = string(r[:searchCeiling])
	}
	for _, e := range all {
		r := rowOf(e)
		for i, target := range e.Reads {
			row, known := byTarget[target]
			if !known {
				continue
			}
			if i == 0 {
				read := row
				r.Read = &read
			}
			switch {
			case row.Status == catalog.Late || row.Status == catalog.Stale:
				r.Freshness = "stale"
			case r.Freshness == "":
				r.Freshness = "fresh"
			}
		}
		v.Total++
		apis[e.API] = true
		switch r.Freshness {
		case "stale":
			v.Stale++
		case "fresh":
			v.Fresh++
		}
		if c := e.Manifest.Cache; c != nil && !containsString(v.Caches, c.Type) {
			v.Caches = append(v.Caches, c.Type)
		}
		if m := e.Manifest; m.Connection != "" {
			if conn := m.Connection + " (" + m.Dialect + ")"; !containsString(v.Connections, conn) {
				v.Connections = append(v.Connections, conn)
			}
		}
		switch {
		case f.API != "" && e.API != f.API,
			f.State != "" && r.Freshness != f.State,
			q != "" && !strings.Contains(r.SearchText(), q):
			continue
		}
		v.Rows = append(v.Rows, r)
	}
	v.APIs = len(apis)
	for api := range apis {
		v.APINames = append(v.APINames, api)
	}
	sort.Strings(v.APINames)
	sort.SliceStable(v.Rows, func(i, j int) bool {
		a, b := v.Rows[i], v.Rows[j]
		if freshnessRank[a.Freshness] != freshnessRank[b.Freshness] {
			return freshnessRank[a.Freshness] < freshnessRank[b.Freshness]
		}
		if a.API != b.API {
			return a.API < b.API
		}
		return a.Path < b.Path
	})
	return v
}

// EndpointView is one endpoint's page: its contract, as published.
type EndpointView struct {
	Row    EndpointRow
	Params []ParamView
	Limits catalog.EndpointLimits
	// About is the note above the endpoint in its file, if its author marked
	// one.
	About string

	// CanRun says this console may run the endpoint: it asks who you are,
	// and it has an address for the API. Without both there is no Try it.
	CanRun bool
	// Form is Try it's fields; Answer what the last Run brought back, or
	// RunErr why there is none.
	Form   []FormField
	Answer *endpointcall.Answer
	RunErr string
}

// ParamView is one parameter as a caller reads it.
type ParamView struct {
	Name, Type, Accepts, Default, Note string
	Required                           bool
}

// BuildEndpoint is the page of one endpoint.
func BuildEndpoint(e postgres.PublishedEndpoint) EndpointView {
	v := EndpointView{Row: rowOf(e), Limits: e.Endpoint.Limits, About: e.Endpoint.Notes[""]}
	for _, p := range e.Endpoint.Params {
		pv := ParamView{Name: p.Name, Type: p.Type, Required: p.Required, Note: e.Endpoint.Notes["params."+p.Name]}
		if p.Default != nil {
			pv.Default = *p.Default
		}
		switch {
		case len(p.Values) > 0:
			pv.Accepts = strings.Join(p.Values, ", ")
		case p.Type == "string" && p.MaxLen > 0:
			pv.Accepts = fmt.Sprintf("text, at most %d characters", p.MaxLen)
		case p.Type == "int" && (p.Min != nil || p.Max != nil):
			pv.Accepts = rangeText(p.Min, p.Max)
		case p.Type == "date":
			pv.Accepts = "YYYY-MM-DD"
		case p.Type == "bool":
			pv.Accepts = "true or false"
		case p.Type == "period":
			pv.Accepts = strings.Join(periods, ", ")
		}
		v.Params = append(v.Params, pv)
	}
	return v
}

func rangeText(min, max *int64) string {
	switch {
	case min != nil && max != nil:
		return fmt.Sprintf("a whole number from %d to %d", *min, *max)
	case min != nil:
		return fmt.Sprintf("a whole number, at least %d", *min)
	default:
		return fmt.Sprintf("a whole number, at most %d", *max)
	}
}

// BytesBilled is the endpoint's byte ceiling in words.
func (v EndpointView) BytesBilled() string {
	if v.Limits.MaxBytesBilled <= 0 {
		return "no ceiling"
	}
	return bytesText(v.Limits.MaxBytesBilled)
}

// Curl is how to call it, with the key as a placeholder: the console never
// holds one, and a page that printed a real key would be a page to guard.
func (v EndpointView) Curl() string {
	q := []string{}
	for _, f := range v.Form {
		if f.Value != "" {
			q = append(q, url.QueryEscape(f.Name)+"="+url.QueryEscape(f.Value))
		}
	}
	u := v.CurlBase()
	if len(q) > 0 {
		u += "?" + strings.Join(q, "&")
	}
	return CurlHead + "'" + u + "'"
}

// CurlHead is the curl's fixed start. The key is a placeholder, always: the
// console's own is never written into a page.
const CurlHead = "curl -H 'Authorization: Bearer <key>' "

// CurlBase is the address the curl calls, before its query string. ui.js
// rebuilds the rest from the form as somebody types, with the same escaping
// url.QueryEscape uses here, so a quote typed into a field cannot end the
// shell's quoted string.
func (v EndpointView) CurlBase() string {
	return "<api address>" + v.Row.Path
}

// EndpointRef is an endpoint on a model's page, as one of its readers.
type EndpointRef struct {
	API, Name, Path, Href string
}

// ReadersOf are the endpoints over a target, for its page.
func ReadersOf(all []postgres.PublishedEndpoint) []EndpointRef {
	var out []EndpointRef
	for _, e := range all {
		r := rowOf(e)
		out = append(out, EndpointRef{API: r.API, Name: r.Name, Path: r.Path, Href: r.Href()})
	}
	return out
}

// FormField is one input of Try it, typed as its parameter is.
type FormField struct {
	Name, Kind, Value string
	// Options are a select's: an enum's values, a bool's, a period's. Empty
	// for an input.
	Options  []string
	Required bool
	MaxLen   int
}

// periods are what a period parameter accepts, in brevis-sql-api's order.
//
// A LIST THIS CANNOT IMPORT, SO IT SAYS WHERE IT CAME FROM:
// `sql/internal/api/period.go`, `periods` -- the engine imports nothing of
// the sql module. If that one moves, this one moves.
var periods = []string{"today", "yesterday", "last_7_days", "last_30_days", "last_week", "last_month", "month_to_date"}

// TryForm is the endpoint's parameters as a form, holding what was last sent
// -- or, before anything was, each parameter's default.
func TryForm(params []catalog.EndpointParam, sent url.Values) []FormField {
	var out []FormField
	for _, p := range params {
		f := FormField{Name: p.Name, Required: p.Required, MaxLen: p.MaxLen}
		switch p.Type {
		case "enum":
			f.Kind, f.Options = "select", p.Values
		case "bool":
			f.Kind, f.Options = "select", []string{"true", "false"}
		case "period":
			f.Kind, f.Options = "select", periods
		case "date":
			f.Kind = "date"
		case "int":
			f.Kind = "number"
		default:
			f.Kind = "text"
		}
		if v, there := sent[p.Name]; there {
			f.Value = v[0]
		} else if p.Default != nil && sent == nil {
			f.Value = *p.Default
		}
		out = append(out, f)
	}
	return out
}

// TryValues are the declared parameters a submitted form carries, the empty
// ones left out: an empty select is "use the default", which is the API's
// to apply, and an undeclared name is not forwarded at all.
func TryValues(params []catalog.EndpointParam, q url.Values) url.Values {
	out := url.Values{}
	for _, p := range params {
		if v := strings.TrimSpace(q.Get(p.Name)); v != "" {
			out.Set(p.Name, v)
		}
	}
	return out
}

// paramsCount is the head of the parameters card: how many, and how many a
// request must carry.
func paramsCount(ps []ParamView) string {
	req := 0
	for _, p := range ps {
		if p.Required {
			req++
		}
	}
	if req == 0 {
		return fmt.Sprint(len(ps))
	}
	return fmt.Sprintf("%d · %d required", len(ps), req)
}

// thousands writes a count with its thousands separated -- 10,000, not 10000,
// which somebody has to count the zeros of.
func thousands(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// fieldClass is a Try it field: the shell's input, the height of a button.
const fieldClass = "h-8 w-full min-w-0 rounded-card border border-line bg-surface px-2.5 text-[0.82rem] text-ink focus:border-accent-strong focus:outline-none"

// curlAttrs tie the curl to Try it's form, when there is one to follow.
func curlAttrs(v EndpointView) templ.Attributes {
	if !v.CanRun {
		return nil
	}
	return templ.Attributes{"data-curl-from": "try-it", "data-curl-base": v.CurlBase(), "data-curl-head": CurlHead}
}

// paramsSaid is an endpoint's parameters as a row says them: "8 · 2
// required", or how many when none is required.
func paramsSaid(ctx context.Context, r EndpointRow) string {
	if len(r.Params) == 0 {
		return i18n.T(ctx, "endpoints.params.none")
	}
	if n := len(r.Required); n > 0 {
		return i18n.T(ctx, "endpoints.params.required", "n", fmt.Sprint(len(r.Params)), "required", fmt.Sprint(n))
	}
	return fmt.Sprint(len(r.Params))
}

// rebuiltSaid is when the table a row reads was rebuilt, and by whom.
func rebuiltSaid(ctx context.Context, now time.Time, d *DataRow) string {
	if d == nil || d.LastWhen == nil {
		return ""
	}
	return i18n.T(ctx, "endpoints.rebuilt", "when", i18n.Since(ctx, now, *d.LastWhen), "by", WriterLine(*d))
}

// kindOf is the kind of a table an endpoint reads: its URI's scheme.
func kindOf(target string) string {
	scheme, _, ok := strings.Cut(target, "://")
	if !ok {
		return ""
	}
	return scheme
}

// publishEndpointYAML and publishEndpointCommands are Publish endpoint's
// example: an API's file with one endpoint, and how it reaches the console.
// The cache names the variable its address is read from, never the address.
const publishEndpointYAML = `name: metrics-api
connection: warehouse

cache:
  type: redis
  addr_from: BREVIS_CACHE_ADDR
  ttl: 6h

endpoints:
  #: Clicks per day, for the customer's dashboard.
  - name: clicks
    path: /v1/clicks
    tenant: key
    params:
      start_date: {type: date, required: true}
      end_date: {type: date, required: true}
    sql_file: endpoints/clicks.sql`

const publishEndpointCommands = `brevis-sql-api describe --config api.yaml --out manifest.json
brevis endpoints publish manifest.json`
