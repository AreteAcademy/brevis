// Package events names what can happen to an installation, for the console's
// events feed: the closed list of kinds, and which source each belongs to.
//
// WHERE EACH KIND COMES FROM:
//
//   - The actions are stored in the events table, each written by the action
//     itself, in its own transaction, with who did it (the console's operator,
//     "cli", or "" on a console with no login):
//     publishing a workflow, a gateway or an API; pausing or resuming a
//     schedule; pruning a workflow; taking a gateway or an API down.
//   - The runs are not stored twice: the feed reads them from runs, as
//     started when they started and as succeeded, failed or canceled when
//     they ended. A run still in the queue has not happened yet.
//
// A PUBLISH IS AN EVENT WHEN IT CHANGED SOMETHING: a new workflow, graph,
// schedule, stream configuration or endpoint. A CI that republishes
// everything on every deploy must not fill the feed with publishes that
// changed nothing.
//
// The events hold names, counts and flags, never a configuration or a
// credential. They are kept as long as the runs: `brevis prune` deletes both
// past the same PurgeAfter. A kind this version does not know -- written by a
// newer one -- has no source, and the feed leaves it out.
package events
