// A module of ITS OWN, and not a package of the root: a fetcher imports the
// SDK, and the SDK brings BigQuery, S3 and MySQL behind it. Hanging it off the
// engine's go.mod would drag that tree into the module engine-weight.sh exists
// to keep lean.
module testfetcher

go 1.23.0

require github.com/AreteAcademy/brevis/sdk v0.0.0

require github.com/google/uuid v1.6.0 // indirect

replace github.com/AreteAcademy/brevis/sdk => ../../../../../sdk
