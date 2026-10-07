package gateway_test

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/gateway"
	"github.com/AreteAcademy/brevis/gateway/sink/autotable"
	"github.com/AreteAcademy/brevis/gateway/sink/bigquery"
	"github.com/AreteAcademy/brevis/gateway/sink/files"
	"github.com/AreteAcademy/brevis/gateway/sink/mysql"
	"github.com/AreteAcademy/brevis/gateway/sink/postgres"
	"github.com/AreteAcademy/brevis/gateway/sink/pubsub"
	"github.com/AreteAcademy/brevis/gateway/sink/redshift"
)

func locators() *gateway.Sinks {
	s := gateway.NewSinks()
	s.MustRegisterLocator(postgres.Sink, postgres.Locate)
	s.MustRegisterLocator(mysql.Sink, mysql.Locate)
	s.MustRegisterLocator(redshift.Sink, redshift.Locate)
	s.MustRegisterLocator(bigquery.Sink, bigquery.Locate)
	s.MustRegisterLocator(pubsub.Sink, pubsub.Locate)
	s.MustRegisterLocator(files.Sink, files.Locate)
	s.MustRegisterLocator(autotable.Sink, autotable.Locate)
	return s
}

// Every driver names its destination from configuration alone. Nothing here
// has a credential or a route to any service, which is the condition describe
// runs under in a deploy pipeline.
func TestEverySinkIsNamedFromItsConfigurationAlone(t *testing.T) {
	t.Setenv("PG_DSN", "postgres://loader:s3cret@db.internal:5432/analytics?sslmode=disable")
	t.Setenv("MY_DSN", "loader:s3cret@tcp(db.internal:3306)/shop")
	t.Setenv("RS_DSN", "postgres://loader:s3cret@cluster.example:5439/warehouse")

	cases := []struct {
		name string
		sink gateway.Sink
		want string
	}{
		{"postgres", gateway.Sink{Type: postgres.Sink, DSNFrom: "PG_DSN", Table: "landing.clicks"}, "postgres://analytics/landing/clicks"},
		{"mysql", gateway.Sink{Type: mysql.Sink, DSNFrom: "MY_DSN", Table: "clicks"}, "mysql://shop/clicks"},
		{"redshift", gateway.Sink{Type: redshift.Sink, DSNFrom: "RS_DSN", Table: "landing.clicks"}, "redshift://warehouse/landing/clicks"},
		{"bigquery", gateway.Sink{Type: bigquery.Sink, Project: "acme-prod", Dataset: "bronze", Table: "clicks"}, "bigquery://acme-prod/bronze/clicks"},
		{"pubsub", gateway.Sink{Type: pubsub.Sink, Project: "acme-prod", Topic: "clicks-raw"}, "pubsub://acme-prod/clicks-raw"},
		{"files", gateway.Sink{Type: files.Sink, Path: "s3://acme-archive/clicks"}, "s3://acme-archive/clicks/"},
		{"auto_table into bigquery", gateway.Sink{Type: autotable.Sink,
			Into: &gateway.Sink{Type: bigquery.Sink, Project: "acme-prod", Dataset: "bronze"}}, "bigquery://acme-prod/bronze/*"},
	}
	reg := locators()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, known, err := reg.Locate(c.sink)
			if err != nil || !known {
				t.Fatalf("Locate = %q, %v, %v", got, known, err)
			}
			if got != c.want {
				t.Fatalf("Locate = %q, want %q", got, c.want)
			}
			for _, leak := range []string{"loader", "s3cret", "db.internal", "cluster.example"} {
				if strings.Contains(got, leak) {
					t.Fatalf("%q carries %q: a target is a name, not an address", got, leak)
				}
			}
		})
	}
}

func TestAMissingDSNVariableIsNamed(t *testing.T) {
	_, _, err := locators().Locate(gateway.Sink{Type: postgres.Sink, DSNFrom: "NOT_SET_ANYWHERE", Table: "landing.clicks"})
	if err == nil || !strings.Contains(err.Error(), "NOT_SET_ANYWHERE") {
		t.Fatalf("err = %v, want one naming the variable", err)
	}
}

// What the gateway would refuse to serve, it refuses to describe.
func TestBigQueryNeedsItsProjectAndDatasetAsTheGatewayDoes(t *testing.T) {
	t.Setenv("GOOGLE_PROJECT_ID", "from-the-environment")
	if _, _, err := locators().Locate(gateway.Sink{Type: bigquery.Sink, Dataset: "bronze", Table: "clicks"}); err == nil {
		t.Fatal("an empty project was filled from the environment; the gateway refuses it")
	}
}

func TestASinkThisBinaryCannotDescribeIsUnknownNotAnError(t *testing.T) {
	_, known, err := locators().Locate(gateway.Sink{Type: "kafka"})
	if known || err != nil {
		t.Fatalf("known=%v err=%v, want unknown and no error", known, err)
	}
}

func TestPatternReplacesTheLastSegment(t *testing.T) {
	if got := gateway.Pattern("postgres://analytics/landing/route"); got != "postgres://analytics/landing/*" {
		t.Fatalf("Pattern = %q", got)
	}
}
