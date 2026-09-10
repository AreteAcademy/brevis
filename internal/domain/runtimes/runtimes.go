// Package runtimes says what a step runs in: a language, and the tools it
// drives.
//
// The package is `runtimes` and not `runtime` because `cmd/brevis` imports the
// standard library's `runtime`, and a collision there would force an import
// alias on every file that wanted both.
//
// # Why this is a package and not a method
//
// Detect is a PURE function over three strings. That is the whole point: a
// decision made inside something that holds a Kubernetes client or a pool is a
// decision no test ever sees, and this repository has already paid for that
// once -- load.CreationPlan in the SDK exists for the same reason.
//
// Every rule here is exercised with no cluster, no image and no database.
//
// # Why a closed vocabulary
//
// Detect returns "" when it does not know, and the screen draws nothing. It
// never returns a guess dressed as an answer.
//
// A card that says nothing is honest. A card that says "shell" for a step that
// runs dbt costs somebody the hour they spend believing it, and the badge
// beside it -- `SDK v0.53.0` -- is trustworthy precisely because it is
// OBSERVED and cannot lie. Anything drawn next to it inherits the standard.
package runtimes

import (
	"sort"
	"strings"
)

// Source says where a detection came from. The screen shows it, because "this
// step runs Python" and "this command starts with python" are different claims.
const (
	// SourceObserved is the process saying so at run time. Nothing produces it
	// yet: it arrives with the `@brevis:` protocol, alongside the SDK badge.
	SourceObserved = "observed"

	// SourceDeclared is the author writing `runtime:` in the workflow.
	SourceDeclared = "declared"

	// SourceInferred is this package reading `run:` and `image:`.
	SourceInferred = "inferred"
)

// The runtimes. An id is a stable key: it reaches the payload, the CSS token
// and the YAML, so renaming one is a breaking change to all three.
const (
	Python = "python"
	Go     = "go"
	Node   = "node"
	Java   = "java"
	Rust   = "rust"
	PHP    = "php"
	Ruby   = "ruby"
	DotNet = "dotnet"
	Shell  = "shell"
	SQL    = "sql"
)

// The tools.
const (
	DBT       = "dbt"
	Spark     = "spark"
	Airbyte   = "airbyte"
	Soda      = "soda"
	SQLMesh   = "sqlmesh"
	Meltano   = "meltano"
	DuckDB    = "duckdb"
	DLT       = "dlt"
	Pandas    = "pandas"
	Polars    = "polars"
	Airflow   = "airflow"
	Terraform = "terraform"
)

// Runtimes and Tools are the closed vocabularies, in display order.
//
// Both are exported because publish-time validation needs them and so does the
// error message that lists what is valid -- a refusal that does not say what
// WOULD have been accepted just moves the guessing.
var (
	Runtimes = []string{Python, Go, Node, Java, Rust, PHP, Ruby, DotNet, SQL, Shell}
	Tools    = []string{DBT, Spark, Airbyte, Soda, SQLMesh, Meltano, DLT, DuckDB, Pandas, Polars, Airflow, Terraform}
)

// Label is what a person reads. Empty for an id outside the vocabulary.
func Label(id string) string {
	return labels[id]
}

var labels = map[string]string{
	Python: "Python", Go: "Go", Node: "Node.js", Java: "Java",
	Rust: "Rust", PHP: "PHP", Ruby: "Ruby", DotNet: ".NET",
	SQL: "SQL", Shell: "Shell",

	DBT: "dbt", Spark: "Spark", Airbyte: "Airbyte", Soda: "Soda",
	SQLMesh: "SQLMesh", Meltano: "Meltano", DLT: "dlt", DuckDB: "DuckDB",
	Pandas: "pandas", Polars: "Polars", Airflow: "Airflow",
	Terraform: "Terraform",
}

// IsRuntime and IsTool answer whether an id is in the vocabulary.
func IsRuntime(id string) bool { return contains(Runtimes, id) }

// IsTool reports whether id names a known tool.
func IsTool(id string) bool { return contains(Tools, id) }

// Detection is what a step runs in.
//
// The zero value means "nothing is known", and it is a normal outcome: a step
// whose command is a bare binary path, or a template the engine cannot see
// through, tells us nothing and gets no chip.
type Detection struct {
	// Runtime is the language or execution environment. Empty when unknown.
	Runtime string

	// Tools are the frameworks the step drives, sorted, possibly empty.
	//
	// Separate from Runtime because one step routinely has both and ranking
	// them has no right answer: `spark-submit job.py` is Spark explaining the
	// memory and Python explaining the stack trace.
	Tools []string

	// Source is one of the Source* constants. Empty when Runtime and Tools
	// both are.
	Source string
}

// Empty says whether there is anything to draw.
func (d Detection) Empty() bool { return d.Runtime == "" && len(d.Tools) == 0 }

// Detect works out what a step runs in, from the fields the workflow already
// carries.
//
// `action` contributes NOTHING, and that is a correction the corpus test forced.
//
// The first version returned Go for any action, reasoning that an action is a
// task registered in this binary. Two of the repository's own examples use
// `action: kubernetes.run` and `action: docker.run` -- dispatch actions whose
// payload is an arbitrary image -- and both came out labelled Go, which is a
// confident lie about a step that runs somebody else's container.
//
// Telling a real in-process task from a dispatch one needs the executor's
// registry, and this function is pure on purpose. Blank is the honest answer.
//
// What is left is a reading of `run` and `image`, and the two are not equal.
// The command wins for the runtime, because the command is what executes; the
// image only supplies one when the command supplied none. Tools come from both.
// An image named `python:3.12` running `dbt build` is a dbt step.
func Detect(run, image, action string) Detection {
	_ = action

	fromRun := fromCommand(run)
	fromImage := fromImageRef(image)

	d := Detection{Runtime: fromRun.Runtime}
	if d.Runtime == "" {
		d.Runtime = fromImage.Runtime
	}

	tools := map[string]bool{}
	for _, t := range append(fromRun.Tools, fromImage.Tools...) {
		tools[t] = true
	}
	for t := range tools {
		d.Tools = append(d.Tools, t)
	}
	sort.Strings(d.Tools)

	if !d.Empty() {
		d.Source = SourceInferred
	}
	return d
}

// Resolve applies the precedence: observed > declared > inferred.
//
// It lives here, next to Detect, so the order is a property of this package
// rather than a line in an HTTP handler nobody tests. The declared value wins
// over the inferred one because the author is allowed to know better than the
// parser -- an image whose command is a wrapper script is exactly that case.
//
// Declared runtime and declared tools are honoured INDEPENDENTLY: declaring
// only `tools:` keeps the inferred runtime, which is the common case (the
// command says `python`, and only the author knows it drives Spark).
func Resolve(declaredRuntime string, declaredTools []string, inferred Detection) Detection {
	out := inferred
	source := inferred.Source

	if declaredRuntime != "" {
		out.Runtime = declaredRuntime
		source = SourceDeclared
	}
	if len(declaredTools) > 0 {
		out.Tools = append([]string(nil), declaredTools...)
		sort.Strings(out.Tools)
		source = SourceDeclared
	}

	if out.Empty() {
		return Detection{}
	}
	out.Source = source
	return out
}

// ---------------------------------------------------------------------------
// The command
// ---------------------------------------------------------------------------

// separators split a shell command into the pieces that each run something.
//
// EVERY piece contributes. `python -m pip install -r req.txt && dbt build` is a
// dbt step whose runtime is Python: reading only the first would call it
// Python alone, and reading only the last would lose the runtime.
var separators = []string{"&&", "||", ";", "|", "\n"}

// prefixes are commands that run another command. They are stepped over rather
// than matched, or every `uv run python x.py` would detect as nothing.
var prefixes = map[string]int{
	"env": 1, "exec": 1, "time": 1, "nohup": 1, "sudo": 1, "xargs": 1,
	"uv": 2, "poetry": 2, "pipenv": 2, "pdm": 2, "rye": 2, // `uv run <cmd>`
}

// heads maps the first word of a command to a runtime.
var heads = map[string]string{
	"python": Python, "python2": Python, "python3": Python, "pip": Python,
	"pip3": Python, "pytest": Python, "gunicorn": Python, "uvicorn": Python,
	"celery": Python, "jupyter": Python, "conda": Python, "ruff": Python,

	"go": Go, "gofmt": Go, "golangci-lint": Go,

	"node": Node, "npm": Node, "npx": Node, "yarn": Node, "pnpm": Node,
	"bun": Node, "bunx": Node, "deno": Node, "tsx": Node, "ts-node": Node,

	"java": Java, "javac": Java, "mvn": Java, "gradle": Java, "gradlew": Java,
	"mvnw": Java, "scala": Java, "sbt": Java,

	"cargo": Rust, "rustc": Rust,

	"php": PHP, "composer": PHP, "artisan": PHP,

	"ruby": Ruby, "bundle": Ruby, "rake": Ruby, "gem": Ruby, "rails": Ruby,

	"dotnet": DotNet,

	"psql": SQL, "mysql": SQL, "sqlite3": SQL, "bq": SQL, "clickhouse-client": SQL,

	"sh": Shell, "bash": Shell, "zsh": Shell, "ash": Shell, "dash": Shell,
	"cp": Shell, "mv": Shell, "rm": Shell, "mkdir": Shell, "tar": Shell,
	"gzip": Shell, "gunzip": Shell, "curl": Shell, "wget": Shell, "rsync": Shell,
	"aws": Shell, "gcloud": Shell, "gsutil": Shell, "az": Shell, "kubectl": Shell,
	"jq": Shell, "sed": Shell, "awk": Shell, "echo": Shell, "cat": Shell,
	"find": Shell, "make": Shell,
}

// toolHeads maps a first word straight to a tool. The runtime underneath is
// deliberately NOT set: `dbt build` is Python under the hood and nobody wants
// that on the card.
var toolHeads = map[string]string{
	"dbt": DBT, "sqlmesh": SQLMesh, "meltano": Meltano, "soda": Soda,
	"airbyte": Airbyte, "duckdb": DuckDB, "terraform": Terraform,
	// `dlt pipeline`, `dlt init`, `dlt deploy` -- the CLI. The COMMON case is
	// `python my_pipeline.py`, which this cannot see and correctly says only
	// Python; that one is what `tools: [dlt]` in the workflow is for, and a
	// declared chip is drawn differently from an inferred one on purpose.
	"dlt":     DLT,
	"airflow": Airflow,
}

// sparkHeads are the Spark entry points. `pyspark` and `spark-submit` with a
// `.py` also say Python, which is why they are not in toolHeads.
var sparkHeads = map[string]string{
	"spark-submit": "", "spark-sql": "", "pyspark": Python, "spark-shell": Java,
}

// markers are tokens that name a tool anywhere in a fragment, not only at the
// head: `python -m dbt.cli` and `python -m pyspark` are both real.
var markers = map[string]string{
	"dbt": DBT, "dbt.cli": DBT, "pyspark": Spark, "sqlmesh": SQLMesh,
	"meltano": Meltano, "duckdb": DuckDB, "great_expectations": Soda,
	"dlt": DLT,
}

func fromCommand(run string) Detection {
	run = strings.TrimSpace(run)
	if run == "" {
		return Detection{}
	}

	var d Detection
	tools := map[string]bool{}

	for _, fragment := range splitFragments(run) {
		words := strings.Fields(fragment)
		words = stripAssignments(words)
		words = stripPrefixes(words)
		if len(words) == 0 {
			continue
		}

		head := base(words[0])

		// A `.py` / `.js` / `.sh` script invoked directly, with no interpreter.
		if rt := byExtension(head); rt != "" {
			d.Runtime = strongest(d.Runtime, rt)
		}

		if t, ok := toolHeads[head]; ok {
			tools[t] = true
			continue
		}
		if rt, ok := sparkHeads[head]; ok {
			tools[Spark] = true
			if rt != "" {
				d.Runtime = strongest(d.Runtime, rt)
			}
			// spark-submit's runtime comes from what it submits.
			if head == "spark-submit" {
				d.Runtime = strongest(d.Runtime, byArgsExtension(words))
			}
			continue
		}
		if rt, ok := heads[head]; ok {
			d.Runtime = strongest(d.Runtime, rt)
		}

		for _, w := range words[1:] {
			if t, ok := markers[strings.TrimPrefix(base(w), "-")]; ok {
				tools[t] = true
			}
		}
	}

	for t := range tools {
		d.Tools = append(d.Tools, t)
	}
	sort.Strings(d.Tools)
	return d
}

// strongest keeps the more specific of two runtimes.
//
// Shell is the WEAKEST, and the difference is not cosmetic. Real steps open
// with a shell preamble:
//
//	cp /data/in.csv /tmp/ && python transform.py
//
// First match wins would call that a Shell step and hide the Python, which is
// the language whose stack trace the operator is about to read. Anything
// concrete beats `shell`; between two concrete runtimes the first still wins,
// because a command that runs two languages has no single honest answer and the
// one that starts it is the better guess.
func strongest(current, candidate string) string {
	switch {
	case candidate == "":
		return current
	case current == "":
		return candidate
	case current == Shell && candidate != Shell:
		return candidate
	default:
		return current
	}
}

// splitFragments cuts on the shell separators, keeping every piece.
func splitFragments(run string) []string {
	out := []string{run}
	for _, sep := range separators {
		var next []string
		for _, piece := range out {
			next = append(next, strings.Split(piece, sep)...)
		}
		out = next
	}
	return out
}

// stripAssignments drops the leading `VAR=value` of `FOO=bar python x.py`.
//
// Only LEADING ones: a `--flag=value` further along is an argument, and a
// `=` inside a quoted string is not an assignment at all -- neither reaches
// here, because both come after the first non-assignment word.
func stripAssignments(words []string) []string {
	for len(words) > 0 {
		eq := strings.Index(words[0], "=")
		if eq <= 0 || strings.ContainsAny(words[0][:eq], "/.-") {
			break
		}
		words = words[1:]
	}
	return words
}

// stripPrefixes steps over the commands that run other commands.
func stripPrefixes(words []string) []string {
	for len(words) > 0 {
		skip, ok := prefixes[base(words[0])]
		if !ok || len(words) <= skip {
			break
		}
		// `uv run python x.py`: skip both `uv` and `run`. Anything else after
		// `uv` (`uv pip install`) is the tool itself, so only the documented
		// sub-command is stepped over.
		if skip == 2 && words[1] != "run" {
			break
		}
		words = words[skip:]
	}
	return words
}

// base strips a directory from a path so `/usr/bin/python3` matches `python3`.
func base(word string) string {
	if i := strings.LastIndex(word, "/"); i >= 0 && i+1 < len(word) {
		return word[i+1:]
	}
	return word
}

var extensions = map[string]string{
	".py": Python, ".js": Node, ".mjs": Node, ".ts": Node, ".sh": Shell,
	".rb": Ruby, ".php": PHP, ".sql": SQL, ".jar": Java,
}

func byExtension(word string) string {
	for ext, rt := range extensions {
		if strings.HasSuffix(word, ext) {
			return rt
		}
	}
	return ""
}

// byArgsExtension reads what `spark-submit` was handed: a `.py` makes it
// PySpark, a `.jar` makes it the JVM.
func byArgsExtension(words []string) string {
	for _, w := range words[1:] {
		if strings.HasPrefix(w, "-") {
			continue
		}
		if rt := byExtension(base(w)); rt != "" {
			return rt
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The image
// ---------------------------------------------------------------------------

// fromImageRef reads the image's NAME -- the last path segment, with the tag
// and digest removed.
//
// Only the last segment, and this is the whole care of the function.
// `ghcr.io/python-shop/anything` is not a Python image, and matching anywhere
// in the reference would say it is. `library/python:3.12` and
// `ghcr.io/acme/dbt-runner:1.7` both land on their last segment and are read
// correctly.
func fromImageRef(image string) Detection {
	name := imageName(image)
	if name == "" {
		return Detection{}
	}

	var d Detection
	tools := map[string]bool{}
	for _, part := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	}) {
		if t, ok := imageTools[part]; ok {
			tools[t] = true
		}
		if rt, ok := imageRuntimes[part]; ok && d.Runtime == "" {
			d.Runtime = rt
		}
	}

	for t := range tools {
		d.Tools = append(d.Tools, t)
	}
	sort.Strings(d.Tools)
	return d
}

// imageName is the reference's last path segment, without tag or digest.
func imageName(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, "/"); i >= 0 {
		image = image[i+1:]
	}
	if i := strings.Index(image, ":"); i >= 0 {
		image = image[:i]
	}
	return strings.ToLower(image)
}

var imageRuntimes = map[string]string{
	"python": Python, "py": Python, "golang": Go, "go": Go,
	"node": Node, "nodejs": Node, "java": Java, "openjdk": Java,
	"jdk": Java, "rust": Rust, "php": PHP, "ruby": Ruby, "dotnet": DotNet,
}

var imageTools = map[string]string{
	"dbt": DBT, "spark": Spark, "pyspark": Spark, "airbyte": Airbyte,
	"soda": Soda, "sqlmesh": SQLMesh, "meltano": Meltano, "duckdb": DuckDB,
	"airflow": Airflow, "terraform": Terraform,
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
