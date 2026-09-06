package core

import "context"

// DestinationChecker is implemented by a destination that can check the
// declaration against the real table BEFORE the extraction happens.
//
// The check already existed -- it runs in Write, with the batch in hand. The
// problem is the timing: on a vendor with a quota, getting there means having
// spent the whole window's quota to find out that a column does not match. It
// is invariant I3 of plan/2026-09-03-sdk-schema-declarado.md.
//
// Optional on purpose. A directory of files has no schema to check, and
// Redshift would need a running cluster -- and a destination that cannot check
// early must not be forced to pretend it can.
type DestinationChecker interface {
	// CheckDestination checks the declaration against the real destination.
	//
	// It takes the declared names; it returns an error naming the column and
	// both sides. A destination that does not exist yet is NOT an error here:
	// creating a table is Write's decision, and refusing earlier would take
	// CreateTable out of the path.
	CheckDestination(ctx context.Context, columns []string) error
}
