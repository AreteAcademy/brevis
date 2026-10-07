// A module of its own, and that is the demonstration.
//
// This binary does not import Brevis, does not depend on the SDK, and is not
// built by the engine's release. It is YOURS: the engine sends it a command
// and reads its output, which is the whole coupling between the two.
//
// Nested under examples/ rather than inside that module because the Docker
// build's context is this directory alone -- a module with no requirements
// builds with no network, which is what makes `make cluster-goapp` work on a
// laptop with the wifi off.
module github.com/AreteAcademy/brevis/examples/cluster/goapp

go 1.27.0
