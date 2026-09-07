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

	"github.com/AreteAcademy/brevis/internal/alerts"
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
	"github.com/AreteAcademy/brevis/internal/observability/metrics"
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
		cmdScheduler(), cmdAlert(), cmdBackfill(), cmdVersion())
	return c
}

// Version is stamped at build time (-ldflags). "dev" is the value for whoever
// built straight with `go build`, and telling that apart from a release
// artifact matters when somebody reports odd behaviour.
var (
	Version   = "dev"
	Commit    = ""
	BuildDate = ""
)

func cmdVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the binary's version",
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Printf("brevis %s\n", Version)
			if Commit != "" {
				fmt.Printf("  commit  %s\n", Commit)
			}
			if BuildDate != "" {
				fmt.Printf("  build   %s\n", BuildDate)
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
func inline(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	partes := make([]string, len(keys))
	for i, k := range keys {
		partes[i] = k + "=" + m[k]
	}
	return strings.Join(partes, " ")
}

// paramsFromFlags turns repeated `--param key=value` into a map.
//
// An entry with no `=` is an ERROR and not a warning: `--param load_full`
// (forgetting the value) would run with the default, and the operator would
// believe the backfill happened.
func paramsFromFlags(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		key, value, ok := strings.Cut(e, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("--param %q: use key=value", e)
		}
		out[strings.TrimSpace(key)] = value
	}
	return out, nil
}

// expandir resolves files and directories into a list of YAMLs, in a stable
// order. The order matters: publishing the same folder twice has to produce the
// same log, or the difference between two deploys becomes noise.
func expandir(alvos []string) ([]string, error) {
	var files []string
	for _, target := range alvos {
		info, err := os.Stat(target)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, target)
			continue
		}
		found, err := filepath.Glob(filepath.Join(target, "*.y*ml"))
		if err != nil {
			return nil, err
		}
		files = append(files, found...)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .yaml file found in %v", alvos)
	}
	sort.Strings(files)
	return files, nil
}

func cmdValidate() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <file.yaml|directory> ...",
		Short: "Validate workflow files (needs no database)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			files, err := expandir(args)
			if err != nil {
				return err
			}

			var failures1 int
			for _, a := range files {
				conteudo, err := os.ReadFile(a)
				if err != nil {
					fmt.Printf("  ERROR %s: %v\n", a, err)
					failures1++
					continue
				}
				w, err := spec.Parse(a, conteudo)
				if err != nil {
					fmt.Printf("  ERROR %v\n", err)
					failures1++
					continue
				}
				fmt.Printf("  ok    %-28s %s  %d steps, %d dependencies%s\n",
					w.Slug, w.Kind, len(w.Nodes), len(w.Edges), schedule(w.Schedule))
			}
			if failures1 > 0 {
				return fmt.Errorf("%d of %d file(s) had errors", failures1, len(files))
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
			password, err := readPassword()
			if err != nil {
				return err
			}
			if len(password) < 12 {
				return fmt.Errorf("password too short (%d characters); "+
					"use at least 12 — this is the only way into the panel", len(password))
			}
			h, err := auth.GenerateHash(password)
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

// readPassword reads a line without echo when there is a terminal, and from
// standard input when there is not — the second case is a provisioning
// script.
func readPassword() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		// Redirected input: no terminal on which to turn the echo off.
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
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
	var rawParams []string
	var (
		workDir  string
		attempts int
		timeout  time.Duration
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
			given, err := paramsFromFlags(rawParams)
			if err != nil {
				return err
			}
			values, err := w.Resolver(given)
			if err != nil {
				return err
			}

			fmt.Printf("workflow %s (%s, %d steps) in %s\n", w.Slug, w.Kind, len(w.Nodes), workDir)
			if len(values) > 0 {
				fmt.Printf("  params: %s\n\n", inline(values))
			}

			runner := app.Runner{
				Params:   values,
				Processo: exec,
				// An empty Registry in `run`: Go tasks are registered by
				// whoever compiles the binary, and the generic CLI knows none.
				// An `action:` for an unregistered task fails naming the ones
				// that exist, which is the useful behaviour here.
				Go:          local.NewGoExecutor(execution.NewRegistry()),
				MaxAttempts: attempts,
				BackoffBase: time.Second,
				Timeout:     timeout,
				WorkDir:     workDir,
				// PATH and HOME always; beyond that, only what BREVIS_TASK_ENV
				// names. Inheriting the environment would hand the database's
				// credential to every step of every pipeline.
				Env:    config.TasksEnvironment(config.TaskEnvFromEnvironment()),
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
	c.Flags().StringArrayVar(&rawParams, "param", nil,
		"value for a parameter declared in the workflow (key=value; repeatable)")
	c.Flags().IntVar(&attempts, "retries", 1, "attempts per step (1 = no retry)")
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
		target := os.Stdout
		if e.Stream == "stderr" {
			target = os.Stderr
		}
		_, _ = fmt.Fprintf(target, "    %s | %s\n", e.NodeID, e.Message)
	case execution.EventSucceeded:
		fmt.Printf("  ✓ %s\n", e.NodeID)
	case execution.EventFailed:
		fmt.Printf("  ✗ %s (%s)\n", e.NodeID, e.Message)
	}
}

func schedule(cron string) string {
	if cron == "" {
		return "  (manual)"
	}
	return "  cron " + cron
}

// abrir builds the pool and the repositories. Repeated in three subcommands; a
// helper keeps them from diverging in how they handle an error.
func open(ctx context.Context) (*postgres.Pool, config.Config, error) {
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
			pool, _, err := open(ctx)
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
			files, err := expandir(args)
			if err != nil {
				return err
			}

			repo := postgres.NewWorkflowRepo(pool)
			published := make([]string, 0, len(files))
			for _, arq := range files {
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
				fmt.Printf("  published  %-24s %s\n", w.Slug, schedule(w.Schedule))
				published = append(published, w.Slug)
			}

			if podar {
				removed, err := repo.Podar(ctx, idProjeto, published)
				if err != nil {
					return err
				}
				for _, slug := range removed {
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

	client, err := k8s.NoCluster()
	if err != nil {
		var outside k8s.ErrOutsideCluster
		if errors.As(err, &outside) && cfg.Pods.Modo == "auto" {
			log.Info("no cluster: steps with `image:` will run on the instance itself",
				"reason", outside.Reason)
			return nil, nil
		}
		return nil, fmt.Errorf("BREVIS_PODS=%s: %w", cfg.Pods.Modo, err)
	}

	ns := cfg.Pods.Namespace
	if ns == "" {
		ns = client.Namespace()
	}
	log.Info("running steps as pods", "namespace", ns,
		"service_account", cfg.Pods.ServiceAccount)

	return k8s.NewExecutor(client, k8s.Options{
		Namespace:         ns,
		ServiceAccount:    cfg.Pods.ServiceAccount,
		PullSecrets:       cfg.Pods.PullSecrets,
		EnvFromSecrets:    cfg.Pods.EnvFromSecrets,
		EnvFromConfigMaps: cfg.Pods.EnvFromConfigMaps,
		AllowedSecrets:    cfg.Pods.AllowedSecrets,
		CredentialPVC:     cfg.Pods.CredentialPVC,
		CredentialPath:    cfg.Pods.CredentialPath,
		NodeSelector:      cfg.Pods.NodeSelector,
		Tolerations:       podTolerations(cfg.Pods.Tolerations),
		KeepFailedPod:     cfg.Pods.KeepOnFailure,
	}), nil
}

// podTolerations translates the configuration into Kubernetes's object.
// `Equal` is the only operator accepted: `Exists` would tolerate ANY taint with
// that key, which is too broad for a decision coming from an environment
// variable.
func podTolerations(cfg []config.Grace) []k8s.Grace {
	var out []k8s.Grace
	for _, t := range cfg {
		out = append(out, k8s.Grace{
			Key: t.Key, Operator: "Equal", Value: t.Value, Effect: t.Efeito,
		})
	}
	return out
}

func cmdScheduler() *cobra.Command {
	var intervalo time.Duration
	var concurrency int
	var maxPods int
	c := &cobra.Command{
		Use:   "scheduler",
		Short: "Materialize schedules into runs and execute them",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			pool, cfg, err := open(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()

			log := observability.NewLogger(cfg.Env, cfg.LogLevel)
			runs := postgres.NewRunRepo(pool)
			q := queue.New(pool.Pool)

			// Declared here because `executar` below closes over it: the RUNNER
			// records the per-step numbers, and it is built once per run inside
			// that closure.
			met := metrics.New()

			sched := scheduler.NewScheduler(
				postgres.NewScheduleRepo(pool), postgres.NewWorkflowRepo(pool), runs, q, log,
				scheduler.SchedulerOptions{Interval: intervalo})

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
				var outside local.ErrOutsideLocal
				if !errors.As(err, &outside) {
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
			slots := make(chan struct{}, maxPods)

			tasksEnvironment := config.TasksEnvironment(cfg.TaskEnv)
			// In pod mode the task's environment comes from the cluster's
			// Secrets (BREVIS_POD_ENV_FROM_SECRETS), not from here. Warning
			// anyway sent the operator looking for a problem that does not
			// exist.
			if len(tasksEnvironment) <= 2 && pods == nil {
				// Only PATH and HOME. A `dbt` here fails with "Env var required
				// but not provided", which does not point at the cause — saying
				// this at boot saves the investigation.
				log.Warn("tasks get only PATH and HOME",
					"hint", "declare what they need in BREVIS_TASK_ENV (e.g. GOOGLE_PROJECT_ID,STAGE,DBT_KEYFILE)")
			}

			executar := func(ctx context.Context, id uuid.UUID) error {
				r, err := runs.Get(ctx, id)
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
					Env:      tasksEnvironment,
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
					RunAttempt: r.Attempt,
					Slots:      slots,

					// What the step has no way of knowing and the engine does.
					// It goes into the step's environment as BREVIS_RUN_*, and
					// the SDK uses it to decide, among other things, whether to
					// create the destination table on the first run.
					//
					// Without Historico the answer is always "it is not the
					// first": creating a table without being sure is worse than
					// not creating it.
					History:     runs,
					Trigger:     r.TriggerType,
					LogicalDate: r.LogicalDate,
					Metrics:     met,
				}.Run(ctx, w)
			}

			disp := scheduler.New(scheduler.Config{
				Worker: "local", MaxConcorrente: concurrency,
			}, q, runs, executar, log)
			// The dispatcher no longer TALKS to Slack. It writes a row in the
			// same transaction as the failure, and `brevis alert` delivers it.
			//
			// The webhook is still what decides whether alerting is on, and it
			// is read HERE rather than only in the alert pod on purpose: an
			// outbox filling with alerts nothing can deliver would look like a
			// backlog instead of like a setting nobody turned on.
			if cfg.SlackWebhook != "" {
				disp.Channel = alerts.ChannelSlack
				disp.BaseURL = cfg.UIURL
				// Said at boot, once, and naming the OTHER process: an
				// installation that upgrades the scheduler without deploying
				// `brevis alert` records every alert correctly and delivers
				// none of them. The outbox looks healthy; the channel is
				// silent. That is a worse failure than the one this replaced,
				// and one line here is what makes it findable.
				log.Info("failure alerting is on", "channel", alerts.ChannelSlack,
					"delivered_by", "brevis alert",
					"warning", "alerts are only sent while `brevis alert` is running")
			} else {
				// Said at boot, once: an installation that fails in silence
				// tends to be discovered by the customer, not by the team.
				log.Warn("no BREVIS_SLACK_WEBHOOK: failures will not be announced")
			}

			// The scheduler is where the interesting numbers live: queue
			// depth, claim latency, slots in use and orphans recovered are all
			// properties of the process that RUNS steps, not of the one that
			// serves the UI. Until now this process had no HTTP server at all,
			// so none of them could leave it.
			if err := met.WatchQueue(q.Size); err != nil {
				log.Warn("queue depth will not be reported", "error", err)
			}
			if err := met.WatchSlots(func() (int, int) {
				return disp.EmVoo(), concurrency
			}); err != nil {
				log.Warn("slot usage will not be reported", "error", err)
			}
			disp.Metrics = met
			met.Serve(ctx, cfg.MetricsAddr, log)

			log.Info("scheduler and dispatcher are up",
				"interval", intervalo.String(), "concurrency", concurrency)

			// The two loops run together, and independently: the scheduler
			// CREATES, the dispatcher EXECUTES. It is the separation section 37
			// requires — one can go down without interrupting the other.
			failures := make(chan error, 2)
			go func() { failures <- sched.Run(ctx) }()
			go func() { failures <- disp.Run(ctx) }()

			<-ctx.Done()
			log.Info("shutting down")
			return <-failures
		},
	}
	c.Flags().DurationVar(&intervalo, "interval", 10*time.Second, "interval between cycles")
	c.Flags().IntVar(&concurrency, "concurrency", 5, "simultaneous runs")
	c.Flags().IntVar(&maxPods, "max-pods", 5,
		"simultaneous steps in total (in Kubernetes, the cluster's pod ceiling)")
	return c
}

// cmdAlert is the third role of the same binary, beside serve and scheduler.
//
// It exists because of where an alert is WRITTEN, not because delivery deserves
// its own process. The dispatcher records the alert in the same transaction as
// the failure; something else has to drain that table, and it must be able to
// retry across a Slack outage and across its own restart. A goroutine inside
// the scheduler could do neither without becoming this.
//
// What it does NOT buy is independent scaling. Alert volume is a function of
// failures, and if failures are high enough to need a second alert pod, the
// alerts are not the problem.
func cmdAlert() *cobra.Command {
	var interval time.Duration
	var attempts int
	c := &cobra.Command{
		Use:   "alert",
		Short: "Deliver the alerts the scheduler recorded",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			pool, cfg, err := open(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()

			log := observability.NewLogger(cfg.Env, cfg.LogLevel)
			outbox := alerts.New(pool.Pool)

			// The map is what makes an unknown channel a REFUSAL rather than a
			// silent no-op: a row naming something absent here is marked
			// undelivered with the valid names in its error, instead of being
			// retried forever or dropped.
			channels := map[string]notify.Notificador{}
			if cfg.SlackWebhook != "" {
				channels[alerts.ChannelSlack] = notify.NovoSlack(cfg.SlackWebhook, cfg.Env)
			}
			if len(channels) == 0 {
				// Refused at boot rather than warned about. A delivery process
				// with nothing to deliver to is a process that drains the
				// outbox into "undelivered" as fast as it fills -- worse than
				// not running it, because the rows come out looking like
				// Slack rejected them.
				return fmt.Errorf("no channel is configured: set BREVIS_SLACK_WEBHOOK")
			}

			met := metrics.New()
			if err := met.WatchAlerts(outbox.Pending); err != nil {
				log.Warn("the outbox depth will not be reported", "error", err)
			}
			met.Serve(ctx, cfg.MetricsAddr, log)

			d := alerts.NewDeliverer(alerts.Config{
				Worker: "alert", Interval: interval, MaxAttempts: attempts,
			}, outbox, channels, log)
			d.Metrics = met

			log.Info("alert delivery is up",
				"interval", interval.String(), "max_attempts", attempts,
				"channels", alerts.Channels())
			return d.Run(ctx)
		},
	}
	c.Flags().DurationVar(&interval, "interval", time.Second, "interval between delivery cycles")
	c.Flags().IntVar(&attempts, "max-attempts", 6,
		"tries before an alert is recorded as undelivered")
	return c
}

func cmdBackfill() *cobra.Command {
	var rawParams []string
	var de, until string
	c := &cobra.Command{
		Use:   "backfill <workflow>",
		Short: "Materialize a workflow's past slots",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			start, err := time.Parse("2006-01-02", de)
			if err != nil {
				return fmt.Errorf("--from: %w (use YYYY-MM-DD)", err)
			}
			end, err := time.Parse("2006-01-02", until)
			if err != nil {
				return fmt.Errorf("--to: %w (use YYYY-MM-DD)", err)
			}
			// End of day: `--to 2026-01-31` has to include the whole 31st.
			end = end.Add(24*time.Hour - time.Second)

			pool, cfg, err := open(ctx)
			if err != nil {
				return err
			}
			defer pool.Close()

			s := scheduler.NewScheduler(
				postgres.NewScheduleRepo(pool), postgres.NewWorkflowRepo(pool),
				postgres.NewRunRepo(pool), queue.New(pool.Pool),
				observability.NewLogger(cfg.Env, cfg.LogLevel), scheduler.SchedulerOptions{})

			given, err := paramsFromFlags(rawParams)
			if err != nil {
				return err
			}
			n, err := s.Backfill(ctx, args[0], start, end, given)
			if err != nil {
				return err
			}
			fmt.Printf("  %d backfill run(s) queued for %s (%s to %s)\n",
				n, args[0], de, until)
			fmt.Println("  run `brevis scheduler` to execute them")
			return nil
		},
	}
	c.Flags().StringVar(&de, "from", "", "start date (YYYY-MM-DD)")
	c.Flags().StringVar(&until, "to", "", "end date (YYYY-MM-DD)")
	// The backfill's central use case: "reprocess the whole of January with
	// load_full=true". The values apply to every slot in the range.
	c.Flags().StringArrayVar(&rawParams, "param", nil,
		"value for a workflow parameter (key=value; repeatable)")
	_ = c.MarkFlagRequired("from")
	_ = c.MarkFlagRequired("to")
	return c
}

// uiActions wires the screen's two effects — pause a schedule and run now — to
// the components that already implement them. It exists to keep the `api.Actions`
// interface small: the UI must not be able to do anything else to the system.
type uiActions struct {
	schedules *postgres.ScheduleRepo
	sched     *scheduler.Scheduler
}

func (a uiActions) Toggle(ctx context.Context, slug string) (bool, error) {
	return a.schedules.Toggle(ctx, slug)
}

func (a uiActions) Disparar(ctx context.Context, slug string, now time.Time,
	params map[string]string) (uuid.UUID, error) {
	return a.sched.Disparar(ctx, slug, now, params)
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
	schedules := postgres.NewScheduleRepo(pool)
	runsRepo := postgres.NewRunRepo(pool)
	sched := scheduler.NewScheduler(schedules, postgres.NewWorkflowRepo(pool), runsRepo,
		queue.New(pool.Pool), log, scheduler.SchedulerOptions{})

	// The visual identity is optional: with no file, the installation uses the
	// default one. An error HERE is about content (an invalid colour, broken
	// YAML) and does not stop the interface from starting — taking the API down
	// over a colour would be worse than serving it with the default theme and a
	// warning in the log.
	brand, err := branding.Load(cfg.BrandFile)
	if err != nil {
		log.Warn("visual identity ignored", "file", cfg.BrandFile, "error", err)
	} else if brand.Title != branding.Default().Title {
		log.Info("visual identity loaded", "file", cfg.BrandFile, "title", brand.Title)
	}

	// Metrics listen on an address of their own, never on cfg.HTTPAddr: that
	// port is the one behind the Ingress and behind auth.Gate, and a scrape
	// endpoint there would either need a session -- which no scraper has -- or
	// publish every workflow name to the internet.
	//
	// The API's own numbers are the queue's: this process does not execute
	// anything, so what it can honestly report is the state of the TABLE, read
	// at scrape time.
	met := metrics.New()
	if err := met.WatchQueue(queue.New(pool.Pool).Size); err != nil {
		log.Warn("queue depth will not be reported", "error", err)
	}
	met.Serve(ctx, cfg.MetricsAddr, log)

	ui := api.NewUI(postgres.NewReadRepo(pool), postgres.NewWorkflowRepo(pool),
		runsRepo, uiActions{schedules: schedules, sched: sched},
		alerts.New(pool.Pool), brand, log)
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

	failures := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failures <- err
		}
	}()

	select {
	case err := <-failures:
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
