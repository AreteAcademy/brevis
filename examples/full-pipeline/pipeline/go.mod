// One module, one binary, several subcommands -- which is what a real team
// does: one image, many entrypoints.
//
// It pins a PUBLISHED SDK rather than pointing at the tree, so this example is
// what somebody gets from `go get`. The repository's other examples do the
// opposite on purpose: they are a gate on the working tree.
module github.com/AreteAcademy/brevis/examples/full-pipeline/pipeline

go 1.23.0

require github.com/AreteAcademy/brevis/sdk v0.56.0

require github.com/google/uuid v1.6.0 // indirect
