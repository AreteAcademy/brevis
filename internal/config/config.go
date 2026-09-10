// Package config loads and validates the process's configuration from the
// environment.
//
// It sits outside the engine's original package tree, which did not foresee a
// package for this. The alternative was scattering os.Getenv across cmd/ and
// infrastructure/; a single point of reading and validation is worth the
// detour, and rule 7 asks that decisions like this one be explicit rather than
// silent.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/auth"
)

// Config is the immutable state derived from the environment. Everything the
// process needs to start is here -- nothing reads the environment after boot.
type Config struct {
	Env             string
	HTTPAddr        string
	DatabaseURL     string
	LogLevel        string
	ShutdownTimeout time.Duration

	// BrandFile points at the visual identity YAML. Optional: without it the
	// interface uses the default identity.
	BrandFile string

	// MetricsAddr is where /metrics listens, and it is a SEPARATE address from
	// HTTPAddr on purpose. The API pod is the one behind an Ingress, and a
	// scrape endpoint on the same port would either sit behind the login --
	// where no scraper can reach it -- or publish every workflow and step name
	// to whoever finds the path.
	//
	// It defaults to :9090 rather than to empty, because a metrics endpoint
	// that has to be turned on is a metrics endpoint nobody has. Setting
	// BREVIS_METRICS_ADDR to the empty string serves nothing -- which is why it
	// is read with LookupEnv and not with `get`, where empty and unset are the
	// same thing.
	MetricsAddr string

	// TaskEnv lists what the process passes on to local tasks. See
	// TasksEnvironment -- the default does NOT inherit the environment, and
	// that is deliberate.
	TaskEnv []string

	// Hosts are the machines the engine does not manage but may dispatch to,
	// as `name=https://agent:9443` pairs. A step's `host:` names one of them.
	//
	// `BREVIS_HOSTS=dlt-runner-01=https://10.0.3.7:9443,gpu-01=https://10.0.3.9:9443`
	//
	// It is an INSTALLATION-level list for the same reason AllowedSecrets is:
	// the file that names a host is written by somebody else, and a workflow
	// that could dispatch to an arbitrary address would be dispatching this
	// engine's credentials to it. Empty means no step may use `host:`, and the
	// refusal says so with this variable's name in it.
	Hosts map[string]string

	// HostToken authenticates the engine to every agent.
	//
	// One token for all of them, and the gap is stated rather than discovered:
	// no revoking one host without changing every host, and no per-host
	// identity in the audit trail. It is a first version, and the alternative
	// -- per-host credentials -- is a key-management problem this engine does
	// not have anywhere else yet.
	HostToken string

	// SlackWebhook receives the alert for a definitive failure. Empty means
	// nobody is told. It comes from the environment and never from the YAML:
	// whoever holds the URL posts in the channel as if they were the
	// platform.
	SlackWebhook string

	// Auth is the operator credential that closes the interface. See internal/auth.
	Auth auth.Credential

	// UIURL builds the run's link in the alert. Without it the alert says what
	// failed but makes the reader hunt for the run by hand.
	UIURL string

	// Pods parameterises execution in Kubernetes. These are the INSTALLATION's
	// decisions -- which identity and credentials the pods start with -- which
	// is why they come from the environment and not the workflow's YAML: a
	// pipeline must not get to pick the service account it runs as.
	Pods PodsConfig
}

type PodsConfig struct {
	// Mode: auto (use pods when a cluster is there), on (require) or off (never).
	Modo              string
	Namespace         string
	ServiceAccount    string
	PullSecrets       []string
	EnvFromSecrets    []string
	EnvFromConfigMaps []string

	// AllowedSecrets limits what a YAML's `secrets:` may name. Empty denies
	// everything: the installation decides which secrets exist for workflows,
	// and the YAML decides which step receives each one.
	AllowedSecrets []string

	// CredencialPVC and CredencialPath mount the volume where the SDK keeps the
	// credential it rotates. Without the PVC, nothing changes.
	CredentialPVC  string
	CredentialPath string
	NodeSelector   map[string]string
	// Tolerations in "key=value:effect" form, comma separated. An arm64 pool
	// commonly carries a taint, and without a toleration the task's pod stays
	// Pending forever -- no error, just stopped.
	Tolerations   []Grace
	KeepOnFailure bool
}

// Toleracao mirrors the pod's field, without importing Kubernetes' type.
type Grace struct {
	Key    string
	Value  string
	Efeito string
}

// Load builds the Config and fails at boot if something required is missing.
// Failing early is deliberate: a process that starts without DATABASE_URL only
// finds out on the first request, and by then readiness has already lied to the
// orchestrator.
func Load() (Config, error) {
	c := Config{
		Env:          get("BREVIS_ENV", "local"),
		HTTPAddr:     get("BREVIS_HTTP_ADDR", ":8080"),
		DatabaseURL:  os.Getenv("BREVIS_DATABASE_URL"),
		LogLevel:     get("BREVIS_LOG_LEVEL", "info"),
		BrandFile:    get("BREVIS_BRAND_FILE", "brand.yaml"),
		MetricsAddr:  optional("BREVIS_METRICS_ADDR", ":9090"),
		TaskEnv:      list("BREVIS_TASK_ENV"),
		SlackWebhook: os.Getenv("BREVIS_SLACK_WEBHOOK"),
		UIURL:        os.Getenv("BREVIS_UI_URL"),
		Auth: auth.Credential{
			User:   renamed("BREVIS_AUTH_USER", "BREVIS_AUTH_USUARIO"),
			Hash:   renamed("BREVIS_AUTH_PASSWORD_HASH", "BREVIS_AUTH_SENHA_HASH"),
			Secret: []byte(renamed("BREVIS_AUTH_SECRET", "BREVIS_AUTH_SEGREDO")),
		},
		Pods: PodsConfig{
			Modo:              get("BREVIS_PODS", "auto"),
			Namespace:         os.Getenv("BREVIS_POD_NAMESPACE"),
			ServiceAccount:    os.Getenv("BREVIS_POD_SERVICE_ACCOUNT"),
			PullSecrets:       list("BREVIS_POD_PULL_SECRETS"),
			EnvFromSecrets:    list("BREVIS_POD_ENV_FROM_SECRETS"),
			EnvFromConfigMaps: list("BREVIS_POD_ENV_FROM_CONFIGMAPS"),
			AllowedSecrets:    list("BREVIS_POD_ALLOWED_SECRETS"),
			CredentialPVC:     get("BREVIS_POD_CREDENTIAL_PVC", ""),
			CredentialPath:    get("BREVIS_POD_CREDENTIAL_PATH", ""),
			NodeSelector:      pares("BREVIS_POD_NODE_SELECTOR"),
			Tolerations:       graces("BREVIS_POD_TOLERATIONS"),
			KeepOnFailure:     renamed("BREVIS_POD_KEEP_ON_FAILURE", "BREVIS_POD_MANTER_EM_FALHA") == "true",
		},
		Hosts:           pares("BREVIS_HOSTS"),
		HostToken:       os.Getenv("BREVIS_HOST_TOKEN"),
		ShutdownTimeout: 15 * time.Second,
	}

	if v := os.Getenv("BREVIS_SHUTDOWN_TIMEOUT_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("BREVIS_SHUTDOWN_TIMEOUT_SECONDS: %q is not an integer", v)
		}
		c.ShutdownTimeout = time.Duration(n) * time.Second
	}

	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("BREVIS_DATABASE_URL is required")
	}
	switch c.Pods.Modo {
	case "auto", "on", "off":
	default:
		return Config{}, fmt.Errorf("BREVIS_PODS: %q is not valid (auto, on or off)", c.Pods.Modo)
	}
	if err := c.Auth.Validate(); err != nil {
		return Config{}, err
	}
	// Outside local, starting without a credential is refused.
	//
	// The interface triggers pipelines: a POST to /workflows/<slug>/trigger runs
	// a `dbt build` that writes to the warehouse. Open on the internet, it is a
	// remote control for the warehouse available to anyone -- which is exactly
	// the state the dev environment came up in, and nobody noticed because
	// nothing failed. A warning in the log would not have been enough: nobody
	// reads the log of a process that works. Failing at boot is what makes the
	// oversight visible.
	//
	// `local` is left out because there the server listens on the developer's
	// own machine, and demanding a password on every `make up` would push the
	// team to turn authentication off for good.
	if c.Env != "local" && !c.Auth.Enabled() {
		return Config{}, fmt.Errorf(
			"BREVIS_ENV=%s requires a credential: set BREVIS_AUTH_USER, "+
				"BREVIS_AUTH_PASSWORD_HASH (generate one with `brevis hash`) and "+
				"BREVIS_AUTH_SECRET", c.Env)
	}
	return c, nil
}

// TasksEnvironment builds the environment each local step receives.
//
// A task does NOT inherit the orchestrator's environment. The reason is
// concrete: the Brevis process carries BREVIS_DATABASE_URL with the Postgres
// user and password, and a workflow is an arbitrary command written by somebody
// else -- inheriting by default would hand the database credential to every
// step of every pipeline.
//
// What the task needs is therefore declared:
// `BREVIS_TASK_ENV=GOOGLE_PROJECT_ID,STAGE` passes those two from the process's
// environment. `NAME=value` sets a literal. `*` passes everything EXCEPT the
// BREVIS_* ones -- the wildcard exists for those who need it, and the exception
// exists because the orchestrator's configuration is never the task's business.
//
// PATH and HOME always go in: without PATH no command resolves, and the error
// would be a "not found" that explains nothing.
//
// A package function and not a method: `brevis run` runs without a database and
// therefore without a Config -- but needs the same environment.
func TasksEnvironment(names []string) map[string]string {
	env := map[string]string{
		"PATH": os.Getenv("PATH"),
		"HOME": os.Getenv("HOME"),
	}
	for _, input := range names {
		if input == "*" {
			for _, kv := range os.Environ() {
				k, v, _ := strings.Cut(kv, "=")
				if strings.HasPrefix(k, "BREVIS_") {
					continue
				}
				env[k] = v
			}
			continue
		}
		if name, value, ok := strings.Cut(input, "="); ok {
			env[name] = value
			continue
		}
		// A name with no value: passed on if it exists. Missing does NOT become
		// an empty string -- `GOOGLE_PROJECT_ID=""` would make dbt fail later,
		// with a message worse than the one for a missing variable.
		if v, existe := os.LookupEnv(input); existe {
			env[input] = v
		}
	}
	return env
}

// toleracoes reads "key=value:effect,other=value:effect".
//
// A malformed entry is IGNORED rather than becoming a boot error: a wrong
// toleration leaves the pod Pending, which is visible; refusing the scheduler's
// boot over it would also stop the workflows that do not need that pool.
func graces(key string) []Grace {
	var out []Grace
	for _, input := range list(key) {
		par, efeito, temEfeito := strings.Cut(input, ":")
		k, v, hasValue := strings.Cut(par, "=")
		if !hasValue || !temEfeito {
			continue
		}
		out = append(out, Grace{Key: strings.TrimSpace(k),
			Value: strings.TrimSpace(v), Efeito: strings.TrimSpace(efeito)})
	}
	return out
}

// TaskEnvFromEnvironment reads BREVIS_TASK_ENV for whoever did not load the whole
// Config.
func TaskEnvFromEnvironment() []string { return list("BREVIS_TASK_ENV") }

// lista splits on commas, ignoring empties — "a,,b" is a typo, and an empty
// secret name would make the server refuse the whole pod.
func list(key string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(key), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pares reads "key=value,other=value" — nodeSelector's format.
func pares(key string) map[string]string {
	out := map[string]string{}
	for _, p := range list(key) {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// optional is `get` for a value whose EMPTY setting is meaningful. `get` folds
// unset and empty together, which is right for a brand file and wrong here:
// BREVIS_METRICS_ADDR="" is how an installation says "serve no metrics", and
// with `get` it would silently get the default instead.
func optional(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func get(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// renamed reads a variable that used to have a Portuguese name, and accepts
// both.
//
// Four of them were renamed on 2026-09-10: the repository is English, and an
// operator reading English instructions had to type MANTER_EM_FALHA. The old
// name keeps working, with a warning naming the new one.
//
// The transition is not politeness. Outside `local` the engine REFUSES TO BOOT
// without a credential, and BREVIS_AUTH_USUARIO / _SENHA_HASH / _SEGREDO are
// what a running installation authenticates with. A rename with no fallback
// turns the next rollout into a CrashLoopBackOff on a deploy that changed
// nothing else -- and the operator's own manifest, which was correct yesterday,
// is what the error would be about.
//
// Removing the fallback is a breaking change and belongs in a major, with the
// warning having shipped for a version or two first.
func renamed(name, former string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	v := os.Getenv(former)
	if v != "" {
		slog.Warn("this environment variable was renamed; the old name still works",
			"old", former, "new", name,
			"note", "the old name is accepted for now and will be removed in a major version")
	}
	return v
}
