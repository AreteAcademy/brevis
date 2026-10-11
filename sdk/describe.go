package sdk

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// describeCeiling is the largest description sent, in bytes. The executors
// read the log one line at a time with a 1 MiB ceiling, and a description
// is configuration, not data: anything near this is a writer listing
// something it should not.
const describeCeiling = 64 << 10

// describedField is one setting, in the shape a gateway's manifest uses, so
// the engine stores, compares and shows both the same way.
type describedField struct {
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// describe is the pipeline's configuration as the `pipeline` marker carries
// it: the source, each stage in order, the target -- the order it runs in.
//
// It never fails. A pipeline whose stages do not validate is described
// without them, and the run's own check is what refuses it.
func (p *Pipeline) describe() map[string]any {
	var fields []describedField
	seen := map[string]bool{}
	add := func(path string, v any) {
		if v == nil || reflect.ValueOf(v).IsZero() {
			return
		}
		// One value per path, which the engine requires and refuses the
		// whole description without. A writer of somebody else's listing a
		// key twice loses its second value, not the pipeline its page.
		if seen[path] {
			slog.Warn("the pipeline's description named a setting twice; the first value is kept", "path", path)
			return
		}
		seen[path] = true
		fields = append(fields, describedField{Path: path, Value: v})
	}

	add("source.driver", driverName(p.Source.From))
	settingsOf(p.Source.From, "source.", add)

	if stages, err := p.stages(); err == nil {
		for i, st := range stages {
			prefix := fmt.Sprintf("transform.%d.", i+1)
			switch st.kind {
			case StageMap:
				names := make([]string, len(st.transformers))
				for j, fn := range st.transformers {
					names[j] = transformerName(fn)
				}
				add(prefix+"map", names)
			case StageAggregate:
				describeReduce(st.reduce, prefix+"aggregate.", add)
			}
		}
	}

	add("target.driver", driverName(p.Target.To))
	if l, ok := p.Target.To.(Locator); ok {
		add("target.target", l.Locate())
	}
	// What the Target DECLARES is under its own prefix: a writer's settings
	// are under target. too, and Postgres's `schema` (where the table is)
	// and a declared `schema` (its columns) are different things that
	// shared a path -- and the engine refused the whole description.
	add("target.declared.columns", p.Target.Columns)
	if len(p.Target.Schema) > 0 {
		cols := make([]string, len(p.Target.Schema))
		for i, c := range p.Target.Schema {
			cols[i] = strings.TrimSpace(c.Name + " " + string(c.Type))
		}
		add("target.declared.schema", cols)
	}
	add("target.declared.partition_by", p.Target.PartitionBy)
	add("target.dedup", string(p.Target.Dedup))
	settingsOf(p.Target.To, "target.", add)

	return map[string]any{"type": "pipeline", "v": 1, "fields": fields}
}

// describeReduce is an aggregation, whole: it is declarative, so all of it can
// be shown. The columns in name order, so the same Reduce always reads -- and
// hashes -- the same.
func describeReduce(r *Reduce, prefix string, add func(string, any)) {
	if r == nil {
		return
	}
	add(prefix+"by", r.By.fields)
	names := make([]string, 0, len(r.Agg))
	for name := range r.Agg {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		add(prefix+name, aggregatorText(r.Agg[name]))
	}
	if r.Finish != nil {
		add(prefix+"finish", funcName(reflect.ValueOf(r.Finish).Pointer()))
	}
}

// aggregatorText is `sum(valor)`, `max_by(valor, id)`, `count`.
func aggregatorText(a Aggregator) string {
	kind := a.kind
	if kind == "" {
		kind = "custom"
	}
	if len(a.fields) == 0 {
		return kind
	}
	return kind + "(" + strings.Join(a.fields, ", ") + ")"
}

// settingsOf adds what a driver says about itself, under prefix.
func settingsOf(driver any, prefix string, add func(string, any)) {
	c, ok := driver.(Configured)
	if !ok {
		return
	}
	for _, s := range c.Configuration() {
		add(prefix+s.Key, s.Value)
	}
}

// driverName is a driver's type as its package spells it: `bigquery.Table`,
// `from.HTTP`.
func driverName(driver any) string {
	if driver == nil {
		return ""
	}
	return strings.TrimPrefix(fmt.Sprintf("%T", driver), "*")
}

// transformerName is what a transformer does, when one of the SDK's factories
// made it (label.go), and its function's name otherwise.
func transformerName(fn Transformer) string {
	if fn == nil {
		return ""
	}
	if l, ok := labelOf(fn); ok {
		return l
	}
	return funcName(reflect.ValueOf(fn).Pointer())
}

// A closure's runtime name is its enclosing function's plus `.func1`.
var closureSuffix = regexp.MustCompile(`(\.func\d+)+(\.\d+)*$`)

// funcName names a function the way somebody reading the pipeline would:
// `sdk.SkipWithout`, `main.normalise`.
//
// A closure is named by the function that made it, because that is usually a
// factory -- SkipWithout("id") returns one, and "sdk.SkipWithout" is the
// answer. When the compiler INLINES the factory, the runtime name carries the
// caller too (`sdk.build.SkipWithout.func1`), so the maker is the LAST
// component, not the first. One written inline in main, or at package level
// (`init.func1`, `glob..func1`), has nothing worth naming and says so.
func funcName(pc uintptr) string {
	f := runtime.FuncForPC(pc)
	if f == nil {
		return "anonymous function"
	}
	name := f.Name()
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	pkg, rest, _ := strings.Cut(name, ".")
	loc := closureSuffix.FindStringIndex(rest)
	if loc == nil {
		return name
	}
	parts := strings.Split(rest[:loc[0]], ".")
	maker := parts[len(parts)-1]
	if maker == "" || maker == "init" || maker == "glob" || (pkg == "main" && maker == "main") {
		return "anonymous function"
	}
	return pkg + "." + maker
}

// describe sends the pipeline's description, once, before the first phase.
//
// Too large for one line, it is not sent at all -- the run goes on, and the
// console shows the writer without its configuration rather than half of it.
func (r *reporter) describe(p *Pipeline) {
	if r == nil || !r.on {
		return
	}
	ev := p.describe()
	line, err := json.Marshal(ev)
	if err != nil || len(line) > describeCeiling {
		slog.Warn("the pipeline's description was not sent: the console will not show how it is configured",
			"bytes", len(line), "ceiling", describeCeiling, "error", err)
		return
	}
	r.emit(ev)
}
