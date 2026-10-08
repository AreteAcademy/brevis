// brevis-sql: plain .sql files become tables and views, run as a Brevis step.
//
// A module of its own, like the gateway, for the reason #66 states as a rule:
// anything holding a warehouse driver is its own module and binary, and
// `./cmd/brevis` imports neither the SDK nor a driver. engine-weight.sh stays
// where it is because nothing here can reach it.
//
// ONE REQUIRE, and it argued for itself.
//
// The reference extractor is stdlib only -- 1.9 MB against a real parser's 14
// to 37 -- and that is the property this module keeps. `gopkg.in/yaml.v3` is
// here because a model's header is YAML, the engine and the gateway already
// read their config with it, and the alternative is a hand-rolled subset that
// accepts something the rest of Brevis would reject. A header that means one
// thing here and another in a workflow is worse than 504 KB.
module github.com/AreteAcademy/brevis/sql

go 1.26.0

require gopkg.in/yaml.v3 v3.0.1
