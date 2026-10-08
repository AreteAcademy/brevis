// brevis-sql: plain .sql files become tables and views, run as a Brevis step.
//
// A module of its own, like the gateway, for the reason #66 states as a rule:
// anything holding a warehouse driver is its own module and binary, and
// `./cmd/brevis` imports neither the SDK nor a driver. engine-weight.sh stays
// where it is because nothing here can reach it.
//
// NO REQUIRES, and that is a property rather than a milestone. The reference
// extractor below is stdlib only -- 1.9 MB against a real parser's 14 to 37 --
// and the first dependency added here should have to argue for itself.
module github.com/AreteAcademy/brevis/sql

go 1.26.0
