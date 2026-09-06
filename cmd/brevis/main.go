// Command brevis is the platform's single binary.
//
// One binary with subcommands, not several binaries: the plan (section 2)
// describes the API, the scheduler and the workers as roles of the same system,
// and a single binary keeps one version, one image and one build path.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/AreteAcademy/brevis/internal/api"
	app "github.com/AreteAcademy/brevis/internal/application/execution"
	spec "github.com/AreteAcademy/brevis/internal/application/workflow"
	"github.com/AreteAcademy/brevis/internal/auth"
	"github.com/AreteAcademy/brevis/internal/branding"
	"github.com/AreteAcademy/brevis/internal/config"
	wfdom "github.com/AreteAcademy/brevis/internal/domain/workflow"
	"github.com/AreteAcademy/brevis/internal/execution"
	k8s "github.com/AreteAcademy/brevis/internal/execution/kubernetes"
	"github.com/AreteAcademy/brevis/internal/execution/local"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/internal/notify"
	"github.com/AreteAcademy/brevis/internal/observability"
	"github.com/AreteAcademy/brevis/internal/queue"
	"github.com/AreteAcademy/brevis/internal/scheduler"
)

func main() {
	if err := raiz().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func raiz() *cobra.Command {
	c := &cobra.Command{
		Use:           "brevis",
		Short:         "Brevis — a data transformation and orchestration engine",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	c.AddCommand(cmdServe(), cmdMigrate(), cmdValidate(), cmdBrand(), cmdHash(), cmdRun(), cmdPublish(),
		cmdScheduler(), cmdBackfill(), cmdVersion())
	return c
}

// Versao is stamped at build time (-ldflags). "dev" is the value for whoever
// built straight with `go build`, and telling that apart from a release
// artifact matters when somebody reports odd behaviour.
var (
	Versao = "dev"
	Commit = ""
	Data   = ""
)

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the binary's version",
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Printf("brevis %s\n", Versao)
			if Commit != "" {
				fmt.Printf("  commit  %s\n", Commit)
			}
			if Data != "" {
				fmt.Printf("  build   %s\n", Data)
			}
			fmt.Printf("  go      %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}

func cmdServe() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP API",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return serve(cmd.Context())
		},
	}
}

func cmdMigrate() *cobra.Command {
	return &cobra.Command{
		Use:       "migrate [up|down|status]",
		Short:     "Apply the schema migrations",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"up", "down", "status"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return postgres.Migrate(cmd.Context(), cfg.DatabaseURL, args[0])
		},
	}
}

// cmdValidate exists to give an answer BEFORE publishing. Section 5 of the plan
// says to validate the DAG before saving; being able to run that in the editor
// or in CI, with no database and no server, is what makes the rule useful rather
// than bureaucratic.
// emLinha prints the params in a stable order — two identical runs have to
// produce the same log.
func emLinha(m map[string]string) string {
	chaves := make([]string, 0, len(m))
	for k := range m {
		chaves = append(chaves, k)
	}
	sort.Strings(chaves)
	partes := make([]string, len(chaves))
	for i, k := range chaves {
		partes[i] = k + "=" + m[k]
	}
	return strings.Join(partes, " ")
}

// paramsDaLinha turns repeated `--param key=value` into a map.
//
// An entry with no `=` is an ERROR and not a warning: `--param load_full`
// (forgetting the value) would run with the default, and the operator would
// believe the backfill happened.
func paramsDaLinha(entradas []string) (map[string]string, error) {
	if len(entradas) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entradas))
	for _, e := range entradas {
		chave, valor, ok := strings.Cut(e, "=")
		if !ok || strings.TrimSpace(chave) == "" {
			return nil, fmt.Errorf("--param %q: use key=value", e)
		}
		out[strings.TrimSpace(chave)] = valor
	}
	return out, nil
}

// expandir resolves files and directories into a list of YAMLs, in a stable
// order. The order matters: publishing the same folder twice has to produce the
// same log, or the difference between two deploys becomes noise.
func expandir(alvos []string) ([]string, error) {
	var arquivos []string
	for _, alvo := range alvos {
		info, err := os.Stat(alvo)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			arquivos = append(arquivos, alvo)
			continue
		}
		encontrados, err := filepath.Glob(filepath.Join(alvo, "*.y*ml"))
		if err != nil {
			return nil, err
		}
		arquivos = append(arquivos, encontrados...)
	}
	if len(arquivos) == 0 {
		return nil, fmt.Errorf("no .yaml file found in %v", alvos)
	}
	sort.Strings(arquivos)
	return arquivos, nil
}

func cmdValidate() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <file.yaml|directory> ...",
		Short: "Validate workflow files (needs no database)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			arquivos, err := expandir(args)
			if err != nil {
				return err
			}

			var falhas int
			for _, a := range arquivos {
				conteudo, err := os.ReadFile(a)
				if err != nil {
					fmt.Printf("  ERROR %s: %v\n", a, err)
					falhas++
					continue
				}
				w, err := spec.Parse(a, conteudo)
				if err != nil {
					fmt.Printf("  ERROR %v\n", err)
					falhas++
					continue
				}
				fmt.Printf("  ok    %-28s %s  %d steps, %d dependencies%s\n",
					w.Slug, w.Kind, len(w.Nodes), len(w.Edges), agenda(w.Schedule))
			}
			if falhas > 0 {
				return fmt.Errorf("%d of %d file(s) had errors", falhas, len(arquivos))
			}
			return nil
		},
	}
}

// cmdHash generates the password hash that goes into the configuration.
//
// The password is read from the terminal, not from an argument: an argument
// shows up in any process's `ps` on the machine and is written to the shell's
// history.
func cmdHash() *cobra.Command {
	return &cobra.Command{
		Use:   "hash",
		Short: "Generate the BREVIS_AUTH_SENHA_HASH hash (reads the password from the terminal)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Fprint(os.Stderr, "password: ")
			senha, err := lerSenha()
			if err != nil {
				return err
			}
			if len(senha) < 12 {
				return fmt.Errorf("password too short (%d characters); "+
					"use at least 12 — this is the only way into the panel", len(senha))
			}
			h, err := auth.GenerateHash(senha)
			if err != nil {
				return err
			}
			// The hash goes to stdout on its own, so it can be redirected; the
			// labels go to stderr.
			fmt.Fprintln(os.Stderr, "\nBREVIS_AUTH_SENHA_HASH:")
			fmt.Println(h)
			fmt.Fprintln(os.Stderr, "\nStill missing: BREVIS_AUTH_USUARIO and a "+
				"BREVIS_AUTH_SEGREDO of 32+ bytes (openssl rand -base64 48).")
			return nil
		},
	}
}

// lerSenha reads a line without echo when there is a terminal, and from
// standard input when there is not — the second case is a provisioning
// script.
func lerSenha() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		// Redirected input: no terminal on which to turn the echo off.
		linha, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimRight(linha, "\r\n"), err
	}
	return semEco()
}

// cmdBrand validates a brand file without starting the server.
//
// It exists for the same reason `validate` does: today a wrong hex value in
// brand.yaml only shows up when the container starts, and the message arrives
// through the pod's log — far from whoever edited the file. The installation's
// CI calls this and the error comes back in the pull request.
//
// `marca` is kept as an alias: it was the command's name in a released version,
// and a script calling it must not start printing "unknown command".
func cmdBrand() *cobra.Command {
	return &cobra.Command{
		Use:     "brand <brand.yaml>",
		Aliases: []string{"marca"},
		Short:   "Validate a brand file (needs no database)",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			// Load treats absence as "use the default", which is right at
			// boot and wrong here: whoever asked to validate a path expects to
			// be told it does not exist.
			if _, err := os.Stat(args[0]); err != nil {
				return err
			}
			m, err := branding.Load(args[0])
			if err != nil {
				return err
			}
			logo := m.Logo
			if logo == branding.DefaultLogo {
				logo += "  (built-in symbol)"
			}
			fmt.Printf("  ok    %s · %s\n", m.Title, m.Subtitle)
			fmt.Printf("        logo      %s\n", logo)
			fmt.Printf("        accent    %s\n", m.Theme.Accent)
			fmt.Printf("        %s\n", branding.Attribution)
			return nil
		},
	}
}

// cmdRun runs a workflow on the instance itself. No queue, no database, no
// scheduler — the short path the amendment to section 3 enabled.
func cmdRun() *cobra.Command {
	var paramsCrus []string
	var (
		workDir    string
		tentativas int
		timeout    time.Duration
	)
	c := &cobra.Command{
		Use:   "run <file.yaml>",
		Short: "Run a workflow locally",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conteudo, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			w, err := spec.Parse(args[0], conteudo)
			if err != nil {
				return err
			}

			env := os.Getenv("BREVIS_ENV")
			if env == "" {
				env = "local"
			}
			exec, err := local.New(env)
			if err != nil {
				return err
			}

			if workDir == "" {
				workDir = filepath.Dir(args[0])
			}
			informados, err := paramsDaLinha(paramsCrus)
			if err != nil {
				return err
			}
			valores, err := w.Resolver(informados)
			if err != nil {
				return err
			}

			fmt.Printf("workflow %s (%s, %d steps) in %s\n", w.Slug, w.Kind, len(w.Nodes), workDir)
			if len(valores) > 0 {
				fmt.Printf("  params: %s\n\n", emLinha(valores))
			}

			runner := app.Runner{
				Params:   valores,
				Processo: exec,
				// An empty Registry in `run`: Go tasks are registered by
				// whoever compiles the binary, and the generic CLI knows none.
				// An `action:` for an unregistered task fails naming the ones
				// that exist, which is the useful behaviour here.
				Go:            local.NewGoExecutor(execution.NewRegistry()),
				MaxTentativas: tentativas,
				BackoffBase:   time.Second,
				Timeout:       timeout,
				WorkDir:       workDir,
				// PATH and HOME always; beyond that, only what BREVIS_TASK_ENV
				// names. Inheriting the environment would hand the database's
				// credential to every step of every pipeline.
				Env:    config.AmbienteDasTasks(config.TaskEnvDoAmbiente()),
				Report: consoleReporter{},
			}
			if err := runner.Run(cmd.Context(), w); err != nil {
				return err
			}
			fmt.Printf("\nworkflow %s finished\n", w.Slug)
			return nil
		},
	}
	c.Flags().StringVar(&workDir, "workdir", "", "working directory (default: the file's own)")
	c.Flags().StringArrayVar(&paramsCrus, "param", nil,
		"value for a parameter declared in the workflow (key=value; repeatable)")
	c.Flags().IntVar(&tentativas, "retries", 1, "attempts per step (1 = no retry)")
	c.Flags().DurationVar(&timeout, "timeout", 0, "timeout per step (0 = no limit)")
	return c
}

// consoleReporter prints the events prefixed by the step, which is what keeps
// the output readable when several run in parallel at the same level.
type consoleReporter struct{}

func (consoleReporter) Evento(e execution.Event) {
	switch e.Kind {
	case execution.EventStarted:
		fmt.Printf("  ▶ %s\n", e.NodeID)
	case execution.EventLog:
		destino := os.Stdout
		if e.Stream == "stderr" {
			destino = os.Stderr
		}
		_, _ = fmt.Fprintf(destino, "    %s | %s\n", e.NodeID, e.Message)
	case execution.EventSucceeded:
		fmt.Printf("  ✓ %s\n", e.NodeID)
	case execution.EventFailed:
		fmt.Printf("  ✗ %s (%s)\n", e.NodeID, e.Message)
	}
}

func agenda(cron string) string {
	if cron == "" {
		return "  (manual)"
	}
	return "  cron " + cron
}

// abrir builds the pool and the repositories. Repeated in three subcommands; a
// helper keeps them from diverging in how they handle an error.
func abrir(ctx context.Context) (*postgres.Pool, config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, config.Config{}, err
	}
	pool, err := postgres.New(ctx, cfg.DatabaseURL)
	return pool, cfg, err
}

func cmdPublish() *cobra.Command {
	var projeto string
	var podar bool
	c := &cobra.Command{
		Use:   "publish <file.yaml|directory> ...",
		Short: "Publish workflows and their schedules to the database",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pool, _, err := abrir(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()

			// A default project for local mode. Section 4 has Project as a
			// first-class entity; until there is project management, this fixed
			// slug keeps the FK honest without inventing a hierarchy.
			var idProjeto uuid.UUID
			err = pool.QueryRow(ctx, `
				INSERT INTO projects (id, slug, name) VALUES ($1, $2, $2)
				ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
				RETURNING id`, uuid.New(), projeto).Scan(&idProjeto)
			if err != nil {
				return err
			}

			// It takes a directory the way `validate` already did: an
			// installation assembles a FOLDER of workflows, and forcing the
			// caller to expand the glob would leave the command hostage to
			// whoever's shell is calling.
			arquivos, err := expandir(args)
			if err != nil {
				return err
			}

			repo := postgres.NewWorkflowRepo(pool)
			publicados := make([]string, 0, len(arquivos))
			for _, arq := range arquivos {
				conteudo, err := os.ReadFile(arq)
				if err != nil {
					return err
				}
				w, err := spec.Parse(arq, conteudo)
				if err != nil {
					return err
				}
				if err := repo.Publicar(ctx, w, idProjeto); err != nil {
					return err
				}
				fmt.Printf("  published  %-24s %s\n", w.Slug, agenda(w.Schedule))
				publicados = append(publicados, w.Slug)
			}

			if podar {
				removidos, err := repo.Podar(ctx, idProjeto, publicados)
				if err != nil {
					return err
				}
				for _, slug := range removidos {
					fmt.Printf("  removed    %-24s (no longer in the folder)\n", slug)
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&projeto, "project", "default", "the project's slug")
	// Optional and not the default: `publish one-file.yaml` must not delete the
	// project's other 48 just because they were not named on the command line.
	c.Flags().BoolVar(&podar, "prune", false,
		"remove from the project the workflows absent from the published list (history is preserved)")
	return c
}

// executorDePods decides between a pod and a process, once, at boot.
//
// `auto` is the default because the same binary runs in both places: on a
// laptop there is no service account mounted and it falls back to a local
// process; in a cluster there is, and it starts creating pods. `on` exists for
// the deployment that must NOT silently become local execution — there, ending
// up without a cluster has to be a boot error.
func executorDePods(cfg config.Config, log *slog.Logger) (execution.Executor, error) {
	if cfg.Pods.Modo == "off" {
		return nil, nil
	}

	cliente, err := k8s.NoCluster()
	if err != nil {
		var fora k8s.ErrForaDoCluster
		if errors.As(err, &fora) && cfg.Pods.Modo == "auto" {
			log.Info("no cluster: steps with `image:` will run on the instance itself",
				"reason", fora.Motivo)
			return nil, nil
		}
		return nil, fmt.Errorf("BREVIS_PODS=%s: %w", cfg.Pods.Modo, err)
	}

	ns := cfg.Pods.Namespace
	if ns == "" {
		ns = cliente.Namespace()
	}
	log.Info("running steps as pods", "namespace", ns,
		"service_account", cfg.Pods.ServiceAccount)

	return k8s.NewExecutor(cliente, k8s.Opcoes{
		Namespace:         ns,
		ServiceAccount:    cfg.Pods.ServiceAccount,
		PullSecrets:       cfg.Pods.PullSecrets,
		EnvFromSecrets:    cfg.Pods.EnvFromSecrets,
		EnvFromConfigMaps: cfg.Pods.EnvFromConfigMaps,
		SecretsPermitidos: cfg.Pods.SecretsPermitidos,
		CredencialPVC:     cfg.Pods.CredencialPVC,
		CredencialPath:    cfg.Pods.CredencialPath,
		NodeSelector:      cfg.Pods.NodeSelector,
		Tolerations:       toleracoesDoPod(cfg.Pods.Toleracoes),
		ManterPodEmFalha:  cfg.Pods.ManterEmFalha,
	}), nil
}

// toleracoesDoPod translates the configuration into Kubernetes's object.
// `Equal` is the only operator accepted: `Exists` would tolerate ANY taint with
// that key, which is too broad for a decision coming from an environment
// variable.
func toleracoesDoPod(cfg []config.Toleracao) []k8s.Toleracao {
	var out []k8s.Toleracao
	for _, t := range cfg {
		out = append(out, k8s.Toleracao{
			Key: t.Chave, Operator: "Equal", Value: t.Valor, Effect: t.Efeito,
		})
	}
	return out
}

func cmdScheduler() *cobra.Command {
	var intervalo time.Duration
	var concorrencia int
	var maxPods int
	c := &cobra.Command{
		Use:   "scheduler",
		Short: "Materialize schedules into runs and execute them",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			pool, cfg, err := abrir(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()

			log := observability.NewLogger(cfg.Env, cfg.LogLevel)
			runs := postgres.NewRunRepo(pool)
			fila := queue.New(pool.Pool)

			sched := scheduler.NewScheduler(
				postgres.NewScheduleRepo(pool), postgres.NewWorkflowRepo(pool), runs, fila, log,
				scheduler.OpcoesScheduler{Intervalo: intervalo})

			// The dispatcher has to know how to EXECUTE a run. It reads the
			// definition stored on the Run itself — section 22's snapshot — and
			// not the YAML on disk, which may have changed since the trigger.
			//
			// The process executor is OPTIONAL outside local mode. In a cluster
			// every step has an `image:` and becomes a pod; requiring the local
			// executor here made the scheduler refuse to boot in production with
			// "ProcessExecutor only operates with BREVIS_ENV=local" — a guard
			// written for `brevis run` that should never have applied to this
			// path.
			//
			// The variable has the INTERFACE's type, and not the concrete
			// pointer's: assigning a nil `*ProcessExecutor` to an interface
			// produces a NON-nil interface, and the runner would call a method
			// on a null pointer instead of reporting "no process executor
			// configured".
			var processo execution.Executor
			if exec, err := local.New(cfg.Env); err == nil {
				processo = exec
			} else {
				var fora local.ErrForaDoLocal
				if !errors.As(err, &fora) {
					return err
				}
				// A step with no `image:` fails saying so, and only that step —
				// not the whole scheduler.
				log.Info("no process executor; every step needs an `image:`")
			}
			// The pod executor: in a cluster, every step with an `image:`
			// becomes a pod of its own. Outside a cluster, `pods` stays nil and
			// everything runs as a local process — the SAME YAML in both
			// cases.
			pods, err := executorDePods(cfg, log)
			if err != nil {
				return err
			}

			// Settled once, at boot: the process's environment does not change,
			// and re-reading it per run would only multiply system calls.
			//
			// The POD ceiling. Shared by every run in this process: with ten
			// steps ready and five slots, five run and the rest go in as slots
			// open up.
			//
			// Separate from --concurrency on purpose: that one counts RUNS, this
			// one counts STEPS. Five runs with three parallel steps each would
			// mean fifteen pods if the only limit were the run limit.
			vagas := make(chan struct{}, maxPods)

			ambienteDasTasks := config.AmbienteDasTasks(cfg.TaskEnv)
			// In pod mode the task's environment comes from the cluster's
			// Secrets (BREVIS_POD_ENV_FROM_SECRETS), not from here. Warning
			// anyway sent the operator looking for a problem that does not
			// exist.
			if len(ambienteDasTasks) <= 2 && pods == nil {
				// Only PATH and HOME. A `dbt` here fails with "Env var required
				// but not provided", which does not point at the cause — saying
				// this at boot saves the investigation.
				log.Warn("tasks get only PATH and HOME",
					"hint", "declare what they need in BREVIS_TASK_ENV (e.g. GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE)")
			}

			executar := func(ctx context.Context, id uuid.UUID) error {
				r, err := runs.Buscar(ctx, id)
				if err != nil {
					return err
				}
				var w wfdom.Workflow
				if err := json.Unmarshal(r.Definition, &w); err != nil {
					return err
				}
				return app.Runner{
					// The RUN's params, not the workflow's: it is the snapshot
					// of that execution's input. They go into the command
					// through the template and into the step's environment, so
					// a fetcher using the SDK sees them without being passed
					// anything as an argument.
					Params:   r.Params,
					Processo: processo,
					Pods:     pods,
					Go:       local.NewGoExecutor(execution.NewRegistry()),
					Env:      ambienteDasTasks,
					Report:   consoleReporter{},
					// Without this the `task_runs` table stays empty and the DAG
					// on screen has no per-step state — the debt left open in
					// PHASE 2.
					Persist: runs,
					RunID:   id,
					// The RUN's attempt goes into the pod's name. Without it,
					// the dispatcher's retry restarts the run from zero — the
					// step at attempt 0 again — and runs into the previous
					// attempt's pod, which may be stuck in Pending forever.
					TentativaDoRun: r.Attempt,
					Vagas:          vagas,

					// What the step has no way of knowing and the engine does.
					// It goes into the step's environment as BREVIS_RUN_*, and
					// the SDK uses it to decide, among other things, whether to
					// create the destination table on the first run.
					//
					// Without Historico the answer is always "it is not the
					// first": creating a table without being sure is worse than
					// not creating it.
					Historico:   runs,
					Trigger:     r.TriggerType,
					LogicalDate: r.LogicalDate,
				}.Run(ctx, w)
			}

			disp := scheduler.New(scheduler.Config{
				Worker: "local", MaxConcorrente: concorrencia,
			}, fila, runs, executar, log)
			if cfg.SlackWebhook != "" {
				disp.Alertas = notify.NovoSlack(cfg.SlackWebhook, cfg.Env)
				disp.URLBase = cfg.UIURL
				log.Info("failure alerting is on", "destination", "slack")
			} else {
				// Said at boot, once: an installation that fails in silence
				// tends to be discovered by the customer, not by the team.
				log.Warn("no BREVIS_SLACK_WEBHOOK: failures will not be announced")
			}

			log.Info("scheduler and dispatcher are up",
				"interval", intervalo.String(), "concurrency", concorrencia)

			// The two loops run together, and independently: the scheduler
			// CREATES, the dispatcher EXECUTES. It is the separation section 37
			// requires — one can go down without interrupting the other.
			erros := make(chan error, 2)
			go func() { erros <- sched.Run(ctx) }()
			go func() { erros <- disp.Run(ctx) }()

			<-ctx.Done()
			log.Info("shutting down")
			return <-erros
		},
	}
	c.Flags().DurationVar(&intervalo, "interval", 10*time.Second, "interval between cycles")
	c.Flags().IntVar(&concorrencia, "concurrency", 5, "simultaneous runs")
	c.Flags().IntVar(&maxPods, "max-pods", 5,
		"simultaneous steps in total (in Kubernetes, the cluster's pod ceiling)")
	return c
}

func cmdBackfill() *cobra.Command {
	var paramsCrus []string
	var de, ate string
	c := &cobra.Command{
		Use:   "backfill <workflow>",
		Short: "Materialize a workflow's past slots",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			inicio, err := time.Parse("2006-01-02", de)
			if err != nil {
				return fmt.Errorf("--from: %w (use YYYY-MM-DD)", err)
			}
			fim, err := time.Parse("2006-01-02", ate)
			if err != nil {
				return fmt.Errorf("--to: %w (use YYYY-MM-DD)", err)
			}
			// End of day: `--to 2026-01-31` has to include the whole 31st.
			fim = fim.Add(24*time.Hour - time.Second)

			pool, cfg, err := abrir(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()

			s := scheduler.NewScheduler(
				postgres.NewScheduleRepo(pool), postgres.NewWorkflowRepo(pool),
				postgres.NewRunRepo(pool), queue.New(pool.Pool),
				observability.NewLogger(cfg.Env, cfg.LogLevel), scheduler.OpcoesScheduler{})

			informados, err := paramsDaLinha(paramsCrus)
			if err != nil {
				return err
			}
			n, err := s.Backfill(ctx, args[0], inicio, fim, informados)
			if err != nil {
				return err
			}
			fmt.Printf("  %d backfill run(s) queued for %s (%s to %s)\n",
				n, args[0], de, ate)
			fmt.Println("  run `brevis scheduler` to execute them")
			return nil
		},
	}
	c.Flags().StringVar(&de, "from", "", "start date (YYYY-MM-DD)")
	c.Flags().StringVar(&ate, "to", "", "end date (YYYY-MM-DD)")
	// The backfill's central use case: "reprocess the whole of January with
	// load_full=true". The values apply to every slot in the range.
	c.Flags().StringArrayVar(&paramsCrus, "param", nil,
		"value for a workflow parameter (key=value; repeatable)")
	_ = c.MarkFlagRequired("from")
	_ = c.MarkFlagRequired("to")
	return c
}

// acoesDaUI wires the screen's two effects — pause a schedule and run now — to
// the components that already implement them. It exists to keep the `api.Actions`
// interface small: the UI must not be able to do anything else to the system.
type acoesDaUI struct {
	agendas *postgres.ScheduleRepo
	sched   *scheduler.Scheduler
}

func (a acoesDaUI) Alternar(ctx context.Context, slug string) (bool, error) {
	return a.agendas.Alternar(ctx, slug)
}

func (a acoesDaUI) Disparar(ctx context.Context, slug string, agora time.Time,
	params map[string]string) (uuid.UUID, error) {
	return a.sched.Disparar(ctx, slug, agora, params)
}

func serve(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := observability.NewLogger(cfg.Env, cfg.LogLevel)

	// Shuts down on SIGINT/SIGTERM. Without this, a deploy cuts requests in
	// flight.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// The UI gets no path of its own for creating a Run: it calls the SAME
	// scheduler, so that section 37's rule ("the scheduler creates runs") keeps
	// having a single owner. Here it is used without the loop — no schedule is
	// materialized by this process, only the manual trigger.
	agendas := postgres.NewScheduleRepo(pool)
	runsRepo := postgres.NewRunRepo(pool)
	sched := scheduler.NewScheduler(agendas, postgres.NewWorkflowRepo(pool), runsRepo,
		queue.New(pool.Pool), log, scheduler.OpcoesScheduler{})

	// The visual identity is optional: with no file, the installation uses the
	// default one. An error HERE is about content (an invalid colour, broken
	// YAML) and does not stop the interface from starting — taking the API down
	// over a colour would be worse than serving it with the default theme and a
	// warning in the log.
	marca, err := branding.Load(cfg.BrandFile)
	if err != nil {
		log.Warn("visual identity ignored", "file", cfg.BrandFile, "error", err)
	} else if marca.Title != branding.Default().Title {
		log.Info("visual identity loaded", "file", cfg.BrandFile, "title", marca.Title)
	}

	ui := api.NewUI(postgres.NewReadRepo(pool), postgres.NewWorkflowRepo(pool),
		runsRepo, acoesDaUI{agendas: agendas, sched: sched}, marca, log)
	// `inseguro` follows the environment: locally the server listens on plain
	// http, and a Secure cookie would never come back — the login would look
	// broken.
	srv := api.NewServerAutenticado(log, map[string]api.Checker{"postgres": pool}, ui,
		cfg.Auth, cfg.Env == "local").HTTPServer(cfg.HTTPAddr)
	if cfg.Auth.Enabled() {
		log.Info("interface is protected", "user", cfg.Auth.User)
	} else {
		log.Warn("interface is OPEN: anyone can trigger a workflow",
			"hint", "set BREVIS_AUTH_USUARIO and BREVIS_AUTH_SENHA_HASH")
	}

	erros := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			erros <- err
		}
	}()

	select {
	case err := <-erros:
		return err
	case <-ctx.Done():
		log.Info("shutting down", "timeout", cfg.ShutdownTimeout.String())
	}

	// A context of its own: the one above is already cancelled by the signal,
	// and using it here would abort the shutdown the instant it starts.
	ctxEnc, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return srv.Shutdown(ctxEnc)
}
