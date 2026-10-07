package sdk

import "github.com/AreteAcademy/brevis/sdk/internal/core"

// checkRowForTest runs the check every driver runs, over one row.
//
// Through core rather than through a driver on purpose: five drivers call it
// with the same four arguments, and a test that went through one of them would
// prove the fix for that one.
func checkRowForTest(declared []string, s Schema, row any) error {
	return core.CheckRow(declared, s, []core.Envelope{{Payload: row}}, nil)
}
