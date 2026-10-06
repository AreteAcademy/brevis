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
// What separates a replica from a stranger is CONTAINMENT. A pool is
// configured with its SERVICE -- `brevis-agent.dados.svc` -- and each replica
// advertises its POD -- `brevis-agent-2.brevis-agent.dados.svc`, the Service's
// name with a label in FRONT. So the advertised host has to be the configured
// one or a name inside it.
//
// THE THIRD CORRECTION, and the one that mattered most: the rule before this
// compared the DOMAIN -- everything after the first label -- which assumed the
// two names differ in their first label. They do not. A pod has one label MORE
// than its Service, so the rule REFUSED the only topology in which a pool
// works. It passed every test here and would have failed in a cluster, which
// is what a question about running the real thing is worth.
//
// It is also tighter. Comparing domains let `redis.dados.svc` through when the
// Service was configured -- a sibling, not a name inside it.
//
// The PORT is deliberately not part of it, which was the second correction.
// `Service port 80 -> targetPort 9443` is an ordinary topology: the configured
// address carries the Service's port and the advertisement carries the pod's,
// so a port equality refuses a pool that is correctly deployed.
func TestAnAdvertisementIsBoundedByTheConfiguredAddress(t *testing.T) {
	const configured = "http://brevis-agent-0.brevis-agent.dados.svc:9443"

	for _, tc := range []struct {
		name      string
		advertise string
		want      string
		why       string
	}{
		{
			// Configured with a POD, which is one agent and not a pool: a
			// sibling pod is a different machine and is not inside this name.
			name:      "a sibling pod when a pod was configured",
			advertise: "http://brevis-agent-2.brevis-agent.dados.svc:9443",
			want:      configured,
			why:       "configuring one pod points at one agent, not at a pool",
		},
		{
			name:      "a stranger on the same port",
			advertise: "http://attacker.example.com:9443",
			want:      configured,
			why:       "the case scheme-and-port alone would have allowed",
		},
		{
			// THE CASE THE CODE'S OWN COMMENT NAMES, and which no test covered
			// until a mutation lived. `notbrevis-agent.dados.svc` ENDS WITH
			// `brevis-agent.dados.svc`, so a strings.HasSuffix passes it --
			// and it is a different Service that anything able to create one
			// in this namespace could stand up. Labels are compared, not
			// characters.
			name:      "a domain that merely ends with the right characters",
			advertise: "http://pod.notbrevis-agent.dados.svc:9443",
			want:      configured,
			why:       "a suffix on the string accepts a different Service",
		},
		{
			name:      "a stranger one label deeper",
			advertise: "http://x.brevis-agent.dados.svc.attacker.com:9443",
			want:      configured,
			why:       "a suffix match on the string would have allowed this",
		},
		{
			// Refused because it is a SIBLING, not because of the port. With a
			// pod configured there is no pool to allow. The port's own case
			// lives in TestThePoolTopologyIsAllowed, where it matters.
			name:      "a sibling pod on another port",
			advertise: "http://brevis-agent-2.brevis-agent.dados.svc:9999",
			want:      configured,
			why:       "a sibling is refused whatever port it names",
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

// THE TOPOLOGY A POOL ACTUALLY HAS, and the one an earlier rule refused.
//
// `BREVIS_HOSTS` carries the SERVICE, because that is what spreads the starts
// across replicas -- configuring one pod sends every start to that pod and the
// others never see work. Each replica then advertises its own POD, whose name
// is the Service's with a label in front.
//
// The rule before this compared domains and refused exactly this. Every test
// passed; a cluster would not have.
func TestThePoolTopologyIsAllowed(t *testing.T) {
	a := remote.HTTPAgent{BaseURL: "http://brevis-agent.dados.svc:9443"}

	for _, pod := range []string{
		"http://brevis-agent-0.brevis-agent.dados.svc:9443",
		"http://brevis-agent-2.brevis-agent.dados.svc:9443",
	} {
		if got := a.At(pod); got != pod {
			t.Errorf("At(%q) = %q: the engine cannot come back to the replica "+
				"that took the work, which is what the advertisement is for", pod, got)
		}
	}

	// A pod on a DIFFERENT port is still a name inside the Service, which is
	// the `Service port 80 -> targetPort 9443` case the port was left out of
	// the rule for.
	if pod := "http://brevis-agent-1.brevis-agent.dados.svc:9443"; a.At(pod) != pod {
		t.Error("a pod of this Service was refused over its port")
	}

	// And a sibling of the SERVICE is not a name inside it.
	for _, stranger := range []string{
		"http://redis.dados.svc:9443",
		"http://pod.notbrevis-agent.dados.svc:9443",
		"http://brevis-agent.dados.svc.attacker.com:9443",
	} {
		if got := a.At(stranger); got == stranger {
			t.Errorf("At(%q) followed it", stranger)
		}
	}
}
