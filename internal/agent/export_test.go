package agent

import "sync"

// Pending exposes what a drain is still waiting for, for the race test: from
// outside, the only other way to see it is to wait and time out.
func (a *Agent) Pending() []string { return a.pending() }

// PersistDropped writes an execution that is no longer the one kept under its
// id -- a write that lost the race to a prune or to a new start of the id.
func (a *Agent) PersistDropped(id string) {
	e := &execution{id: id, ring: a.opt.RingSize, first: 1, next: 1,
		closed: true, exited: true, settled: true, finishedAt: a.opt.Now()}
	e.cond = sync.NewCond(&e.mu)
	a.persist(e)
}
