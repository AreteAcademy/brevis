// Package gateway is an HTTP endpoint that lands data.
//
// It is NOT the engine, and the separation is the design rather than tidiness.
// `engine-weight.sh` states the rule it follows: "the data drivers -- pgx on
// the fetcher's side, BigQuery, S3, MySQL -- live in the TASKS' pods, which are
// other images". The engine orchestrates and never touches customer data; this
// does nothing else, on every request. So it is a module of its own, the way
// sdk/metrics/otelmeter is, and the engine's binary never links it.
//
// The two also differ in every property that decides a design:
//
//	               engine                 gateway
//	shape          batch, scheduled       online, per request
//	unit           a run                  an event
//	the SLO        did the run finish     p99 of POST, nothing lost on a 200
//	failure        retry the run          the client is already gone
//	scaling        one scheduler          N stateless replicas
//
// # What it reuses, and it is most of the work
//
// Every `to/` driver in the SDK already takes a BATCH --
// `Write(ctx, []Envelope, WriteOptions)` -- which is exactly what a
// micro-batching sink wants. So the gateway drives them directly, with no
// Pipeline around them, and inherits destinations that are already tested and
// already refuse what they cannot do.
//
// It also inherits `ingestion_id`: the frozen UUID v5 over
// provider|entity|source_key|record_ts. A client that retries a POST produces
// the same id, so a sink with dedup absorbs the retry -- and a row landed here
// is the same row a batch fetcher would land for the same record.
package gateway
