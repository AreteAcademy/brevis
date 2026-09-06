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

// janelaOverview is the dashboard's horizon. Twenty-four hours cover a full
// daily cycle -- most schedules are daily, and a shorter window would show only
// part of the day and make the success rate swing because of the cut, not
// because anything changed.
const janelaOverview = 24 * time.Hour

// Leitura is what the UI needs from the database. The interface is declared here, in the consumer.
type Leitura interface {
	Indicadores(ctx context.Context, janela time.Duration) (postgres.Indicadores, error)
	ExecucoesPorHora(ctx context.Context, horas int) ([]postgres.Balde, error)
	EmAndamento(ctx context.Context, limite int) ([]postgres.ResumoRun, error)
	UltimasRuns(ctx context.Context, limite int) ([]postgres.ResumoRun, error)
	Runs(ctx context.Context, f postgres.FiltroRuns) ([]postgres.ResumoRun, error)
	ContarRuns(ctx context.Context, f postgres.FiltroRuns) (int, error)
	RunsDoWorkflow(ctx context.Context, slug string, limite int) ([]postgres.ResumoRun, error)
	Workflows(ctx context.Context) ([]postgres.ResumoWorkflow, error)
	Agendas(ctx context.Context) ([]postgres.AgendaResumo, error)
	Projetos(ctx context.Context) ([]postgres.ResumoProjeto, error)
	ProfundidadeDaFila(ctx context.Context) (int, int, error)
}

// Definicoes reads a workflow's published definition. Kept apart from `Leitura`
// because it returns the domain, not a screen projection.
type Definicoes interface {
	Definicao(ctx context.Context, slug string) (wf.Workflow, error)
}

// Execucoes reads a Run and the state of its steps.
type Execucoes interface {
	Buscar(ctx context.Context, id uuid.UUID) (run.Run, error)
	EstadoDosNos(ctx context.Context, id uuid.UUID) (map[string]postgres.EstadoNo, error)
	LogsDaRun(ctx context.Context, id uuid.UUID) ([]postgres.LogDoPasso, error)
}

// Acoes are the two effects the screen triggers. A small interface on purpose:
// the UI must not be able to do anything more to the system than pause a
// schedule and ask for a run now.
type Acoes interface {
	Alternar(ctx context.Context, slug string) (bool, error)
	Disparar(ctx context.Context, slug string, agora time.Time, params map[string]string) (uuid.UUID, error)
}

// UI registers the server-rendered pages and the JSON the React island consumes.
type UI struct {
	leitura Leitura
	defs    Definicoes
	execs   Execucoes
	acoes   Acoes
	marca   branding.Marca
	log     *slog.Logger
}

func NewUI(l Leitura, d Definicoes, e Execucoes, a Acoes, m branding.Marca, log *slog.Logger) *UI {
	return &UI{leitura: l, defs: d, execs: e, acoes: a, marca: m, log: log}
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
	mux.HandleFunc("POST /workflows/{slug}/toggle", u.alternar)
	mux.HandleFunc("POST /workflows/{slug}/trigger", u.disparar)

	// The JSON the React island fetches. It sits under /api so the URL makes
	// clear what is a page and what is data -- the same path serves both.
	mux.HandleFunc("GET /api/workflows/{slug}/graph", u.grafoDoWorkflow)
	mux.HandleFunc("GET /api/runs/{id}/graph", u.grafoDaRun)

	// Served from the embed, not from disk: the container is distroless and has
	// no web/assets, and the binary has to work from any directory.
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(assets.FS)))
}

func (u *UI) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	ind, err := u.leitura.Indicadores(ctx, janelaOverview)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	baldes, err := u.leitura.ExecucoesPorHora(ctx, int(janelaOverview.Hours()))
	if err != nil {
		u.erro(w, r, err)
		return
	}
	emCurso, err := u.leitura.EmAndamento(ctx, 8)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	recentes, err := u.leitura.UltimasRuns(ctx, 10)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	pendentes, _, err := u.leitura.ProfundidadeDaFila(ctx)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	agendas, err := u.leitura.Agendas(ctx)
	if err != nil {
		u.erro(w, r, err)
		return
	}

	u.render(w, r, pages.Overview(pages.DadosOverview{
		Janela:    janelaOverview,
		Ind:       ind,
		Baldes:    baldes,
		EmCurso:   emCurso,
		Proximas:  proximasExecucoes(agendas, time.Now(), 8, u.log),
		Recentes:  recentes,
		Pendentes: pendentes,
	}))
}

// proximasExecucoes computes each active schedule's next dispatch and returns
// the soonest first.
//
// The computation lives here and not in the database: cron is a domain rule
// (`schedule`), and reimplementing it in SQL would create a second reading of
// the same field -- one that would one day diverge from the one the scheduler
// actually uses.
func proximasExecucoes(agendas []postgres.AgendaResumo, agora time.Time, limite int,
	log *slog.Logger) []pages.ProximaExecucao {

	var out []pages.ProximaExecucao
	for _, a := range agendas {
		if !a.Ativo {
			continue
		}
		s := sch.Schedule{WorkflowSlug: a.WorkflowSlug, Cron: a.Cron, Timezone: a.Timezone, Ativo: true}
		prox, err := s.Proximo(agora)
		if err != nil {
			// An invalid cron in the database must not take the whole dashboard
			// down; the schedule simply does not show up in the list.
			log.Warn("invalid cron while computing the next trigger",
				"workflow", a.WorkflowSlug, "cron", a.Cron, "error", err)
			continue
		}
		out = append(out, pages.ProximaExecucao{
			Workflow: a.WorkflowSlug, Cron: a.Cron, Timezone: a.Timezone, Quando: prox,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Quando.Before(out[j].Quando) })
	if len(out) > limite {
		out = out[:limite]
	}
	return out
}

func (u *UI) runs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := pages.FiltroRuns{
		Estado:    estadoValido(q.Get("state")),
		Workflow:  q.Get("workflow"),
		De:        q.Get("from"),
		Ate:       q.Get("to"),
		Pagina:    pagina(q.Get("page")),
		PorPagina: pages.PorPaginaPadrao,
	}

	de, ate := instante(f.De), instante(f.Ate)
	if de == nil {
		f.De = ""
	}
	if ate == nil {
		f.Ate = ""
	}
	f.Rotulo = rotuloDoPeriodo(de, ate)

	consulta := postgres.FiltroRuns{
		Estado: f.Estado, Workflow: f.Workflow, De: de, Ate: ate,
		Limite: f.PorPagina, Offset: (f.Pagina - 1) * f.PorPagina,
	}
	total, err := u.leitura.ContarRuns(r.Context(), consulta)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	runs, err := u.leitura.Runs(r.Context(), consulta)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	u.render(w, r, pages.Runs(runs, f, total))
}

// estadoValido refuses any state outside §7's machine.
//
// This is not about SQL -- the value already goes parameterised. It is about the
// screen: `?estado=xpto` would return an empty list with every chip greyed out,
// and the operator would read that as "there are no runs" rather than "that
// filter does not exist".
func estadoValido(s string) string {
	switch s {
	case "queued", "running", "success", "failed", "retrying", "canceled":
		return s
	}
	return ""
}

func pagina(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// instante accepts the RFC3339 the chart's links emit. An invalid value becomes
// no filter rather than an error: a half-pasted link should not give a 500.
func instante(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

// rotuloDoPeriodo describes the window in the reader's own timezone. Without it
// the filter chip would show "2026-09-01T02:00:00Z", which is not the time the
// person saw on the chart.
func rotuloDoPeriodo(de, ate *time.Time) string {
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
	todos, err := u.leitura.Workflows(r.Context())
	if err != nil {
		u.erro(w, r, err)
		return
	}

	agora := time.Now()
	for i := range todos {
		todos[i].ProximaRun = proximaDoWorkflow(todos[i], agora)
	}

	q := r.URL.Query()
	f := pages.Filtro{
		Busca:     strings.TrimSpace(q.Get("q")),
		Estado:    estadoValido(q.Get("state")),
		Ativo:     q.Get("active"),
		Tag:       q.Get("tag"),
		Ordem:     ordemValida(q.Get("sort")),
		Desc:      q.Get("dir") == "desc",
		Pagina:    pagina(q.Get("page")),
		PorPagina: pages.PorPaginaPadrao,
	}

	filtrados := filtrar(todos, f)
	ordenar(filtrados, f)
	u.render(w, r, pages.Workflows(recortar(filtrados, f), tagsDe(todos), f,
		len(todos), len(filtrados)))
}

// ordemValida limits sorting to the columns that exist -- without it, `?ordem=;`
// would only produce a list in an unexplained order.
func ordemValida(s string) string {
	switch s {
	case "workflow", "schedule", "next", "last":
		return s
	}
	return ""
}

// ordenar applies the chosen column.
//
// Two rules the naive inversion (comparing with the arguments swapped) broke: a
// MISSING value stays last in both directions -- sorting by "last run" must not
// start with the ones that never ran -- and the slug tie-break is always
// ascending, or two equivalent rows swap places on every load.
func ordenar(ws []postgres.ResumoWorkflow, f pages.Filtro) {
	if f.Ordem == "" {
		return
	}
	sort.SliceStable(ws, func(i, j int) bool {
		a, b := ws[i], ws[j]
		temA, temB := temValor(a, f.Ordem), temValor(b, f.Ordem)
		if temA != temB {
			return temA
		}
		if temA {
			if c := comparaCampo(a, b, f.Ordem); c != 0 {
				if f.Desc {
					return c > 0
				}
				return c < 0
			}
		}
		return a.Slug < b.Slug
	})
}

func temValor(w postgres.ResumoWorkflow, campo string) bool {
	switch campo {
	case "schedule":
		return w.Cron != ""
	case "next":
		return w.ProximaRun != nil
	case "last":
		return w.UltimaRunEm != nil
	}
	return true
}

func comparaCampo(a, b postgres.ResumoWorkflow, campo string) int {
	switch campo {
	case "schedule":
		return strings.Compare(a.Cron, b.Cron)
	case "next":
		return comparaTempo(a.ProximaRun, b.ProximaRun)
	case "last":
		return comparaTempo(a.UltimaRunEm, b.UltimaRunEm)
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
func recortar(ws []postgres.ResumoWorkflow, f pages.Filtro) []postgres.ResumoWorkflow {
	de := (f.Pagina - 1) * f.PorPagina
	if de >= len(ws) {
		return nil
	}
	ate := de + f.PorPagina
	if ate > len(ws) {
		ate = len(ws)
	}
	return ws[de:ate]
}

func proximaDoWorkflow(w postgres.ResumoWorkflow, agora time.Time) *time.Time {
	if !w.Ativo || w.Cron == "" {
		return nil
	}
	s := sch.Schedule{WorkflowSlug: w.Slug, Cron: w.Cron, Timezone: w.Timezone, Ativo: true}
	prox, err := s.Proximo(agora)
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
func filtrar(ws []postgres.ResumoWorkflow, f pages.Filtro) []postgres.ResumoWorkflow {
	busca := strings.ToLower(f.Busca)
	out := make([]postgres.ResumoWorkflow, 0, len(ws))
	for _, w := range ws {
		if busca != "" && !strings.Contains(strings.ToLower(w.Slug), busca) &&
			!strings.Contains(strings.ToLower(w.Nome), busca) {
			continue
		}
		if f.Estado != "" && w.UltimoStatus != f.Estado {
			continue
		}
		switch f.Ativo {
		case "active":
			if !w.Ativo {
				continue
			}
		case "paused":
			// No schedule is not "paused": it is a workflow that never had a
			// cron, and mixing the two would hide precisely the schedule that
			// was turned off.
			if w.Ativo || !w.TemAgenda {
				continue
			}
		}
		if f.Tag != "" && !contem(w.Tags, f.Tag) {
			continue
		}
		out = append(out, w)
	}
	return out
}

func contem(lista []string, alvo string) bool {
	for _, s := range lista {
		if s == alvo {
			return true
		}
	}
	return false
}

// tagsDe gathers the tags of ALL workflows, not of the filtered rows: the filter
// bar must not shrink as you filter, or there is no way back.
func tagsDe(ws []postgres.ResumoWorkflow) []string {
	vistas := map[string]struct{}{}
	var out []string
	for _, w := range ws {
		for _, t := range w.Tags {
			if _, ja := vistas[t]; ja {
				continue
			}
			vistas[t] = struct{}{}
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func (u *UI) projetos(w http.ResponseWriter, r *http.Request) {
	ps, err := u.leitura.Projetos(r.Context())
	if err != nil {
		u.erro(w, r, err)
		return
	}
	u.render(w, r, pages.Projetos(ps))
}

// workflow is a workflow's page: a server-rendered header plus the DAG as an island.
func (u *UI) workflow(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	def, err := u.defs.Definicao(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// A missing history does not block the screen: the definition is the main content.
	ultimas, err := u.leitura.RunsDoWorkflow(r.Context(), slug, 10)
	if err != nil {
		u.log.Warn("the workflow's history is unavailable", "workflow", slug, "error", err)
	}
	u.render(w, r, pages.Workflow(def, ultimas))
}

// run is a run's page. The header comes from the database on the server; the DAG
// with each step's state arrives by fetch, and only that needs JavaScript.
func (u *UI) run(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	execucao, err := u.execs.Buscar(r.Context(), id)
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
	u.render(w, r, pages.Run(execucao, logs))
}

func (u *UI) alternar(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	ativo, err := u.acoes.Alternar(r.Context(), slug)
	if err != nil {
		u.erro(w, r, err)
		return
	}
	u.log.Info("schedule toggled", "workflow", slug, "active", ativo)
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
	for chave, valores := range r.PostForm {
		if nome, ok := strings.CutPrefix(chave, "param."); ok && len(valores) > 0 {
			params[nome] = valores[0]
		}
	}

	id, err := u.acoes.Disparar(r.Context(), slug, time.Now(), params)
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
	destino := r.Referer()
	// So aceita destino do proprio site: um Referer externo transformaria o
	// redirect num vetor de redirecionamento aberto.
	if destino == "" || !strings.HasPrefix(destino, "/") {
		if u := parseMesmoHost(r); u != "" {
			destino = u
		} else {
			destino = "/workflows"
		}
	}
	http.Redirect(w, r, destino, http.StatusSeeOther)
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
	caminho := u.EscapedPath()
	if u.RawQuery != "" {
		caminho += "?" + u.RawQuery
	}
	return caminho
}

func (u *UI) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The brand travels in the context: every template reaches it without it
	// having to enter each page's signature.
	if err := c.Render(branding.EmContexto(r.Context(), u.marca), w); err != nil {
		u.log.Error("rendering the page", "path", r.URL.Path, "error", err)
	}
}

func (u *UI) erro(w http.ResponseWriter, r *http.Request, err error) {
	u.log.Error("querying the ui's data", "path", r.URL.Path, "error", err)
	http.Error(w, "erro interno", http.StatusInternalServerError)
}

// RegistrarLogin wires the session routes. It is kept apart from Registrar
// because it only exists when there is a credential: without one, a login screen
// that always accepts would be worse than none.
func (u *UI) RegistrarLogin(mux *http.ServeMux, portao *auth.Portao) {
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		u.render(w, r, pages.Login(pages.DadosLogin{
			Destino: auth.Destino(r.URL.Query().Get("next")),
		}))
	})

	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		destino := auth.Destino(r.FormValue("next"))
		usuario := r.FormValue("username")

		if !portao.Entrar(w, usuario, r.FormValue("password")) {
			// Logged as a warning, with the attempted username and the origin: a
			// burst of failures is the only sign somebody is guessing, and
			// without a log it does not exist. The password never enters
			// here.
			u.log.Warn("login refused", "user", usuario, "origin", r.RemoteAddr)

			// 200, and not a redirect: the form comes back filled in with the
			// destination and the error in the same response.
			w.WriteHeader(http.StatusUnauthorized)
			u.render(w, r, pages.Login(pages.DadosLogin{
				Destino: destino,
				Erro:    "Invalid username or password.",
			}))
			return
		}
		u.log.Info("login", "user", usuario)
		http.Redirect(w, r, destino, http.StatusSeeOther)
	})

	// POST, not GET: an <img src="/logout"> on any page would drop the session
	// of whoever opened it.
	mux.HandleFunc("POST /logout", func(w http.ResponseWriter, r *http.Request) {
		portao.Sair(w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}
