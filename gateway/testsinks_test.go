package gateway_test

import (
	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/gateway/metastore/memcached"
	"github.com/AreteAcademy/brevis/gateway/metastore/redis"
	"github.com/AreteAcademy/brevis/gateway/sink/autotable"
	"github.com/AreteAcademy/brevis/gateway/sink/bigquery"
	"github.com/AreteAcademy/brevis/gateway/sink/files"
	"github.com/AreteAcademy/brevis/gateway/sink/mysql"
	"github.com/AreteAcademy/brevis/gateway/sink/postgres"
	"github.com/AreteAcademy/brevis/gateway/sink/pubsub"
	"github.com/AreteAcademy/brevis/gateway/sink/redshift"
	"github.com/AreteAcademy/brevis/gateway/store/gcs"
	"github.com/AreteAcademy/brevis/gateway/store/s3"
)

// everything is what cmd/gateway registers, so the tests exercise the
// published image's build rather than a set of their own.
//
// A set of their own is the trap: the tests would pass on sinks the image does
// not carry, and the first person to find out would be whoever deployed it.
func everything() []gateway.Option {
	sinks := gateway.NewSinks()
	sinks.MustRegister(pubsub.Sink, pubsub.New)
	sinks.MustRegister(postgres.Sink, postgres.New)
	sinks.MustRegister(mysql.Sink, mysql.New)
	sinks.MustRegister(bigquery.Sink, bigquery.New)
	sinks.MustRegister(redshift.Sink, redshift.New)
	sinks.MustRegister(files.Sink, files.New)
	sinks.MustRegister(autotable.Sink, autotable.New)

	metastores := gateway.NewMetastores()
	metastores.MustRegister(redis.Name, redis.Open)
	metastores.MustRegister(memcached.Name, memcached.Open)

	stores := gateway.NewStores()
	stores.MustRegister(s3.Scheme, s3.Open)
	stores.MustRegister(gcs.Scheme, gcs.Open)

	return []gateway.Option{gateway.WithSinks(sinks), gateway.WithStores(stores),
		gateway.WithMetastores(metastores)}
}

// with is everything() plus the options a test adds.
func with(opts ...gateway.Option) []gateway.Option {
	return append(everything(), opts...)
}
