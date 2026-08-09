package graft

import (
	"fmt"

	"m31labs.dev/hyphae/internal/types"
	"m31labs.dev/mdpp"
)

// LintFinding reports one proposed write's dry-resolution outcome.
type LintFinding struct {
	Index     int    `json:"index"`
	Kind      string `json:"kind"`
	TargetURI string `json:"target"`
	Applies   bool   `json:"applies"`
	// Reason explains a write that would not apply, in the same words a
	// real graft's SkippedWrite would use.
	Reason string `json:"reason,omitempty"`
}

// LintWrites dry-resolves proposed writes with the real graft handlers,
// so authors learn before submission exactly which writes a graft would
// skip and why. Nothing is persisted and no spore record is required;
// spaceID supplies the default target for create_file writes that omit
// one, matching Apply.
func LintWrites(installRoot, spaceID string, writes []types.ProposedWrite) []LintFinding {
	ctx := &applyContext{
		dryRun:        true,
		rollback:      map[string][]byte{},
		pending:       map[string][]byte{},
		writtenRanges: map[string][]mdpp.Range{},
	}
	findings := make([]LintFinding, 0, len(writes))
	for i, pw := range writes {
		finding := LintFinding{Index: i, Kind: pw.Kind, TargetURI: pw.Target}
		if unsupportedWriteKinds[pw.Kind] {
			finding.Reason = "unsupported write kind in v0.1.1"
			findings = append(findings, finding)
			continue
		}

		var (
			aw       *AppliedWrite
			skip     *SkippedWrite
			fatalErr error
		)
		switch pw.Kind {
		case "append_section", "insert_after":
			aw, skip, _, fatalErr = applyInsertWrite(pw, installRoot, ctx)
		case "create_file":
			if pw.Target == "" {
				pw.Target = spaceID
				finding.TargetURI = spaceID
			}
			aw, skip, _, fatalErr = applyCreateFile(pw, installRoot, ctx)
		case "replace_block":
			aw, skip, _, fatalErr = applyReplaceBlock(pw, installRoot, ctx)
		case "add_tag":
			aw, skip, _, fatalErr = applyAddTag(pw, installRoot, ctx)
		default:
			finding.Reason = fmt.Sprintf("unknown write kind %q", pw.Kind)
			findings = append(findings, finding)
			continue
		}

		switch {
		case fatalErr != nil:
			finding.Reason = fatalErr.Error()
		case skip != nil:
			finding.Reason = skip.Reason
		case aw != nil:
			finding.Applies = true
		default:
			finding.Reason = "handler returned no result"
		}
		findings = append(findings, finding)
	}
	return findings
}
