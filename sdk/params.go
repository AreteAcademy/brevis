package sdk

import (
	"os"
	"strings"

	core "github.com/AreteAcademy/brevis/sdk/internal/core"
)

// Param reads one of the run's parameters.
//
//	func main() {
//		sdk.Run(pipeline(sdk.Param("since")))
//	}
//
// It is the package-level form of RunContext.Params, for the case Pipeline.Flags
// cannot serve: a value needed to BUILD the pipeline, before sdk.Run is called
// and therefore before any flag has been parsed.
//
// An absent parameter is the empty string, never a panic, so a fetcher run by
// hand does not have to know this exists.
func Param(name string) string { return paramValue(name) }

// ParamList reads a parameter the workflow declared as `list|<type>`.
//
//	func main() {
//		sdk.Run(pipeline(sdk.ParamList("ufs")))
//	}
//
// The engine sends every parameter as a string -- one map from the trigger form
// to this process -- so a list arrives as "SP,RJ" and this splits it, trimming
// the space a form leaves after a comma. The engine already refused an empty
// item, a repeated one and a comma inside an item at trigger time.
//
// It returns nil for a parameter that is absent or empty: an empty list rather
// than a list holding one empty string, because ranging over "nothing to do"
// should do nothing.
func ParamList(name string) []string {
	raw := strings.TrimSpace(paramValue(name))
	if raw == "" {
		return nil
	}
	out := make([]string, 0, strings.Count(raw, ",")+1)
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// paramValue reads the environment first and the command line second.
//
// The environment wins because under the engine it IS the value: a `-param`
// left in a manifest must not quietly override what the operator typed in the
// trigger form. The command line exists for the other half of a fetcher's life
// -- the laptop -- where there is no engine to set anything:
//
//	./fetch-stations -param ufs=SP,RJ -param since=2026-01-01
//
// Without it, developing a fetcher that takes parameters would mean composing
// BREVIS_RUN_PARAMS as JSON by hand on every run, and what people actually do
// then is hardcode the value "just for now".
func paramValue(name string) string {
	if v, ok := core.RunContextFromEnv().Params[name]; ok && v != "" {
		return v
	}
	return paramFromArgs(os.Args[1:], name)
}

// paramFromArgs scans for `-param name=value`, in both the separated and the
// joined spelling, and in both the one-dash and two-dash forms.
//
// It is a scan and not a flag.FlagSet because of WHEN it runs: Param is called
// while main is building the pipeline, and the FlagSet only parses inside
// Execute, further down. Registering `-param` there as well is what keeps the
// two consistent -- the flag appears in -h and is not refused as unknown.
//
// The last occurrence wins, which is what a reader expects from a repeated flag
// and what shells do with a repeated assignment.
func paramFromArgs(args []string, name string) string {
	prefix := name + "="
	found := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a != "-param" && a != "--param" {
			// -param=ufs=SP,RJ -- one token, two equals signs.
			if rest, ok := strings.CutPrefix(a, "-param="); ok {
				if v, ok := strings.CutPrefix(rest, prefix); ok {
					found = v
				}
			} else if rest, ok := strings.CutPrefix(a, "--param="); ok {
				if v, ok := strings.CutPrefix(rest, prefix); ok {
					found = v
				}
			}
			continue
		}
		if i+1 >= len(args) {
			break
		}
		i++
		if v, ok := strings.CutPrefix(args[i], prefix); ok {
			found = v
		}
	}
	return found
}
