package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/AreteAcademy/brevis/internal/auth"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/domain/run"
	sch "github.com/AreteAcademy/brevis/internal/domain/schedule"
	wf "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/assets"
	"github.com/AreteAcademy/brevis/web/pages"
)

// overviewWindow is the dashboard's horizon. Twenty-four hours cover a full
// daily cycle -- most schedules are daily, and a shorter window would show only
// part of the day and make the success rate swing because of the cut, not
// because anything changed.
const overviewWindow = 24 * time.Hour

// Leitura is what the UI needs from the database. The interface is declared here, in the consumer.
type Leitura interface {
	Indicators(ctx context.Context, window time.Duration) (postgres.Indicators, error)
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
	brand   branding.Brand
	log     *slog.Logger
}

func NewUI(l Leitura, d Definitions, e RunsChart, a Actions, m branding.Brand, log *slog.Logger) *UI {
	return &UI{leitura: l, defs: d, execs: e, actions: a, brand: m, log: log}
}

// Registrar wires the routes into the mux.
func (u *UI) Registrar(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", u.overview) // {$} matches the EXACT root, not the prefix
	mux.HandleFunc("GET /runs", u.runs)
	mux.HandleFunc("GET /workflows", u.workflows)
	mux.HandleFunc("GET /projects", u.projetos)
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

	// Served from the embed, not from disk: the container is distroless and has
	// no web/assets, and the binary has to work from any directory.
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(assets.FS)))
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
	u.render(w, r, pages.Workflow(def, latest))
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
	u.render(w, r, pages.Run(run, logs))
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

func (u *UI) disparar(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	// The params come from the form, prefixed with `param.` so they do not
	// collide with future fields of the form itself.
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulario invalido", http.StatusBadRequest)
		return
	}
	params := map[string]string{}
	for key, values := range r.PostForm {
		if name, ok := strings.CutPrefix(key, "param."); ok && len(values) > 0 {
			params[name] = values[0]
		}
	}

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
	u.log.Info("run manual criado", "workflow", slug, "run", id)
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
	if err := c.Render(branding.IntoContext(r.Context(), u.brand), w); err != nil {
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
			Target: auth.Target(r.URL.Query().Get("next")),
		}))
	})

	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		target := auth.Target(r.FormValue("next"))
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
