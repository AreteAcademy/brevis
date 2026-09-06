// Package config loads and validates the process's configuration from the
// environment.
//
// It sits outside the tree §36 of the plan describes, which did not foresee a
// package for this. The alternative was scattering os.Getenv across cmd/ and
// infrastructure/; a single point of reading and validation is worth the
// detour, and rule 7 asks that decisions like this one be explicit rather than
// silent.
package config

import (
	"fmt"
	"github.com/AreteAcademy/brevis/internal/auth"
	"os"
	"strconv"
	"strings"
	"time"
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

	// TaskEnv lists what the process passes on to local tasks. See
	// AmbienteDasTasks -- the default does NOT inherit the environment, and
	// that is deliberate.
	TaskEnv []string

	// SlackWebhook receives the alert for a definitive failure. Empty means
	// nobody is told. It comes from the environment and never from the YAML:
	// whoever holds the URL posts in the channel as if they were the
	// platform.
	SlackWebhook string

	// Auth is the operator credential that closes the interface. See internal/auth.
	Auth auth.Credencial

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

	// SecretsPermitidos limits what a YAML's `secrets:` may name. Empty denies
	// everything: the installation decides which secrets exist for workflows,
	// and the YAML decides which step receives each one.
	SecretsPermitidos []string

	// CredencialPVC and CredencialPath mount the volume where the SDK keeps the
	// credential it rotates. Without the PVC, nothing changes.
	CredencialPVC  string
	CredencialPath string
	NodeSelector   map[string]string
	// Tolerations in "key=value:effect" form, comma separated. An arm64 pool
	// commonly carries a taint, and without a toleration the task's pod stays
	// Pending forever -- no error, just stopped.
	Toleracoes    []Toleracao
	ManterEmFalha bool
}

// Toleracao mirrors the pod's field, without importing Kubernetes' type.
type Toleracao struct {
	Chave  string
	Valor  string
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
		TaskEnv:      lista("BREVIS_TASK_ENV"),
		SlackWebhook: os.Getenv("BREVIS_SLACK_WEBHOOK"),
		UIURL:        os.Getenv("BREVIS_UI_URL"),
		Auth: auth.Credencial{
			Usuario: os.Getenv("BREVIS_AUTH_USUARIO"),
			Hash:    os.Getenv("BREVIS_AUTH_SENHA_HASH"),
			Segredo: []byte(os.Getenv("BREVIS_AUTH_SEGREDO")),
		},
		Pods: PodsConfig{
			Modo:              get("BREVIS_PODS", "auto"),
			Namespace:         os.Getenv("BREVIS_POD_NAMESPACE"),
			ServiceAccount:    os.Getenv("BREVIS_POD_SERVICE_ACCOUNT"),
			PullSecrets:       lista("BREVIS_POD_PULL_SECRETS"),
			EnvFromSecrets:    lista("BREVIS_POD_ENV_FROM_SECRETS"),
			EnvFromConfigMaps: lista("BREVIS_POD_ENV_FROM_CONFIGMAPS"),
			SecretsPermitidos: lista("BREVIS_POD_ALLOWED_SECRETS"),
			CredencialPVC:     get("BREVIS_POD_CREDENTIAL_PVC", ""),
			CredencialPath:    get("BREVIS_POD_CREDENTIAL_PATH", ""),
			NodeSelector:      pares("BREVIS_POD_NODE_SELECTOR"),
			Toleracoes:        toleracoes("BREVIS_POD_TOLERATIONS"),
			ManterEmFalha:     os.Getenv("BREVIS_POD_MANTER_EM_FALHA") == "true",
		},
		ShutdownTimeout: 15 * time.Second,
	}

	if v := os.Getenv("BREVIS_SHUTDOWN_TIMEOUT_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("BREVIS_SHUTDOWN_TIMEOUT_SECONDS: %q nao e um inteiro", v)
		}
		c.ShutdownTimeout = time.Duration(n) * time.Second
	}

	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("BREVIS_DATABASE_URL is required")
	}
	switch c.Pods.Modo {
	case "auto", "on", "off":
	default:
		return Config{}, fmt.Errorf("BREVIS_PODS: %q invalido (auto, on ou off)", c.Pods.Modo)
	}
	if err := c.Auth.Validar(); err != nil {
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
	if c.Env != "local" && !c.Auth.Ativa() {
		return Config{}, fmt.Errorf(
			"BREVIS_ENV=%s exige credencial: defina BREVIS_AUTH_USUARIO, "+
				"BREVIS_AUTH_SENHA_HASH (gere com `brevis hash`) e "+
				"BREVIS_AUTH_SEGREDO", c.Env)
	}
	return c, nil
}

// AmbienteDasTasks builds the environment each local step receives.
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
func AmbienteDasTasks(nomes []string) map[string]string {
	env := map[string]string{
		"PATH": os.Getenv("PATH"),
		"HOME": os.Getenv("HOME"),
	}
	for _, entrada := range nomes {
		if entrada == "*" {
			for _, kv := range os.Environ() {
				k, v, _ := strings.Cut(kv, "=")
				if strings.HasPrefix(k, "BREVIS_") {
					continue
				}
				env[k] = v
			}
			continue
		}
		if nome, valor, ok := strings.Cut(entrada, "="); ok {
			env[nome] = valor
			continue
		}
		// A name with no value: passed on if it exists. Missing does NOT become
		// an empty string -- `GOOGLE_PROJECT_ID=""` would make dbt fail later,
		// with a message worse than the one for a missing variable.
		if v, existe := os.LookupEnv(entrada); existe {
			env[entrada] = v
		}
	}
	return env
}

// toleracoes reads "key=value:effect,other=value:effect".
//
// Entrada malformada e IGNORADA em vez de virar erro de boot: uma toleracao
// errada deixa o pod Pending, que e visivel; recusar o boot do scheduler por
// causa dela pararia tambem os workflows que nao precisam daquele pool.
func toleracoes(chave string) []Toleracao {
	var out []Toleracao
	for _, entrada := range lista(chave) {
		par, efeito, temEfeito := strings.Cut(entrada, ":")
		k, v, temValor := strings.Cut(par, "=")
		if !temValor || !temEfeito {
			continue
		}
		out = append(out, Toleracao{Chave: strings.TrimSpace(k),
			Valor: strings.TrimSpace(v), Efeito: strings.TrimSpace(efeito)})
	}
	return out
}

// TaskEnvDoAmbiente le BREVIS_TASK_ENV para quem nao carregou a Config inteira.
func TaskEnvDoAmbiente() []string { return lista("BREVIS_TASK_ENV") }

// lista separa por virgula, ignorando vazios — "a,,b" e um erro de digitacao, e
// um nome de secret vazio faria o pod inteiro ser recusado pelo servidor.
func lista(chave string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(chave), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pares le "chave=valor,outra=valor" — o formato de nodeSelector.
func pares(chave string) map[string]string {
	out := map[string]string{}
	for _, p := range lista(chave) {
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

func get(chave, padrao string) string {
	if v := os.Getenv(chave); v != "" {
		return v
	}
	return padrao
}
