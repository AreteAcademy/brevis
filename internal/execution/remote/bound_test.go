package remote_test

import (
	"testing"

	"github.com/AreteAcademy/brevis/internal/execution/remote"
)

// An agent says where to come back to, and the engine does not simply believe
// it.
//
// The advertisement makes the engine POST to an address the AGENT chose,
// carrying the shared token. The token is not the new exposure -- every agent
// already receives it on every request -- but an arbitrary outbound POST from
// the engine is a capability that did not exist before this mechanism.
//
// SCHEME AND PORT ARE NOT A BOUND, and the first version of this test is what
// said so: `http://attacker.example.com:9443` has the same scheme and the same
// port as the configured address, so a rule that checked only those would have
// allowed every host on earth. It would have looked like a bound and been one
// comparison away from nothing.
//
// What actually separates a replica from a stranger is the DOMAIN. A pool
// differs in the first DNS label -- `brevis-agent-2` instead of
// `brevis-agent-0` -- and shares everything after it. So the rule is: same
// scheme, same domain. It needs no second configuration, because the bound is
// derived from the address an operator already wrote into BREVIS_HOSTS.
//
// THE PORT IS DELIBERATELY NOT PART OF IT, which was the second correction.
// `Service port 80 -> targetPort 9443` is an ordinary topology: the configured
// address carries the Service's port and the advertisement carries the pod's,
// so a port equality refuses a pool that is correctly deployed. What it would
// have bought is stopping a redirect to another PORT inside the same domain --
// real, much smaller, and not worth refusing a legitimate deployment for.
func TestAnAdvertisementIsBoundedByTheConfiguredAddress(t *testing.T) {
	const configured = "http://brevis-agent-0.brevis-agent.dados.svc:9443"

	for _, tc := range []struct {
		name      string
		advertise string
		want      string
		why       string
	}{
		{
			name:      "another replica of the same pool",
			advertise: "http://brevis-agent-2.brevis-agent.dados.svc:9443",
			want:      "http://brevis-agent-2.brevis-agent.dados.svc:9443",
			why:       "a pool differs in the first label and nothing else",
		},
		{
			name:      "a stranger on the same port",
			advertise: "http://attacker.example.com:9443",
			want:      configured,
			why:       "the case scheme-and-port alone would have allowed",
		},
		{
			name:      "a stranger one label deeper",
			advertise: "http://x.brevis-agent.dados.svc.attacker.com:9443",
			want:      configured,
			why:       "a suffix match on the string would have allowed this",
		},
		{
			// ALLOWED, and deliberately: see the port paragraph above. A
			// Service on 80 in front of pods on 9443 is the ordinary case this
			// protects.
			name:      "a different port in the same domain",
			advertise: "http://brevis-agent-2.brevis-agent.dados.svc:9999",
			want:      "http://brevis-agent-2.brevis-agent.dados.svc:9999",
			why:       "a Service port and a pod port legitimately differ",
		},
		{
			name:      "a different scheme",
			advertise: "https://brevis-agent-2.brevis-agent.dados.svc:9443",
			want:      configured,
		},
		{
			name:      "not a URL at all",
			advertise: "::nonsense::",
			want:      configured,
		},
		{
			name:      "nothing advertised, which is one agent at one address",
			advertise: "",
			want:      configured,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := remote.HTTPAgent{BaseURL: configured}
			if got := a.At(tc.advertise); got != tc.want {
				t.Errorf("At(%q) = %q, want %q. %s", tc.advertise, got, tc.want, tc.why)
			}
		})
	}
}

// A configured address with no domain to share -- a bare host, which is what a
// compose stack or an IP looks like -- allows only itself.
//
// There is no first label to vary, so there is no pool to allow, and the
// honest answer is that an advertisement can only confirm the address the
// installation already has.
func TestAnAddressWithNoDomainAllowsOnlyItself(t *testing.T) {
	for _, configured := range []string{"http://agent:9443", "http://10.0.3.7:9443"} {
		a := remote.HTTPAgent{BaseURL: configured}
		if got := a.At("http://somewhere-else:9443"); got != configured {
			t.Errorf("with %s configured, At allowed %q", configured, got)
		}
		if got := a.At(configured); got != configured {
			t.Errorf("with %s configured, At refused the same address: %q", configured, got)
		}
	}
}
