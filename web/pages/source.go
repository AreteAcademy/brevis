package pages

import (
	"context"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
	"github.com/AreteAcademy/brevis/web/components"
	"github.com/AreteAcademy/brevis/web/i18n"
)

// Source is where a step reads, as its configuration declares it
// (screen-loads): its kind -- "http", "file", "database", "other", or "" when
// it declared none -- the driver it named, the logo it is drawn with, and the
// place it reads from, when the driver says.
//
// ONLY WHAT WAS DECLARED. Only the Go SDK describes a step's source; a
// Python, dbt or shell step has no Source, and it is not guessed from the
// destination or the step's name.
type Source struct {
	Kind, Driver, Logo, Where string
}

// sourceDrivers is the SDK's own sources, by the type name it describes them
// with (sdk/describe.go: driverName). A driver not here is somebody else's,
// and is "other".
var sourceDrivers = map[string]struct{ kind, logo string }{
	"from.HTTP":      {"http", "http"},
	"from.Files":     {"file", "file"},
	"postgres.Query": {"database", "postgres"},
	"mysql.Query":    {"database", "mysql"},
}

// SourceOf reads a step's configuration into its Source; nil, or a gateway's
// configuration (which has a sink, not a source), is no Source.
func SourceOf(c *postgres.WriterConfig) Source {
	if c == nil {
		return Source{}
	}
	f := map[string]string{}
	for _, fl := range c.Fields {
		if fl.Value != nil {
			f[fl.Path] = valueText(fl.Value)
		}
	}
	driver := f["source.driver"]
	if driver == "" {
		return Source{}
	}
	known, ok := sourceDrivers[driver]
	if !ok {
		return Source{Kind: "other", Driver: driver}
	}
	s := Source{Kind: known.kind, Driver: driver, Logo: known.logo}
	switch known.kind {
	case "http":
		s.Where = f["source.url"]
	case "file":
		s.Where = f["source.path"]
	case "database":
		s.Where = joinWords("/", f["source.host"], f["source.database"])
	}
	return s
}

// AttachStepConfigs hangs each step's current configuration on the list's
// writers, by step and destination; a gateway writer, and a step that
// described nothing, are left as they are.
func AttachStepConfigs(entries []postgres.CatalogEntry, configs map[postgres.StepKey]*postgres.WriterConfig) {
	for _, e := range entries {
		for i := range e.Writers {
			w := &e.Writers[i]
			if w.Gateway == nil && w.Config == nil {
				w.Config = configs[postgres.StepKey{Workflow: w.Workflow, Node: w.Node, Target: e.Target}]
			}
		}
	}
}

// originLabel is a source's kind as a row says it: the protocol, the files,
// the database by its name, or somebody else's driver by its type.
func originLabel(ctx context.Context, s Source) string {
	switch s.Kind {
	case "http":
		return i18n.T(ctx, "loads.source.http")
	case "file":
		return i18n.T(ctx, "loads.source.file")
	case "database":
		return components.LogoTitle(ctx, s.Logo)
	}
	return s.Driver
}
