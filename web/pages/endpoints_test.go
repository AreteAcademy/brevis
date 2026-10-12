package pages

import (
	"strings"
	"testing"

	"github.com/AreteAcademy/brevis/internal/domain/catalog"
	"github.com/AreteAcademy/brevis/internal/infrastructure/postgres"
)

// WHAT A PERIOD ACCEPTS IS SAID ONCE ON THE PAGE: the contract's text and
// Try it's select are the same list, so the page cannot offer a period it
// tells the reader does not exist.
func TestAPeriodsContractAndItsSelectAreTheSameList(t *testing.T) {
	params := []catalog.EndpointParam{{Name: "period", Type: "period"}}
	v := BuildEndpoint(postgres.PublishedEndpoint{Endpoint: catalog.Endpoint{Params: params}})
	options := TryForm(params, nil)[0].Options
	if v.Params[0].Accepts != strings.Join(options, ", ") {
		t.Errorf("the contract says %q and the select offers %v", v.Params[0].Accepts, options)
	}
}
