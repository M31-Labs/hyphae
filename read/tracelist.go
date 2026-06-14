package read

import (
	"m31labs.dev/hyphae/internal/trace"
	"m31labs.dev/hyphae/internal/types"
)

// listActiveTraces wraps internal/trace.List with ActiveOnly:true.
// Separated into its own file to keep the import of the trace package isolated.
func listActiveTraces(spaceRoot string) ([]types.Trace, error) {
	return trace.List(spaceRoot, trace.ListFilter{ActiveOnly: true})
}
