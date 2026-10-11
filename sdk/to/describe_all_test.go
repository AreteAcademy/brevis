package to_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/to"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
	"github.com/AreteAcademy/brevis/sdk/to/mysql"
	"github.com/AreteAcademy/brevis/sdk/to/postgres"
	"github.com/AreteAcademy/brevis/sdk/to/pubsub"
	"github.com/AreteAcademy/brevis/sdk/to/redshift"
)

// A planted credential, in every place a DSN carries one: the user, the
// password, and a password in the query string.
const (
	pgDSN    = "postgres://loader:SEKRET@db.internal:5432/shop?password=SEKRET&sslmode=disable"
	mysqlDSN = "loader:SEKRET@tcp(db.internal:3306)/shop?parseTime=true"
)

func shippedWriters() map[string]sdk.Writer {
	return map[string]sdk.Writer{
		"to.Files":       to.Files{Path: "gs://acme-landing/orders/", Format: sdk.FormatNDJSON, Compress: true},
		"bigquery.Table": bigquery.Table{Project: "acme-prod", Dataset: "landing", Name: "orders"},
		"postgres.Table": postgres.Table{DSN: pgDSN, Name: "landing.orders"},
		"mysql.Table":    mysql.Table{DSN: mysqlDSN, Name: "orders"},
		"redshift.Table": redshift.Table{DSN: pgDSN, Name: "landing.orders", Staging: "s3://acme-staging/rs/", IAMRole: "arn:aws:iam::123:role/copy"},
		"pubsub.Topic":   pubsub.Topic{Project: "acme-prod", Name: "orders"},
	}
}

// settings is a writer's Configuration, by key.
func settings(t *testing.T, w sdk.Writer) map[string]any {
	t.Helper()
	c, ok := w.(sdk.Configured)
	if !ok {
		t.Fatalf("%T does not implement sdk.Configured", w)
	}
	out := map[string]any{}
	for _, s := range c.Configuration() {
		if _, dup := out[s.Key]; dup {
			t.Fatalf("%T lists %q twice", w, s.Key)
		}
		out[s.Key] = s.Value
	}
	return out
}

// Every writer this SDK ships says how it is set up. A writer added without it
// would land on /data as a bare type name: add it to the list above.
func TestEveryShippedWriterIsConfigured(t *testing.T) {
	for name, w := range shippedWriters() {
		if _, ok := w.(sdk.Configured); !ok {
			t.Errorf("%s does not implement sdk.Configured", name)
		}
	}
}

// THE TEST THIS TASK EXISTS FOR. Whatever a writer lists, no user, no password
// and no secret from its DSN reaches the description -- which every console
// login reads.
func TestNoWriterDescribesItsCredentials(t *testing.T) {
	for name, w := range shippedWriters() {
		raw, err := json.Marshal(w.(sdk.Configured).Configuration())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, leak := range []string{"SEKRET", "loader"} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s described %q: %s", name, leak, raw)
			}
		}
	}
}

func TestPostgresSaysWhereAndHow(t *testing.T) {
	got := settings(t, postgres.Table{DSN: pgDSN, Name: "landing.orders", CreateTable: true, Evolve: sdk.EvolveAdditive})
	want := map[string]any{
		"host": "db.internal", "port": 5432, "database": "shop", "schema": "landing", "table": "orders",
		"create_table": true, "evolve": "additive",
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

func TestMySQLSaysWhereAndHow(t *testing.T) {
	got := settings(t, mysql.Table{DSN: mysqlDSN, Name: "orders", BatchSize: 500})
	want := map[string]any{"host": "db.internal", "port": 3306, "database": "shop", "table": "orders", "batch_size": 500}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

func TestRedshiftSaysWhereAndHow(t *testing.T) {
	got := settings(t, shippedWriters()["redshift.Table"])
	want := map[string]any{
		"host": "db.internal", "port": 5432, "database": "shop", "schema": "landing", "table": "orders",
		"staging": "s3://acme-staging/rs/", "iam_role": "arn:aws:iam::123:role/copy",
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

// BigQuery's names are resolved the way its load resolves them -- set, then
// the environment, then the default -- so the page cannot show a different
// table from the one written.
func TestBigQuerySaysWhereAndHow(t *testing.T) {
	t.Setenv("BREVIS_SDK_DATASET", "")
	got := settings(t, bigquery.Table{
		Project: "acme-prod", Name: "orders",
		ClusterBy: []string{"customer_id"}, PartitionExpiration: 90 * 24 * time.Hour,
		RequirePartitionFilter: true,
	})
	want := map[string]any{
		"project": "acme-prod", "dataset": "landing", "table": "orders",
		"staging_bucket": "acme-prod-brevis-staging", "inline_limit": 5000,
		"cluster_by": []string{"customer_id"}, "partition_expiration": "2160h",
		"require_partition_filter": true,
	}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

// A function cannot be shown; that one is set can.
func TestPubSubSaysWhichFunctionsAreSet(t *testing.T) {
	got := settings(t, pubsub.Topic{Project: "acme-prod", Name: "orders",
		Attributes: func(sdk.Envelope) map[string]string { return nil }})
	if got["project"] != "acme-prod" || got["topic"] != "orders" || got["attributes"] != "set by a function" {
		t.Fatalf("settings = %v", got)
	}
	if _, ok := got["ordering_key"]; ok {
		t.Fatal("an ordering key nobody set was described")
	}
}

func TestFilesSaysWhereAndHow(t *testing.T) {
	got := settings(t, to.Files{Path: "gs://acme-landing/orders/", Format: sdk.FormatNDJSON, Compress: true, PartitionBy: "dt"})
	want := map[string]any{"path": "gs://acme-landing/orders/", "format": "ndjson", "compress": true, "partition_by": "dt"}
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}

// A location written with a key in it -- the run would fail on it, but the
// description goes out before the run checks anything.
func TestNoWriterDescribesACredentialInALocation(t *testing.T) {
	const key, secret = "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"
	writers := map[string]sdk.Writer{
		"to.Files":       to.Files{Path: "s3://" + key + ":" + secret + "@acme-landing/orders/"},
		"redshift.Table": redshift.Table{Name: "orders", Staging: "s3://" + key + ":" + secret + "@acme-staging/rs/"},
	}
	for name, w := range writers {
		raw, _ := json.Marshal(w.(sdk.Configured).Configuration())
		for _, leak := range []string{key, secret} {
			if strings.Contains(string(raw), leak) {
				t.Errorf("%s described %q: %s", name, leak, raw)
			}
		}
		if !strings.Contains(string(raw), "s3://acme-") {
			t.Errorf("%s lost the bucket itself: %s", name, raw)
		}
	}
}
