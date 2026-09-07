package runtimes_test

import (
	"reflect"
	"strings"
	"testing"

	rt "github.com/AreteAcademy/brevis/internal/domain/runtimes"
)

// The case table IS the specification.
//
// A rule with no row here is a rule nobody has checked, and the rows that
// expect NOTHING carry the weight: an over-eager detector is worse than a
// missing one, because a wrong chip sits next to a badge that cannot lie and
// borrows its credibility.
func TestDetectReadsTheCommand(t *testing.T) {
	cases := []struct {
		name    string
		run     string
		image   string
		action  string
		runtime string
		tools   []string
	}{
		// --- the plain shapes -------------------------------------------
		{name: "a script with its interpreter", run: "python fetch.py", runtime: rt.Python},
		{name: "python3", run: "python3 -m app", runtime: rt.Python},
		{name: "go run", run: "go run ./cmd/fetch", runtime: rt.Go},
		{name: "node", run: "node index.js", runtime: rt.Node},
		{name: "npm", run: "npm run build", runtime: rt.Node},
		{name: "java -jar", run: "java -jar app.jar", runtime: rt.Java},
		{name: "maven wrapper", run: "./mvnw test", runtime: rt.Java},
		{name: "cargo", run: "cargo run --release", runtime: rt.Rust},
		{name: "php", run: "php artisan schedule:run", runtime: rt.PHP},
		{name: "ruby", run: "bundle exec rake db:migrate", runtime: rt.Ruby},
		{name: "dotnet", run: "dotnet run", runtime: rt.DotNet},
		{name: "psql", run: "psql -f load.sql", runtime: rt.SQL},

		// --- the shell shapes people actually write ----------------------
		{
			name: "an env prefix does not hide the interpreter",
			run:  "FOO=bar python x.py", runtime: rt.Python,
		},
		{
			name: "two env prefixes",
			run:  "A=1 B=2 python x.py", runtime: rt.Python,
		},
		{
			name: "an absolute interpreter path",
			run:  "/usr/local/bin/python3 x.py", runtime: rt.Python,
		},
		{
			name: "a cd prefix, which the fragment split handles",
			run:  "cd /src && dbt build", tools: []string{rt.DBT},
		},
		{
			name: "uv run",
			run:  "uv run python -m app", runtime: rt.Python,
		},
		{
			name: "poetry run",
			run:  "poetry run pytest", runtime: rt.Python,
		},
		{
			name: "a pipe: every fragment is read",
			run:  "dbt build --select gold | tee log.txt", tools: []string{rt.DBT},
		},
		{
			name: "both halves of a chain contribute",
			run:  "python -m pip install -r req.txt && dbt build",
			// Python from the first fragment, dbt from the second. Reading only
			// one of the two loses half the answer.
			runtime: rt.Python, tools: []string{rt.DBT},
		},
		{
			name: "a multi-line script reports the language, not the wrapper",
			run:  "set -e\npython extract.py\npython load.py", runtime: rt.Python,
		},
		{
			name: "a shell preamble does not hide the language behind it",
			// The case that produced `strongest`. First match wins would call
			// this a Shell step and hide the Python -- the language whose stack
			// trace the operator is about to read.
			run: "cp /data/in.csv /tmp/ && python transform.py", runtime: rt.Python,
		},
		{
			name: "shell stays when there is nothing stronger",
			run:  "aws s3 sync s3://bucket/in /data", runtime: rt.Shell,
		},
		{
			name: "a script by extension, with no interpreter",
			run:  "./scripts/backfill.py", runtime: rt.Python,
		},

		// --- Spark: a tool AND a runtime --------------------------------
		{
			name: "spark-submit with a python job",
			run:  "spark-submit --py-files a.zip job.py",
			// Spark explains the memory, Python explains the stack trace.
			runtime: rt.Python, tools: []string{rt.Spark},
		},
		{
			name:    "spark-submit with a jar",
			run:     "spark-submit --class com.acme.Job app.jar",
			runtime: rt.Java, tools: []string{rt.Spark},
		},
		{
			name: "pyspark",
			run:  "pyspark script.py", runtime: rt.Python, tools: []string{rt.Spark},
		},
		{
			name: "a module marker further along the line",
			run:  "python -m pyspark", runtime: rt.Python, tools: []string{rt.Spark},
		},

		// --- tools whose runtime is deliberately not reported ------------
		{
			name: "dbt is dbt, not Python",
			// dbt IS Python underneath and nobody wants that on the card.
			run: "dbt build --select gold", tools: []string{rt.DBT},
		},
		{name: "sqlmesh", run: "sqlmesh plan", tools: []string{rt.SQLMesh}},
		{name: "meltano", run: "meltano run tap target", tools: []string{rt.Meltano}},
		{name: "terraform", run: "terraform apply -auto-approve", tools: []string{rt.Terraform}},

		// --- the rows that must stay blank ------------------------------
		{
			name: "a bare binary says nothing",
			// examples/quickstart runs exactly this. A chip here would be a
			// guess about a file the engine has never seen.
			run: "/opt/brevis/bin/fetch-weather",
		},
		{
			name: "a template the engine cannot see through",
			run:  "{{ .cmd }}",
		},
		{
			name:  "an empty step",
			run:   "",
			image: "",
		},
		{
			name: "a name that merely contains a language",
			// `pythonic-tool` is not Python, and a substring match would say so.
			run: "pythonic-tool --run",
		},

		// --- templating does not hide the head --------------------------
		{
			name: "a param in the arguments",
			run:  "python {{ .script }} --date {{ .date }}", runtime: rt.Python,
		},

		// --- action: says nothing, and the corpus test is why ------------
		{
			name: "an action contributes no runtime",
			// The first version returned Go here. `action: docker.run` runs an
			// arbitrary image, and calling it Go is a confident lie -- telling
			// a real in-process task from a dispatch one needs the executor's
			// registry, which a pure function does not have.
			action: "docker.run",
		},
		{
			name:   "an action with an image is read from the image",
			action: "kubernetes.run", image: "ghcr.io/acme/dbt-runner:1.7",
			tools: []string{rt.DBT},
		},

		// --- the image ---------------------------------------------------
		{
			name: "the image supplies the runtime when the command cannot",
			run:  "/opt/app/start", image: "python:3.12-slim", runtime: rt.Python,
		},
		{
			name: "the image's last segment, not its registry path",
			// The whole care of the image rule: this is NOT a Python image.
			run: "/opt/app/start", image: "ghcr.io/python-shop/anything:v1",
		},
		{
			name: "a tool in the image name",
			run:  "/opt/run.sh", image: "ghcr.io/acme/dbt-runner:1.7",
			runtime: rt.Shell, tools: []string{rt.DBT},
		},
		{
			name: "the command wins over the image for the runtime",
			// An image called python running dbt is a dbt step.
			run: "dbt build", image: "python:3.12",
			runtime: rt.Python, tools: []string{rt.DBT},
		},
		{
			name: "a digest reference",
			run:  "/opt/app/start", image: "golang@sha256:abc123", runtime: rt.Go,
		},
		{
			name: "a registry with a port",
			run:  "/opt/app/start", image: "registry.local:5000/team/node-worker:2",
			runtime: rt.Node,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := rt.Detect(c.run, c.image, c.action)

			if got.Runtime != c.runtime {
				t.Errorf("runtime = %q, want %q", got.Runtime, c.runtime)
			}
			if !sameTools(got.Tools, c.tools) {
				t.Errorf("tools = %v, want %v", got.Tools, c.tools)
			}

			// The Source is not decoration: the screen draws an inferred chip
			// differently, so an empty detection must not claim one.
			wantSource := rt.SourceInferred
			if c.runtime == "" && len(c.tools) == 0 {
				wantSource = ""
			}
			if got.Source != wantSource {
				t.Errorf("source = %q, want %q", got.Source, wantSource)
			}
		})
	}
}

// Precedence is a property of this package, not a line in an HTTP handler.
func TestResolvePrefersWhatTheAuthorDeclared(t *testing.T) {
	inferred := rt.Detect("python wrapper.py", "", "")
	if inferred.Runtime != rt.Python {
		t.Fatalf("the fixture stopped inferring Python: %+v", inferred)
	}

	t.Run("a declared runtime beats the parser", func(t *testing.T) {
		// The author is allowed to know better: a wrapper script in Python that
		// shells out to a Go binary is exactly this case.
		got := rt.Resolve(rt.Go, nil, inferred)
		if got.Runtime != rt.Go {
			t.Errorf("runtime = %q, want the declared %q", got.Runtime, rt.Go)
		}
		if got.Source != rt.SourceDeclared {
			t.Errorf("source = %q, want %q", got.Source, rt.SourceDeclared)
		}
	})

	t.Run("declared tools keep the inferred runtime", func(t *testing.T) {
		// The common case: the command says python, and only the author knows
		// it drives Spark. Overwriting the runtime here would lose a fact the
		// parser got right.
		got := rt.Resolve("", []string{rt.Spark}, inferred)
		if got.Runtime != rt.Python {
			t.Errorf("runtime = %q, want the inferred %q", got.Runtime, rt.Python)
		}
		if !sameTools(got.Tools, []string{rt.Spark}) {
			t.Errorf("tools = %v", got.Tools)
		}
	})

	t.Run("nothing declared leaves the inference alone", func(t *testing.T) {
		got := rt.Resolve("", nil, inferred)
		if got.Runtime != rt.Python || got.Source != rt.SourceInferred {
			t.Errorf("got %+v, want the inference untouched", got)
		}
	})

	t.Run("nothing anywhere stays empty", func(t *testing.T) {
		got := rt.Resolve("", nil, rt.Detect("/opt/bin/x", "", ""))
		if !got.Empty() || got.Source != "" {
			t.Errorf("got %+v, want the zero value -- an empty detection must "+
				"not carry a source, or the screen draws a chip with no text", got)
		}
	})
}

// Every id the vocabulary lists has to be displayable, and every id a rule can
// PRODUCE has to be in the vocabulary.
//
// The second half is what stops the two drifting: a rule quietly emitting an id
// the UI has no label for would render a blank chip.
func TestTheVocabularyAndTheRulesAgree(t *testing.T) {
	for _, id := range append(append([]string{}, rt.Runtimes...), rt.Tools...) {
		if rt.Label(id) == "" {
			t.Errorf("%q is in the vocabulary with no label", id)
		}
	}

	// Everything the detector can emit, gathered by running it.
	for _, c := range []string{
		"python x.py", "go run .", "node x.js", "java -jar a.jar", "cargo run",
		"php x.php", "ruby x.rb", "dotnet run", "psql -f a.sql", "bash x.sh",
		"dbt build", "spark-submit job.py", "sqlmesh plan", "meltano run",
		"soda scan", "duckdb -c 'select 1'", "terraform apply", "airflow dags list",
	} {
		got := rt.Detect(c, "", "")
		if got.Runtime != "" && !rt.IsRuntime(got.Runtime) {
			t.Errorf("%q produced runtime %q, which is not in the vocabulary", c, got.Runtime)
		}
		for _, tool := range got.Tools {
			if !rt.IsTool(tool) {
				t.Errorf("%q produced tool %q, which is not in the vocabulary", c, tool)
			}
		}
	}
}

// The ids reach the payload, a CSS token and the YAML. Renaming one breaks all
// three at once, so they are pinned.
func TestTheIdsAreStableKeys(t *testing.T) {
	want := "airbyte airflow dbt dotnet duckdb go java meltano node pandas " +
		"php polars python ruby rust shell soda spark sqlmesh sql terraform"
	got := append(append([]string{}, rt.Runtimes...), rt.Tools...)
	for _, id := range got {
		if !strings.Contains(want, id) {
			t.Errorf("%q is a new id: it needs a CSS token and a line in the "+
				"YAML documentation before it ships", id)
		}
	}
}

func sameTools(got, want []string) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}
