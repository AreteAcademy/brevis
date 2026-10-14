package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A HOOK THAT IGNORES ITS CONTEXT IS STILL BOUNDED BY IT: the request gives
// up at the endpoint's timeout and answers 504, whatever the hook is doing.
func TestABeforeThatHangsIsA504AtTheTimeout(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	hang := func(context.Context, *Request) error { <-gate; return nil }
	e := daily(t, hang)
	e.timeout = 50 * time.Millisecond
	s := beforeServer(t, &warehouse{cols: []string{"k"}}, e, nil)
	started := time.Now()
	rec := get(s, "/v1/daily")
	if rec.Code != http.StatusGatewayTimeout || time.Since(started) > 2*time.Second {
		t.Fatalf("status %d after %s", rec.Code, time.Since(started))
	}
	if body := scrape(t, s); !strings.Contains(body, `hook_total{endpoint="daily",hook="before",outcome="timeout"} 1`) {
		t.Errorf("the timeout was not counted:\n%s", body)
	}
}

func TestAnAfterThatHangsIsA504AtTheTimeout(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	e := volume(t, func(context.Context, *Result) (any, error) { <-gate; return nil, nil })
	e.timeout = 50 * time.Millisecond
	s := beforeServer(t, &warehouse{cols: []string{"day", "n"}}, e, nil)
	if rec := get(s, "/v1/volume"); rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status %d", rec.Code)
	}
	if body := scrape(t, s); !strings.Contains(body, `hook_total{endpoint="volume",hook="after",outcome="timeout"} 1`) {
		t.Errorf("the timeout was not counted:\n%s", body)
	}
}

// AN ABANDONED HOOK KEEPS ITS TENANT'S SLOT until it really returns. That is
// what bounds the goroutines a hanging hook can leave behind: four per
// tenant, and then that tenant is told 429 -- not every tenant a 503, and
// not a process that grows until it is killed.
func TestHangingHooksHoldTheirTenantsSlotsUntilTheyReturn(t *testing.T) {
	for name, build := range map[string]func(chan struct{}) *Endpoint{
		"before": func(gate chan struct{}) *Endpoint {
			return daily(t, func(context.Context, *Request) error { <-gate; return nil })
		},
		"after": func(gate chan struct{}) *Endpoint {
			return volume(t, func(context.Context, *Result) (any, error) { <-gate; return 1, nil })
		},
	} {
		t.Run(name, func(t *testing.T) {
			gate := make(chan struct{})
			e := build(gate)
			e.timeout = 30 * time.Millisecond
			s := beforeServer(t, &warehouse{cols: []string{"day", "n"}}, e, nil)
			for i := 0; i < perTenantInFlight; i++ {
				if rec := get(s, e.Path); rec.Code != http.StatusGatewayTimeout {
					t.Fatalf("hung request %d answered %d", i, rec.Code)
				}
			}
			if rec := get(s, e.Path); rec.Code != http.StatusTooManyRequests {
				t.Fatalf("with every slot held by a hung hook, the next request answered %d", rec.Code)
			}
			close(gate)
			deadline := time.Now().Add(2 * time.Second)
			for {
				if rec := get(s, e.Path); rec.Code == http.StatusOK {
					break
				} else if time.Now().After(deadline) {
					t.Fatalf("the slots were not given back once the hooks returned: %d", rec.Code)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// A HOOK THAT RETURNS IN TIME GIVES ITS SLOT BACK AT ONCE.
func TestAHookInTimeHoldsNoSlotAfterwards(t *testing.T) {
	s := beforeServer(t, &warehouse{cols: []string{"day", "n"}}, daily(t, dayFirst), nil)
	for i := 0; i < 2*perTenantInFlight; i++ {
		if rec := get(s, "/v1/daily"); rec.Code != http.StatusOK {
			t.Fatalf("request %d answered %d", i, rec.Code)
		}
	}
}
