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

// A COUNT IS WRITTEN WITH ITS THOUSANDS SEPARATED: 10,000 and not 10000,
// which somebody has to count the zeros of.
func TestACountIsWrittenWithItsThousandsSeparated(t *testing.T) {
	for n, want := range map[int]string{0: "0", 500: "500", 1000: "1,000", 10000: "10,000", 1234567: "1,234,567", -2500: "-2,500"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
}
