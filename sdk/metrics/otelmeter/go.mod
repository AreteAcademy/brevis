// A module of its own, and that is the whole point.
//
// sdk/metrics/otelmeter carries the OTLP exporter, which carries protobuf and
// gRPC. Putting it in sdk/go.mod made `go mod tidy` resolve the WHOLE graph
// upward -- BigQuery 1.50 to 1.72, storage 1.30 to 1.56 -- and a consumer of
// sdk/to/bigquery went from 460 packages to 736 without importing anything new.
// The module graph moved underneath them.
//
// Package-level pruning does not protect a consumer from a MODULE-level version
// bump. A separate go.mod is what makes "you only pay for what you import" true
// at the module boundary too.
//
// It requires a PUBLISHED SDK version and carries no `replace`: a replace is
// ignored by consumers, so a module that needs one to build is a module that
// does not build for anybody else (see 3142c16).
module github.com/AreteAcademy/brevis/sdk/metrics/otelmeter

go 1.26.0

require (
	github.com/AreteAcademy/brevis/sdk v0.54.0
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.46.0
	go.opentelemetry.io/otel/metric v1.46.0
	go.opentelemetry.io/otel/sdk/metric v1.46.0
	go.opentelemetry.io/proto/otlp v1.11.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.30.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/sdk v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/telemetry v0.0.0-20260902144106-3ef544be8421 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260904194346-d0f1323225a4 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260904194346-d0f1323225a4 // indirect
	google.golang.org/grpc v1.83.1 // indirect
)
