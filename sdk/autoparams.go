package sdk

import "github.com/AreteAcademy/brevis/sdk/internal/core"

// AutoParams are what the Brevis engine worked out about this run. Reached
// through Pipeline.Run.Auto, or through RunContextFromEnv().Auto in a step that
// is not a pipeline.
//
// The one to use is Now(): it is the clock this run should read instead of
// time.Now(), and it does not move when the run is late or retried.
//
//	rc := sdk.RunContextFromEnv()
//	start, end, ok := rc.Auto.Window()
//	if !ok {
//		start, end = rc.Auto.Now().Add(-24*time.Hour), rc.Auto.Now()
//	}
type AutoParams = core.AutoParams

// EnvAutoParams is the variable the engine injects, as JSON. The engine also
// exports one BREVIS_AUTO_* variable per value for steps that would rather not
// parse anything.
const EnvAutoParams = core.EnvAutoParams
