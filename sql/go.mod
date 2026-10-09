// brevis-sql: plain .sql files become tables and views, run as a Brevis step.
//
// A module of its own, like the gateway, for the reason #66 states as a rule:
// anything holding a warehouse driver is its own module and binary, and
// `./cmd/brevis` imports neither the SDK nor a driver. engine-weight.sh stays
// where it is because nothing here can reach it.
//
// THREE REQUIRES, and each one argued for itself. This comment says what they
// bought, because the next one has to argue too.
//
// The reference extractor is stdlib only -- 1.9 MB against a real parser's 14
// to 37 -- and that is the property this module keeps. What is allowed in
// beside it is a dependency the module cannot do its job without, not a
// convenience.
//
// `gopkg.in/yaml.v3`: a model's header is YAML, the engine and the gateway
// already read their config with it, and the alternative is a hand-rolled
// subset that accepts something the rest of Brevis would reject. A header
// that means one thing here and another in a workflow is worse than 504 KB.
//
// `github.com/jackc/pgx/v5` [#62 S4]: a dialect that cannot connect cannot be
// conformance-tested, and a suite proving only the strings is the thing the
// dialect package exists to avoid. It is the driver the SDK already uses, so
// the repository has one Postgres driver rather than two opinions about
// escaping. The budget it spends against is S8's: the image at 30 MB.
//
// `golang.org/x/oauth2` [#62 S5]: the credentials half of BigQuery, and the
// ONLY half taken from a library. The measurement that decided it, both in
// an empty main:
//
//	cloud.google.com/go/bigquery   31 MB   526 packages   236 modules
//	golang.org/x/oauth2            8.5 MB  201 packages     3 modules
//
// The official client does not fit under S8's 30 MB before a line of
// brevis-sql is written, and what this tool asks of BigQuery is two
// operations -- run a statement, read one value. The REST endpoint is in
// internal/dialect/bigquery/conn.go, with a test for each of the three ways
// it could be quietly wrong. Both dialects together: 15 MB, 241 packages.
module github.com/AreteAcademy/brevis/sql

go 1.26.0

require (
	github.com/jackc/pgx/v5 v5.11.0
	golang.org/x/oauth2 v0.37.0
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/google/uuid v1.6.0 // indirect

require (
	cloud.google.com/go/compute/metadata v0.3.0 // indirect
	github.com/AreteAcademy/brevis/sdk v0.84.1
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

replace github.com/AreteAcademy/brevis/sdk => ../sdk
