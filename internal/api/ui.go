package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/alerts"
	"github.com/AreteAcademy/brevis/internal/auth"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	sch "github.com/AreteAcademy/brevis/internal/domain/schedule"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/infrastructure/sqlserve"
	"github.com/AreteAcademy/brevis/web/assets"
	"github.com/AreteAcademy/brevis/web/components"
	"github.com/AreteAcademy/brevis/web/layouts"
	"github.com/AreteAcademy/brevis/web/pages"
)

// overviewWindow is the dashboard's horizon. Twenty-four hours cover a full
// daily cycle -- most schedules are daily, and a shorter window would show only
// part of the day and make the success rate swing because of the cut, not
// because anything changed.
const overviewWindow = 24 * time.Hour

// workflowWindow is the horizon of ONE workflow's numbers, and it is thirty
// days rather than the dashboard's twenty-four hours.
//
// The two screens answer different questions. The dashboard is "is the
// installation healthy right now", and a day is the right window for that. A
// workflow's page is "is this pipeline reliable", and a daily job has one run
// in twenty-four hours -- a success rate over a single sample is not a rate.
const workflowWindow = 30 * 24 * time.Hour

// Leitura is what the UI needs from the database. The interface is declared here, in the consumer.
type Leitura interface {
	Indicators(ctx context.Context, window time.Duration) (postgres.Indicators, error)
	IndicatorsFor(ctx context.Context, window time.Duration, workflow string) (postgres.Indicators, error)
	RunsPerDay(ctx context.Context, workflow string, days int) ([]postgres.Day, error)
	LoadTrend(ctx context.Context, workflow string, days int) ([]postgres.LoadDay, error)
	RunsPerHour(ctx context.Context, horas int) ([]postgres.Bucket, error)
	InFlight(ctx context.Context, limite int) ([]postgres.RunSummary, error)
	LatestRuns(ctx context.Context, limite int) ([]postgres.RunSummary, error)
	Runs(ctx context.Context, f postgres.RunFilter) ([]postgres.RunSummary, error)
	CountRuns(ctx context.Context, f postgres.RunFilter) (int, error)
	WorkflowRuns(ctx context.Context, slug string, limite int) ([]postgres.RunSummary, error)
	Workflows(ctx context.Context) ([]postgres.WorkflowSummary, error)
	Schedules(ctx context.Context) ([]postgres.ScheduleSummary, error)
	Projects(ctx context.Context) ([]postgres.ProjectSummary, error)
	QueueDepth(ctx context.Context) (int, int, error)
}

// Definitions reads a workflow's published definition. Kept apart from `Leitura`
// because it returns the domain, not a screen projection.
type Definitions interface {
	Definition(ctx context.Context, slug string) (wf.Workflow, error)
}

// RunsChart reads a Run and the state of its steps.
type RunsChart interface {
	Get(ctx context.Context, id uuid.UUID) (run.Run, error)
	NodeStates(ctx context.Context, id uuid.UUID) (map[string]postgres.NodeState, error)
	LogsDaRun(ctx context.Context, id uuid.UUID) ([]postgres.StepLog, error)
}

// AlertsReader lists a run's alerts. It is a CONSTRUCTOR argument and not a
// settable field, so that forgetting to wire it is a compile error rather than
// a screen that quietly stops showing alerts. Two features shipped switched off
// in one week because nothing outside a test ever set the field they needed.
//
// nil is still allowed, for a process that renders no pages.
type AlertsReader interface {
	ForRun(ctx context.Context, runID uuid.UUID) ([]alerts.Record, error)
}

// CatalogReader lists the destinations the ecosystem writes, for /data. A
// constructor argument for AlertsReader's reason: forgetting to wire it is a
// compile error, not a catalog that quietly shows nothing. nil is allowed for a
// process that renders no pages.
type CatalogReader interface {
	Catalog(ctx context.Context) ([]postgres.CatalogEntry, error)
	CatalogTarget(ctx context.Context, target string) (*postgres.TargetDetail, error)
}

// Actions are the two effects the screen triggers. A small interface on purpose:
// the UI must not be able to do anything more to the system than pause a
// schedule and ask for a run now.
type Actions interface {
	Toggle(ctx context.Context, slug string) (bool, error)
	Disparar(ctx context.Context, slug string, now time.Time, params map[string]string) (uuid.UUID, error)
}

// UI registers the server-rendered pages and the JSON the React island consumes.
type UI struct {
	leitura Leitura
	defs    Definitions
	execs   RunsChart
	actions Actions
	alerts  AlertsReader
	catalog CatalogReader
	brand   branding.Brand
	log     *slog.Logger

	// preview asks `brevis-sql serve` for a destination's first rows. Nil
	// when no service is configured, and then the tab does not exist.
	preview *sqlserve.Client

	// protected says this console asks for a login. See dataTools.
	protected bool
}

// WithPreview points the console at a SQL service, and says whether this
// console has a login.
//
// A SETTER AND NOT AN ARGUMENT, because NewUI already takes eight: this one
// is optional in a way the others are not -- a console with no SQL service is
// a console that works, and every caller would otherwise pass nil.
//
// THE CREDENTIAL ITSELF AND NOT A BOOLEAN, so that nobody can answer this
// question wrongly by writing `true`. The only way to get the tabs is to hand
// over the console's real credential and have it be one; a bool would make
// "we are protected" something a caller could simply assert.
//
// An ARGUMENT and not a second setter, so forgetting it is a compile error
// rather than an open console with a warehouse on it.
func (u *UI) WithPreview(c *sqlserve.Client, console auth.Credential) *UI {
	u.preview = c
	u.protected = console.Enabled()
	return u
}

// dataTools says whether a destination may offer its Preview and Query tabs.
//
// THREE CONDITIONS, AND ONLY ONE IS ABOUT SQL.
//
// A console can run with no credential -- it warns at boot, "interface is
// OPEN: anyone can trigger a workflow" -- and the Query tab made that
// sentence incomplete: it is now also anyone can read every table in the
// project, because a query is not confined to the destination it was opened
// from. The tab does not create that hole, it changes what falls through it,
// so a data tool does not outlive the authentication of the screen it sits
// on.
//
// And a destination has to be a RELATION. A bucket and a topic have no
// columns and no rows; a tab on one is a box that can only ever say no,
// which is worse than no tab because it invites somebody to try. A relation
// nobody has written a reader for -- `mysql://` today -- keeps its tabs,
// because there the refusal is a sentence worth reading.
func (u *UI) dataTools(target string) bool {
	return u.protected && u.preview.Configured() && catalog.IsRelation(target)
}

func NewUI(l Leitura, d Definitions, e RunsChart, a Actions, al AlertsReader,
	c CatalogReader, m branding.Brand, log *slog.Logger,
) *UI {
	return &UI{leitura: l, defs: d, execs: e, actions: a, alerts: al, catalog: c, brand: m, log: log}
}

// Registrar wires the routes into the mux.
func (u *UI) Registrar(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", u.overview) // {$} matches the EXACT root, not the prefix
	mux.HandleFunc("GET /runs", u.runs)
	mux.HandleFunc("GET /workflows", u.workflows)
	mux.HandleFunc("GET /projects", u.projetos)
	mux.HandleFunc("GET /data", u.data)
	// THE WORKBENCH, and both verbs on one path: a GET draws the box and a
	// POST runs what is in it. The statement travels in the BODY, which is
	// why there is no second route carrying it.
	mux.HandleFunc("GET /sql", u.sql)
	mux.HandleFunc("POST /sql", u.sql)
	// A query parameter and not a path wildcard: ServeMux cleans `//` out of a
	// path, and would redirect /data/bigquery://… to /data/bigquery:/….
	mux.HandleFunc("GET /data/target", u.dataTarget)
	mux.HandleFunc("GET /runs/{id}/live", u.runLive)
	mux.HandleFunc("GET /workflows/{slug}", u.workflow)
	mux.HandleFunc("GET /runs/{id}", u.run)

	// Effects by POST, not GET: a link that pauses a schedule would be fired by
	// any browser prefetch or link crawler.
	mux.HandleFunc("POST /workflows/{slug}/toggle", u.toggle)
	mux.HandleFunc("POST /workflows/{slug}/trigger", u.disparar)

	// The JSON the React island fetches. It sits under /api so the URL makes
	// clear what is a page and what is data -- the same path serves both.
	mux.HandleFunc("GET /api/workflows/{slug}/graph", u.workflowGraph)
	mux.HandleFunc("GET /api/runs/{id}/graph", u.runGraph)
	// The workbench's tree, drawn a branch at a time. See sql_api.go for why
	// these answer HTML.
	mux.HandleFunc("GET /api/sql/objects", u.sqlObjects)
	mux.HandleFunc("GET /api/sql/columns", u.sqlColumns)
	// POST, BECAUSE THIS ONE CARRIES SQL. The two above take a target and a
	// relation; a statement must never travel in a URL. Registering the
	// method also 405s a GET for free, which is the assertion a test makes.
	mux.HandleFunc("POST /api/sql/estimate", u.sqlEstimate)

	// Served from the embed, not from disk: the container is distroless and has
	// no web/assets, and the binary has to work from any directory.
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", assets.Handler()))
}

// data lists every destination the ecosystem writes.
func (u *UI) data(w http.ResponseWriter, r *http.Request) {
	var entries []postgres.CatalogEntry
	if u.catalog != nil {
		var err error
		if entries, err = u.catalog.Catalog(r.Context()); err != nil {
			u.failure(w, r, err)
			return
		}
	}
	u.render(w, r, pages.Data(pages.BuildData(entries, time.Now()).Apply(pages.ParseDataFilter(r.URL.Query()))))
}

// targetCeiling matches the engine's own bound on a target. A legacy label
// is looked up as stored, so `u` is not shape-checked beyond this: an unknown
// value is a 404, not a 400.
const targetCeiling = 512

// dataTarget is one destination's page.
func (u *UI) dataTarget(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("u")
	if target == "" || len(target) > targetCeiling || strings.ContainsFunc(target, unicode.IsControl) {
		http.Error(w, "name a destination: /data/target?u=<target>", http.StatusBadRequest)
		return
	}
	if u.catalog == nil {
		http.NotFound(w, r)
		return
	}
	d, err := u.catalog.CatalogTarget(r.Context(), target)
	if err != nil {
		u.failure(w, r, err)
		return
	}
	if d == nil {
		http.Error(w, "nothing has landed on "+target, http.StatusNotFound)
		return
	}
	v := pages.BuildTarget(*d, time.Now())

	// NO SERVICE, NO TABS. A tab that always answers "not configured" is a
	// question nobody can act on.
	v.Tabs = u.dataTools(target)
	if v.Tabs {
		v.Tab = r.URL.Query().Get("tab")
	}

	// ONE TAB NOW. The destination previews; the workbench queries. See
	// TargetView.WorkbenchHref for why that is not a second tab.
	switch v.Tab {
	// ASKED ONLY WHEN ASKED FOR. A preview costs a warehouse query, and
	// drawing one on every visit to a destination page would mean a query per
	// page view, charged to somebody, for rows nobody looked at. `?tab=preview`
	// is a link, which also makes it shareable the way /data's filters are.
	case "preview":
		res, err := u.preview.Preview(r.Context(), target, previewRows)
		switch {
		case errors.Is(err, sqlserve.ErrNoConnection):
			v.PreviewErr = pages.NoConnectionFor(target)
		case err != nil:
			// The client already decided which of the service's words may be
			// repeated; this is a view and does not decide it again.
			v.PreviewErr = err.Error()
		default:
			v.Preview = &res
		}

	}
	u.render(w, r, pages.Target(v))
}

// previewRows is how many a destination page draws.
//
// The SERVICE has the ceiling and this cannot raise it; this is a request for
// fewer, and the number is here because it is a question about a SCREEN --
// twenty rows is what somebody glances at to see the shape of a table.
const previewRows = 20

// queryRows is how many a query draws, and statementCeiling how long a
// statement may be.
//
// MORE THAN A PREVIEW, because they are different questions: a preview is a
// glance at the shape of a table and a query is somebody's own report. The
// SERVICE has the real ceiling and this cannot raise it; this asks for fewer.
//
// The ceiling matches `serve`'s body limit rather than undercutting it, so a
// statement refused here is refused for the same reason it would be there --
// two different limits would mean a statement that one accepts and the other
// does not, and a refusal nobody can explain.
const (
	queryRows        = 100
	statementCeiling = 64 << 10
)

// sql is the workbench: one path, two verbs.
//
// IT RUNS NOTHING ON A GET. A warehouse query for opening a screen would be a
// bill for a page view, which is the rule the Preview tab already follows.
func (u *UI) sql(w http.ResponseWriter, r *http.Request) {
	if !u.preview.Configured() {
		// The bar does not offer this section without a service; somebody
		// typing the path gets the same answer rather than a dead box.
		http.NotFound(w, r)
		return
	}
	var entries []postgres.CatalogEntry
	if u.catalog != nil {
		var err error
		if entries, err = u.catalog.Catalog(r.Context()); err != nil {
			u.failure(w, r, err)
			return
		}
	}
	v := pages.BuildSQL(entries)

	// A TARGET IN A URL IS FINE AND A STATEMENT IS NOT. A destination page
	// links here carrying itself, so nobody has to find the connection again
	// in a picker -- and `/data/target?u=` already puts a target in a link.
	//
	// Only one the CATALOG knows is honoured: a parameter naming anything
	// else is ignored rather than trusted, which is the rule `/data/target`
	// follows by answering 404 to the same thing.
	if want := r.URL.Query().Get("target"); want != "" && v.Knows(want) {
		v.Target = want
	}

	if r.Method == http.MethodPost {
		// Bounded before it is parsed, matching `serve`'s own body limit.
		r.Body = http.MaxBytesReader(w, r.Body, statementCeiling)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "that statement is too long to run", http.StatusBadRequest)
			return
		}
		// FROM THE BODY AND NEVER THE QUERY STRING, which is what keeps a
		// statement out of a link. `PostFormValue` is what makes that true.
		v.Target = r.PostFormValue("target")
		v.Statement = strings.TrimSpace(r.PostFormValue("q"))

		// AND THE BODY IS CHECKED AGAINST THE CATALOG, which it was not.
		//
		// The paragraph above says a target in a URL is honoured "only [if]
		// one the CATALOG knows". That was true of the query string and
		// false of the body: this line read `target` straight out of a POST
		// and handed it to the service, which then answered a query AND a
		// listing for it.
		//
		// What it was worth to somebody signed in: the service answers
		// differently for a connection it holds and one it does not, so the
		// body was a way to ask `serve` what it has been configured with,
		// one guess at a time, with the catalog never consulted.
		if v.Target != "" && !v.Knows(v.Target) {
			v.Err, v.Target = pages.Unknown, ""
		}

		// WHAT IS OPEN IN THE TREE, and what was just clicked.
		//
		// `open` rides with every submit so running a query does not close
		// the relation somebody opened; `expand` is the tree's own button,
		// and clicking the one already open CLOSES it, which is what a
		// disclosure does.
		// AND WHICH CONNECTION, which the tree's root node submits now that
		// the picker is gone. Switching clears what is open: `Open` is a
		// `schema.name` with no connection in it, so carrying it across
		// would ask the new warehouse for the old one's relation.
		if want := r.PostFormValue("connect"); want != "" && v.Knows(want) {
			v.Target, v.Err = want, ""
			r.PostForm.Set("open", "")
		}

		v.Open = r.PostFormValue("open")
		if want := r.PostFormValue("expand"); want != "" {
			if want == v.Open {
				v.Open = ""
			} else {
				v.Open = want
			}
		}

		// EXPANDING IS NOT RUNNING. The tree's button submits the editor's
		// own form -- which is how the half-written statement survives the
		// click -- so without this, opening a table would also spend a
		// query nobody asked for.
		if r.PostFormValue("expand") == "" && r.PostFormValue("connect") == "" &&
			v.Statement != "" && v.Target != "" {
			res, err := u.preview.Query(r.Context(), v.Target, v.Statement, queryRows)
			switch {
			case errors.Is(err, sqlserve.ErrNoConnection):
				v.Err = pages.NoConnectionFor(v.Target)
			case err != nil:
				v.Err = err.Error()
			default:
				v.Result = &res
			}
		}
	}
	if v.Target == "" && len(v.Targets) > 0 {
		v.Target = v.Targets[0]
	}

	// THE TREE IS ASKED FOR LAST, once the target is settled -- including
	// the default -- because the listing belongs to a connection and the
	// default is a connection like any other.
	//
	// A WAREHOUSE THAT CANNOT SAY IS NOT AN ERROR PAGE. `Relations` is an
	// optional capability, the service answers 501 where a dialect lacks it,
	// and an unreachable service is the same shape of absence. Either way
	// the tree is not drawn and the box still runs queries: a console that
	// broke without the tree would be a console that needs it.
	if v.Target != "" {
		if objs, err := u.preview.Objects(r.Context(), v.Target); err == nil {
			v.Tree, v.TreeCut = pages.BuildTree(objs.Relations), objs.Truncated
		}
	}

	// AND THE OPEN RELATION'S COLUMNS, which is one more round trip and only
	// for the node somebody opened -- CHECKPOINT D made columns lazy because
	// a project-wide COLUMNS query is the one metadata answer that is
	// genuinely large. A warehouse that cannot describe is not an error page
	// either: the button says so where the columns would be.
	if schema, name, ok := strings.Cut(v.Open, "."); ok && v.Target != "" {
		if cols, err := u.preview.Columns(r.Context(), v.Target, schema, name); err == nil {
			v.Cols = cols
		}
	}
	u.render(w, r, pages.SQL(v))
}

func (u *UI) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	ind, err := u.leitura.Indicators(ctx, overviewWindow)
	if err != nil {
		u.failure(w, r, err)
		return
	}
	buckets, err := u.leitura.RunsPerHour(ctx, int(overviewWindow.Hours()))
	if err != nil {
		u.failure(w, r, err)
		return
	}
	emCurso, err := u.leitura.InFlight(ctx, 8)
	if err != nil {
		u.failure(w, r, err)
		return
	}
	recentes, err := u.leitura.LatestRuns(ctx, 10)
	if err != nil {
		u.failure(w, r, err)
		return
	}
	pending, _, err := u.leitura.QueueDepth(ctx)
	if err != nil {
		u.failure(w, r, err)
		return
	}
	schedules, err := u.leitura.Schedules(ctx)
	if err != nil {
		u.failure(w, r, err)
		return
	}

	u.render(w, r, pages.Overview(pages.OverviewData{
		Window:   overviewWindow,
		Ind:      ind,
		Buckets:  buckets,
		EmCurso:  emCurso,
		Proximas: nextRuns(schedules, time.Now(), 8, u.log),
		Recentes: recentes,
		Pending:  pending,
	}))
}

// nextRuns computes each active schedule's next dispatch and returns
// the soonest first.
//
// The computation lives here and not in the database: cron is a domain rule
// (`schedule`), and reimplementing it in SQL would create a second reading of
// the same field -- one that would one day diverge from the one the scheduler
// actually uses.
func nextRuns(schedules []postgres.ScheduleSummary, now time.Time, limite int,
	log *slog.Logger) []pages.NextRun {

	var out []pages.NextRun
	for _, a := range schedules {
		if !a.Active {
			continue
		}
		s := sch.Schedule{WorkflowSlug: a.WorkflowSlug, Cron: a.Cron, Timezone: a.Timezone, Active: true}
		prox, err := s.Next(now)
		if err != nil {
			// An invalid cron in the database must not take the whole dashboard
			// down; the schedule simply does not show up in the list.
			log.Warn("invalid cron while computing the next trigger",
				"workflow", a.WorkflowSlug, "cron", a.Cron, "error", err)
			continue
		}
		out = append(out, pages.NextRun{
			Workflow: a.WorkflowSlug, Cron: a.Cron, Timezone: a.Timezone, When: prox,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].When.Before(out[j].When) })
	if len(out) > limite {
		out = out[:limite]
	}
	return out
}

func (u *UI) runs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := pages.RunFilter{
		State:    validState(q.Get("state")),
		Workflow: q.Get("workflow"),
		De:       q.Get("from"),
		Ate:      q.Get("to"),
		Page:     page(q.Get("page")),
		PerPage:  pages.DefaultPerPage,
	}

	de, ate := instant(f.De), instant(f.Ate)
	if de == nil {
		f.De = ""
	}
	if ate == nil {
		f.Ate = ""
	}
	f.Label = periodLabel(de, ate)

	query := postgres.RunFilter{
		State: f.State, Workflow: f.Workflow, De: de, Ate: ate,
		Limite: f.PerPage, Offset: (f.Page - 1) * f.PerPage,
	}
	total, err := u.leitura.CountRuns(r.Context(), query)
	if err != nil {
		u.failure(w, r, err)
		return
	}
	runs, err := u.leitura.Runs(r.Context(), query)
	if err != nil {
		u.failure(w, r, err)
		return
	}
	u.render(w, r, pages.Runs(runs, f, total))
}

// validState refuses any state outside §7's machine.
//
// This is not about SQL -- the value already goes parameterised. It is about the
// screen: `?estado=xpto` would return an empty list with every chip greyed out,
// and the operator would read that as "there are no runs" rather than "that
// filter does not exist".
func validState(s string) string {
	switch s {
	case "queued", "running", "success", "failed", "retrying", "canceled":
		return s
	}
	return ""
}

func page(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// instante accepts the RFC3339 the chart's links emit. An invalid value becomes
// no filter rather than an error: a half-pasted link should not give a 500.
func instant(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

// periodLabel describes the window in the reader's own timezone. Without it
// the filter chip would show "2026-09-01T02:00:00Z", which is not the time the
// person saw on the chart.
func periodLabel(de, ate *time.Time) string {
	switch {
	case de != nil && ate != nil:
		l := de.Local()
		if ate.Sub(*de) == time.Hour {
			return l.Format("02/01 15h") + "–" + ate.Local().Format("15h")
		}
		return l.Format("Jan 02 15:04") + " → " + ate.Local().Format("Jan 02 15:04")
	case de != nil:
		return "from " + de.Local().Format("Jan 02 15:04")
	case ate != nil:
		return "until " + ate.Local().Format("Jan 02 15:04")
	}
	return ""
}

func (u *UI) workflows(w http.ResponseWriter, r *http.Request) {
	all, err := u.leitura.Workflows(r.Context())
	if err != nil {
		u.failure(w, r, err)
		return
	}

	now := time.Now()
	for i := range all {
		all[i].NextRun = proximaDoWorkflow(all[i], now)
	}

	q := r.URL.Query()
	f := pages.Filter{
		Search:  strings.TrimSpace(q.Get("q")),
		State:   validState(q.Get("state")),
		Active:  q.Get("active"),
		Tag:     q.Get("tag"),
		Project: q.Get("project"),
		Sort:    validOrder(q.Get("sort")),
		Desc:    q.Get("dir") == "desc",
		Page:    page(q.Get("page")),
		PerPage: pages.DefaultPerPage,
	}

	filtered := filtrar(all, f)
	sortBy(filtered, f)
	u.render(w, r, pages.Workflows(recortar(filtered, f), tagsDe(all), f,
		len(all), len(filtered)))
}

// validOrder limits sorting to the columns that exist -- without it, `?ordem=;`
// would only produce a list in an unexplained order.
func validOrder(s string) string {
	switch s {
	case "workflow", "schedule", "next", "last":
		return s
	}
	return ""
}

// sortBy applies the chosen column.
//
// Two rules the naive inversion (comparing with the arguments swapped) broke: a
// MISSING value stays last in both directions -- sorting by "last run" must not
// start with the ones that never ran -- and the slug tie-break is always
// ascending, or two equivalent rows swap places on every load.
func sortBy(ws []postgres.WorkflowSummary, f pages.Filter) {
	if f.Sort == "" {
		return
	}
	sort.SliceStable(ws, func(i, j int) bool {
		a, b := ws[i], ws[j]
		temA, temB := hasValue(a, f.Sort), hasValue(b, f.Sort)
		if temA != temB {
			return temA
		}
		if temA {
			if c := compareField(a, b, f.Sort); c != 0 {
				if f.Desc {
					return c > 0
				}
				return c < 0
			}
		}
		return a.Slug < b.Slug
	})
}

func hasValue(w postgres.WorkflowSummary, field string) bool {
	switch field {
	case "schedule":
		return w.Cron != ""
	case "next":
		return w.NextRun != nil
	case "last":
		return w.LastRunAt != nil
	}
	return true
}

func compareField(a, b postgres.WorkflowSummary, field string) int {
	switch field {
	case "schedule":
		return strings.Compare(a.Cron, b.Cron)
	case "next":
		return comparaTempo(a.NextRun, b.NextRun)
	case "last":
		return comparaTempo(a.LastRunAt, b.LastRunAt)
	}
	return strings.Compare(a.Slug, b.Slug)
}

// comparaTempo only receives present values: absence is decided earlier, in
// `temValor`, precisely so it does not depend on the direction.
func comparaTempo(a, b *time.Time) int {
	switch {
	case a.Before(*b):
		return -1
	case a.After(*b):
		return 1
	}
	return 0
}

// recortar returns the requested page. A page past the end comes back empty
// rather than overflowing the slice -- which happens when filtering while on a
// high page.
func recortar(ws []postgres.WorkflowSummary, f pages.Filter) []postgres.WorkflowSummary {
	de := (f.Page - 1) * f.PerPage
	if de >= len(ws) {
		return nil
	}
	ate := de + f.PerPage
	if ate > len(ws) {
		ate = len(ws)
	}
	return ws[de:ate]
}

func proximaDoWorkflow(w postgres.WorkflowSummary, now time.Time) *time.Time {
	if !w.Active || w.Cron == "" {
		return nil
	}
	s := sch.Schedule{WorkflowSlug: w.Slug, Cron: w.Cron, Timezone: w.Timezone, Active: true}
	prox, err := s.Next(now)
	if err != nil {
		return nil
	}
	return &prox
}

// filtrar applies the search bar in memory.
//
// In memory and not in SQL because the workflow list is in the dozens, and
// filtering by LAST state would mean repeating the query's LATERAL inside a
// WHERE. If it ever becomes thousands, this turns into a database predicate.
func filtrar(ws []postgres.WorkflowSummary, f pages.Filter) []postgres.WorkflowSummary {
	search := strings.ToLower(f.Search)
	out := make([]postgres.WorkflowSummary, 0, len(ws))
	for _, w := range ws {
		if search != "" && !strings.Contains(strings.ToLower(w.Slug), search) &&
			!strings.Contains(strings.ToLower(w.Name), search) {
			continue
		}
		if f.State != "" && w.LastStatus != f.State {
			continue
		}
		switch f.Active {
		case "active":
			if !w.Active {
				continue
			}
		case "paused":
			// No schedule is not "paused": it is a workflow that never had a
			// cron, and mixing the two would hide precisely the schedule that
			// was turned off.
			if w.Active || !w.HasSchedule {
				continue
			}
		}
		if f.Tag != "" && !contains(w.Tags, f.Tag) {
			continue
		}
		if f.Project != "" && w.Project != f.Project {
			continue
		}
		out = append(out, w)
	}
	return out
}

func contains(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

// tagsDe gathers the tags of ALL workflows, not of the filtered rows: the filter
// bar must not shrink as you filter, or there is no way back.
func tagsDe(ws []postgres.WorkflowSummary) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, w := range ws {
		for _, t := range w.Tags {
			if _, ja := seen[t]; ja {
				continue
			}
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func (u *UI) projetos(w http.ResponseWriter, r *http.Request) {
	ps, err := u.leitura.Projects(r.Context())
	if err != nil {
		u.failure(w, r, err)
		return
	}
	u.render(w, r, pages.Projects(ps))
}

// workflow is a workflow's page: a server-rendered header plus the DAG as an island.
func (u *UI) workflow(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	def, err := u.defs.Definition(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// A missing history does not block the screen: the definition is the main content.
	latest, err := u.leitura.WorkflowRuns(r.Context(), slug, 10)
	if err != nil {
		u.log.Warn("the workflow's history is unavailable", "workflow", slug, "error", err)
	}

	// The statistics, and the same rule: each one degrades to nothing rather
	// than taking the page down. A workflow's definition is readable with no
	// database behind it, and that is the property worth keeping -- somebody
	// looking at a broken installation is exactly who needs to read the graph.
	stats := pages.WorkflowStats{Window: workflowWindow}
	if ind, err := u.leitura.IndicatorsFor(r.Context(), workflowWindow, slug); err == nil {
		stats.Ind = ind
	} else {
		u.log.Warn("the workflow's indicators are unavailable", "workflow", slug, "error", err)
	}
	if days, err := u.leitura.RunsPerDay(r.Context(), slug, 364); err == nil {
		stats.Days = days
	} else {
		u.log.Warn("the workflow's calendar is unavailable", "workflow", slug, "error", err)
	}
	if load, err := u.leitura.LoadTrend(r.Context(), slug, components.TrendDays); err == nil {
		stats.Load = load
	} else {
		u.log.Warn("the workflow's load trend is unavailable", "workflow", slug, "error", err)
	}

	u.render(w, r, pages.Workflow(def, latest, stats))
}

// run is a run's page. The header comes from the database on the server; the DAG
// with each step's state arrives by fetch, and only that needs JavaScript.
func (u *UI) run(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	run, err := u.execs.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// A missing log does not block the screen: the rest of the page stays
	// useful, and a freshly queued run legitimately has no steps yet.
	logs, err := u.execs.LogsDaRun(r.Context(), id)
	if err != nil {
		u.log.Warn("the run's logs are unavailable", "run", id, "error", err)
	}
	// The alerts, on the same terms as the logs: unavailable is not a reason to
	// refuse the page. An alert nobody can see is indistinguishable from an
	// alert that was never sent, which is what this section exists to fix --
	// and a blank page would be worse at it than a page missing one block.
	var raised []alerts.Record
	if u.alerts != nil {
		if raised, err = u.alerts.ForRun(r.Context(), id); err != nil {
			u.log.Warn("the run's alerts are unavailable", "run", id, "error", err)
		}
	}
	u.render(w, r, pages.Run(run, logs, raised))
}

// runLive returns the run's two changing regions, already rendered.
//
// It is a FRAGMENT and not a JSON payload, and that is the decision worth
// stating: the alternative is rendering a step's state a second time in
// JavaScript, and two renderings of "what colour is retrying" is two places to
// keep in step -- the shape this repository has been bitten by five times. The
// server already knows how to draw these.
//
// It also reads the same data as `run`, deliberately. Sharing a helper between
// the two would look tidier and would mean a page and its refresh could
// disagree about what a run is; here they cannot, because they run the same
// three queries and pass them to the same templates.
func (u *UI) runLive(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	current, err := u.execs.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	logs, err := u.execs.LogsDaRun(r.Context(), id)
	if err != nil {
		u.log.Warn("the run's logs are unavailable", "run", id, "error", err)
	}
	var raised []alerts.Record
	if u.alerts != nil {
		if raised, err = u.alerts.ForRun(r.Context(), id); err != nil {
			u.log.Warn("the run's alerts are unavailable", "run", id, "error", err)
		}
	}

	// No caching, at any layer. A refresh that a proxy answered from thirty
	// seconds ago is a screen that lies about a run in flight, which is worse
	// than one that does not refresh at all -- the second is visibly stale and
	// the first is not.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pages.RunLive(current, logs, raised).Render(r.Context(), w); err != nil {
		u.log.Warn("rendering the live fragment", "run", id, "error", err)
	}
}

func (u *UI) toggle(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	active1, err := u.actions.Toggle(r.Context(), slug)
	if err != nil {
		u.failure(w, r, err)
		return
	}
	u.log.Info("schedule toggled", "workflow", slug, "active", active1)
	u.voltar(w, r)
}

// paramsFromForm reads the `param.`-prefixed fields of the trigger form.
//
// Values are joined with a comma, because that is how a `list|<type>` param
// travels everywhere else -- and it is what makes a checkbox group work without
// a line of JavaScript: the browser sends one `param.layers` per box ticked,
// and an unticked box sends nothing at all.
//
// Each list also carries a hidden empty value, which is what makes "none of
// them" different from "the operator did not touch this". With every box
// unticked and no hidden field, the key would be absent from the form and the
// resolver would fall back to the default -- quietly running with the values
// the operator had just cleared.
func paramsFromForm(form url.Values) map[string]string {
	params := map[string]string{}
	for key, values := range form {
		name, ok := strings.CutPrefix(key, "param.")
		if !ok {
			continue
		}
		nonEmpty := make([]string, 0, len(values))
		for _, v := range values {
			if v = strings.TrimSpace(v); v != "" {
				nonEmpty = append(nonEmpty, v)
			}
		}
		params[name] = strings.Join(nonEmpty, ",")
	}
	return params
}

func (u *UI) disparar(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	// The params come from the form, prefixed with `param.` so they do not
	// collide with future fields of the form itself.
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	params := paramsFromForm(r.PostForm)

	id, err := u.actions.Disparar(r.Context(), slug, time.Now(), params)
	if err != nil {
		// An invalid param is an INPUT error, not the server's: a 500 here would
		// send the operator looking for a defect in the platform rather than in
		// the value they typed.
		u.log.Warn("trigger refused", "workflow", slug, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if id == uuid.Nil {
		// The idempotency key collided: two clicks in the same second become one
		// run. Going back to the list is the right behaviour -- there is no new
		// run to go to.
		u.log.Info("trigger ignored by idempotency", "workflow", slug)
		u.voltar(w, r)
		return
	}
	u.log.Info("manual run created", "workflow", slug, "run", id)
	http.Redirect(w, r, "/runs/"+id.String(), http.StatusSeeOther)
}

// voltar returns the operator to the screen they clicked from, keeping the filters.
//
// 303 and not 302: after a POST, the 303 forces the browser to redo the
// navigation as a GET, which is what prevents the "resend form?" prompt on
// reload.
func (u *UI) voltar(w http.ResponseWriter, r *http.Request) {
	target := r.Referer()
	// It only accepts a destination on this site: an external Referer would turn
	// the redirect into an open-redirect vector.
	if target == "" || !strings.HasPrefix(target, "/") {
		if u := parseMesmoHost(r); u != "" {
			target = u
		} else {
			target = "/workflows"
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// parseMesmoHost accepts the Referer only when it points at this same host.
func parseMesmoHost(r *http.Request) string {
	ref := r.Referer()
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host != r.Host {
		return ""
	}
	path := u.EscapedPath()
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return path
}

func (u *UI) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The brand travels in the context: every template reaches it without it
	// having to enter each page's signature.
	// The brand and whether there is a SQL service both travel in the
	// context: every template reaches them without entering each page's
	// signature.
	ctx := layouts.WithSQL(branding.IntoContext(r.Context(), u.brand), u.preview.Configured())
	if err := c.Render(ctx, w); err != nil {
		u.log.Error("rendering the page", "path", r.URL.Path, "error", err)
	}
}

func (u *UI) failure(w http.ResponseWriter, r *http.Request, err error) {
	u.log.Error("querying the ui's data", "path", r.URL.Path, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// RegistrarLogin wires the session routes. It is kept apart from Registrar
// because it only exists when there is a credential: without one, a login screen
// that always accepts would be worse than none.
func (u *UI) RegistrarLogin(mux *http.ServeMux, gate *auth.Gate) {
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		u.render(w, r, pages.Login(pages.LoginData{
			Target: auth.Target(r.URL.Query().Get(auth.NextParam)),
		}))
	})

	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		target := auth.Target(r.FormValue(auth.NextParam))
		user := r.FormValue("username")

		if !gate.SignIn(w, user, r.FormValue("password")) {
			// Logged as a warning, with the attempted username and the origin: a
			// burst of failures is the only sign somebody is guessing, and
			// without a log it does not exist. The password never enters
			// here.
			u.log.Warn("login refused", "user", user, "origin", r.RemoteAddr)

			// 200, and not a redirect: the form comes back filled in with the
			// destination and the error in the same response.
			w.WriteHeader(http.StatusUnauthorized)
			u.render(w, r, pages.Login(pages.LoginData{
				Target: target,
				Err:    "Invalid username or password.",
			}))
			return
		}
		u.log.Info("login", "user", user)
		http.Redirect(w, r, target, http.StatusSeeOther)
	})

	// POST, not GET: an <img src="/logout"> on any page would drop the session
	// of whoever opened it.
	mux.HandleFunc("POST /logout", func(w http.ResponseWriter, r *http.Request) {
		gate.SignOut(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}
