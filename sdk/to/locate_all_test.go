package to_test

import (
	"testing"

	"github.com/AreteAcademy/brevis/sdk"
	"github.com/AreteAcademy/brevis/sdk/to"
	"github.com/AreteAcademy/brevis/sdk/to/bigquery"
	"github.com/AreteAcademy/brevis/sdk/to/mysql"
	"github.com/AreteAcademy/brevis/sdk/to/postgres"
	"github.com/AreteAcademy/brevis/sdk/to/pubsub"
	"github.com/AreteAcademy/brevis/sdk/to/redshift"
)

// Every writer this SDK ships can name its destination. A writer added without
// Locate would load correctly and never appear on /data, and nothing else
// would notice: add it to this list.
func TestEveryShippedWriterIsALocator(t *testing.T) {
	writers := map[string]sdk.Writer{
		"to.Files":       to.Files{},
		"bigquery.Table": bigquery.Table{},
		"postgres.Table": postgres.Table{},
		"mysql.Table":    mysql.Table{},
		"redshift.Table": redshift.Table{},
		"pubsub.Topic":   pubsub.Topic{},
	}
	for name, w := range writers {
		if _, ok := w.(sdk.Locator); !ok {
			t.Errorf("%s does not implement sdk.Locator", name)
		}
	}
}
