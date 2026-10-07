package pages

import (
	"fmt"
	"time"

	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// lastLoad is the destination's most recent load across its writers: when,
// and how many rows that load said it wrote.
func lastLoad(e postgres.CatalogEntry) (*time.Time, *int64) {
	var when *time.Time
	var rows *int64
	for i := range e.Writers {
		w := e.Writers[i]
		if when == nil || w.LastLoaded.After(*when) {
			when, rows = &e.Writers[i].LastLoaded, w.LastRows
		}
	}
	return when, rows
}

// rowsText renders a count, and says nothing for one the step did not give.
func rowsText(v *int64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprint(*v)
}
