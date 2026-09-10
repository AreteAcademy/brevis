package runtimes_test

import (
	"os"
	"reflect"
	"regexp"
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

// The ids reach three places outside this package: the label map the DAG draws
// from, the colour tokens in the stylesheet, and the vocabulary table people
// read before writing YAML. Renaming or adding one breaks all three at once.
//
// This used to be a hard-coded string listing the ids, which meant it could
// only ever say "that id is new" -- it never once looked at the files it named,
// so an id could be added to the string and ship with no label and no
// documentation and the test would still be green. It reads them now.
func TestTheIdsAreStableKeys(t *testing.T) {
	labels := jsLabelKeys(t)
	tokens := cssLangTokens(t)
	documented := documentedVocabulary(t)

	for _, id := range rt.Runtimes {
		// A runtime carries its own colour. Without the token the chip renders
		// in the muted grey every tool uses, which is not wrong enough to
		// notice and not right enough to keep.
		if !tokens[id] {
			t.Errorf("runtime %q has no --color-lang-%s in web/assets/app.src.css", id, id)
		}
	}
	// Tools are deliberately NOT in that list: they draw muted, so a token for
	// one would be the exception nobody could explain. Asserted so that adding
	// one is a decision rather than a slip.
	for _, id := range rt.Tools {
		if tokens[id] {
			t.Errorf("tool %q has a colour token; tools draw muted on purpose", id)
		}
	}

	for _, id := range append(append([]string{}, rt.Runtimes...), rt.Tools...) {
		if !labels[id] {
			t.Errorf("%q has no entry in LANG_LABEL in web/assets/dag.js; the chip would render the raw id", id)
		}
		if !documented[id] {
			t.Errorf("%q is not in the vocabulary table in docs/RUNTIME.md, which is what publish refusals point at", id)
		}
		delete(documented, id)
	}
	// And the other direction: a documented id the engine does not know is a
	// promise the parser will not keep.
	for id := range documented {
		t.Errorf("docs/RUNTIME.md lists %q, which is not in the vocabulary", id)
	}
}

// jsLabelKeys reads the ids out of LANG_LABEL.
func jsLabelKeys(t *testing.T) map[string]bool {
	return keysIn(t, "web/assets/dag.js", `var LANG_LABEL = {`, "};", `(?m)([a-z0-9]+):\s*"`)
}

// cssLangTokens reads the --color-lang-* custom properties.
func cssLangTokens(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile("../../../web/assets/app.src.css")
	if err != nil {
		t.Fatalf("stylesheet: %v", err)
	}
	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`--color-lang-([a-z0-9]+)\s*:`).FindAllStringSubmatch(string(b), -1) {
		found[m[1]] = true
	}
	if len(found) == 0 {
		t.Fatal("no --color-lang-* tokens found; the stylesheet moved and this check went blind")
	}
	return found
}

// documentedVocabulary reads the backticked ids out of the vocabulary section,
// and only that section -- an id mentioned in an example further down is not a
// promise that publish will accept it.
func documentedVocabulary(t *testing.T) map[string]bool {
	found := keysIn(t, "../../../docs/RUNTIME.md", "## The vocabulary", "\n## ", "`"+`([a-z0-9]+)`+"`")
	// The section names the two FIELDS in backticks as well. They end in a
	// colon in the source, so they never reach here -- asserted because a
	// change to that heading would otherwise silently add two phantom ids.
	for _, field := range []string{"runtime", "tools"} {
		if found[field] {
			t.Fatalf("the vocabulary section lists %q as an id; the field headings changed shape", field)
		}
	}
	return found
}

// keysIn pulls a regex's first group out of one delimited region of a file.
func keysIn(t *testing.T, path, open, close, pattern string) map[string]bool {
	t.Helper()
	if !strings.HasPrefix(path, "..") {
		path = "../../../" + path
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	body := string(b)
	i := strings.Index(body, open)
	if i < 0 {
		t.Fatalf("%s no longer contains %q; this check is reading nothing", path, open)
	}
	body = body[i+len(open):]
	if j := strings.Index(body, close); j >= 0 {
		body = body[:j]
	}
	found := map[string]bool{}
	for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(body, -1) {
		found[m[1]] = true
	}
	if len(found) == 0 {
		t.Fatalf("%s matched no ids between %q and %q", path, open, close)
	}
	return found
}

func sameTools(got, want []string) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}

// TestDLTIsDetectedWhereItCanBeAndNotWhereItCannot.
//
// The caveat the proposal raised, answered as a test rather than as a promise:
// a dlt pipeline is USUALLY `python my_pipeline.py`, which names no tool at
// all. A chip that lit up there would be a guess, and one that never lit up
// would be a chip nobody trusts.
//
// So the detector claims dlt exactly where the command says so and stays quiet
// otherwise -- and the quiet case is what `tools: [dlt]` in the workflow is
// for, which the card already draws differently from an inference.
func TestDLTIsDetectedWhereItCanBeAndNotWhereItCannot(t *testing.T) {
	for _, c := range []struct {
		run     string
		tools   []string
		runtime string
	}{
		// The CLI: `dlt` is the head, so no runtime is claimed -- same rule as
		// dbt, which is Python underneath and still does not say so.
		{"dlt pipeline orders run", []string{rt.DLT}, ""},
		{"dlt init chess duckdb", []string{rt.DLT}, ""},
		// A module invocation: the head is `python`, so the marker catches it
		// and BOTH are true.
		{"python -m dlt pipeline orders", []string{rt.DLT}, rt.Python},
		// The common case names no tool. Python, and nothing else -- claiming
		// dlt here would be inventing it from a filename.
		{"python pipelines/orders.py", nil, rt.Python},
		{"python -u main.py", nil, rt.Python},
	} {
		t.Run(c.run, func(t *testing.T) {
			d := rt.Detect(c.run, "", "")
			if !sameTools(d.Tools, c.tools) {
				t.Errorf("tools = %v, wanted %v", d.Tools, c.tools)
			}
			if d.Runtime != c.runtime {
				t.Errorf("runtime = %q, wanted %q", d.Runtime, c.runtime)
			}
		})
	}
}

// A declared `tools: [dlt]` has to be accepted by the validator, which is what
// makes the common case usable at all. Before the constant existed, writing it
// got a publish-time refusal listing every tool except the one being run.
func TestDLTMayBeDeclared(t *testing.T) {
	if !rt.IsTool(rt.DLT) {
		t.Fatal("dlt is not a tool the validator accepts")
	}
	if got := rt.Label(rt.DLT); got != "dlt" {
		t.Errorf("Label(dlt) = %q; it is lowercase in its own documentation", got)
	}
	// And it is in the closed vocabulary, which is what the refusal message
	// prints -- a tool that validates but is absent from that list would send
	// the next person guessing.
	var listed bool
	for _, id := range rt.Tools {
		if id == rt.DLT {
			listed = true
		}
	}
	if !listed {
		t.Error("dlt validates but is absent from Tools, which is what errors list")
	}
}
