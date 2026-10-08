package workflow

import (
	"fmt"
	"strings"
	"time"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
)

// Trigger is why a workflow starts, other than the clock.
//
// A TYPE OF ITS OWN AND NOT TWO FIELDS ON Workflow, because the two only mean
// anything together: a debounce with nothing to debounce is a number nobody
// reads, and the refusal for it has to be able to say so.
//
// Additive in the stored document, like Description, Runtime, Tools and Host:
// an older engine ignores it, and this one reading an older document gets the
// zero value -- which is "no trigger", and is what every workflow published
// before today means. Nothing to migrate.
type Trigger struct {
	// OnLanded are the targets whose landing starts this workflow, in the
	// catalog's own spelling: `bigquery://project/dataset/table`.
	//
	// AN IDENTITY AND NEVER AN ADDRESS, which is the catalog's rule and the
	// reason a DSN pasted here is refused rather than used. A trailing `/*`
	// is admitted: auto_table creates a table per route, so the dataset is
	// the only thing a subscription can name for them.
	OnLanded []string

	// Debounce is how wide the window is that collapses many landings into
	// one run. Zero means every landing starts a run.
	//
	// A DURATION AND NOT A COUNT. Ten landings in a minute is the issue's
	// case, and what makes them one run is that they fall in one window --
	// counting them would need a number nobody can choose and a state nobody
	// can see.
	Debounce time.Duration
}

// Declared says whether this workflow subscribes to anything at all.
func (t Trigger) Declared() bool { return len(t.OnLanded) > 0 }

// validate refuses what a trigger must never be, naming it.
//
// IN THE DOMAIN AND NOT IN THE COMMAND. `brevis validate` and `brevis
// publish` both reach here through spec.Parse -- deliberately, so "a folder
// that validates has to be a folder that publishes" -- and a rule written
// into either command would be a rule the other does not apply.
func (t Trigger) validate(slug string) error {
	if !t.Declared() {
		if t.Debounce != 0 {
			return fmt.Errorf("workflow %q declares `debounce` and no `on_landed`: "+
				"there is nothing for the window to collapse", slug)
		}
		return nil
	}
	if t.Debounce < 0 {
		return fmt.Errorf("workflow %q: `debounce` is %s, and a window cannot be "+
			"negative. Leave it out for a run per landing", slug, t.Debounce)
	}
	for _, target := range t.OnLanded {
		if strings.TrimSpace(target) == "" {
			return fmt.Errorf("workflow %q: `on_landed` holds an empty target", slug)
		}
		// allowPattern, for the auto_table case named on the field above.
		if err := catalog.ValidTarget(target, true); err != nil {
			return fmt.Errorf("workflow %q: `on_landed` %w. A target is an identity "+
				"and never an address: no user, no host, no port", slug, err)
		}
	}
	return nil
}
