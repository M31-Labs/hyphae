package read

// ActiveTraces returns all open traces fanned out across all spaces registered
// in the index. Traces are NOT stored in the SQLite index — they live on disk
// under <space-root>/.trace/<YYYY-MM-DD>/<id>.md — so this method:
//
//  1. Calls Spaces() to enumerate root paths from the index.
//  2. Calls internal/trace.List(root, ListFilter{ActiveOnly:true}) for each.
//  3. Merges and returns the results as plain Trace DTOs.
//
// Spaces with an empty RootPath are skipped; a non-empty RootPath requires the
// on-disk space directory to exist (Spaces() verifies this via os.Stat).
// This avoids shelling out (more portable, testable, no PATH dependency) while
// staying purely read-only. Returning an empty slice with no error is the
// correct behaviour when no active traces are found.
func (r *Reader) ActiveTraces() ([]Trace, error) {
	spaces, err := r.Spaces()
	if err != nil {
		return nil, err
	}

	var out []Trace
	for _, sp := range spaces {
		if sp.RootPath == "" {
			continue
		}
		traces, err := listActiveTraces(sp.RootPath)
		if err != nil {
			// Best-effort: skip spaces where the trace directory doesn't exist
			// or is unreadable. Errors reading individual trace files inside
			// List are already swallowed by the internal implementation.
			continue
		}
		for _, tr := range traces {
			out = append(out, Trace{
				ID:           tr.ID,
				SpaceID:      tr.SpaceID,
				AgentID:      tr.AgentID,
				AgentParent:  tr.AgentParent,
				AgentSession: tr.AgentSession,
				TaskRef:      tr.TaskRef,
				Phase:        tr.Phase,
				Status:       tr.Status,
				Started:      tr.Started,
				LastTick:     tr.LastTick,
				LinkedSpore:  tr.LinkedSpore,
				FilePath:     tr.FilePath,
			})
		}
	}
	return out, nil
}
