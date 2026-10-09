// One module, one binary, several subcommands -- which is what a real team
// does: one image, many entrypoints.
//
// It pins a PUBLISHED SDK rather than pointing at the tree, so this example is
// what somebody gets from `go get`. The repository's other examples do the
// opposite on purpose: they are a gate on the working tree.
//
// AND THE PIN HAS TO MOVE. It sat at v0.56.0, which is before the SDK said
// which destination a load wrote -- `landed` first shipped in v0.81.0 -- so
// this example ran correctly, wrote its files, and put NOTHING on /data.
// Anybody following it saw an empty catalog and no way to tell that from a
// broken one.
module github.com/AreteAcademy/brevis/examples/full-pipeline/pipeline

go 1.26.0

require github.com/AreteAcademy/brevis/sdk v0.84.1

require github.com/google/uuid v1.6.0 // indirect
