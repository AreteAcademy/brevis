package agent

// Pending exposes what a drain is still waiting for, for the race test: from
// outside, the only other way to see it is to wait and time out.
func (a *Agent) Pending() []string { return a.pending() }
