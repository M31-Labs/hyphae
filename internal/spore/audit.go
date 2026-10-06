package spore

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
)

type AuditCounts struct {
	Total            int `json:"total"`
	Valid            int `json:"valid"`
	V2               int `json:"v2"`
	V1               int `json:"v1"`
	V0Legacy         int `json:"v0_legacy"`
	Invalid          int `json:"invalid"`
	Unsigned         int `json:"unsigned"`
	ProposalMismatch int `json:"proposal_mismatch"`
}

type AuditCase struct {
	File    string `json:"file"`
	SporeID string `json:"spore_id,omitempty"`
	VerificationReport
}

type AuditReport struct {
	Counts AuditCounts `json:"counts"`
	Cases  []AuditCase `json:"cases"`
}

// Audit scans inboxes and canonical documents using only public identities.
// Cases contains INVALID results, including localized proposal mismatches.
// Files are counted individually, including archived copies of the same id.
func Audit(dir string, resolve IdentityResolver) (AuditReport, error) {
	report := AuditReport{Cases: []AuditCase{}}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || (filepath.Ext(path) != ".md" && filepath.Ext(path) != ".mdpp") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !auditCandidate(source) {
			return nil
		}
		result, _ := VerifyDetailed(source, resolve)
		report.Counts.Total++
		switch result.Status {
		case "VALID":
			report.Counts.Valid++
			if result.Version == 1 {
				report.Counts.V1++
			} else {
				report.Counts.V2++
			}
		case "V0_LEGACY":
			report.Counts.V0Legacy++
		case "UNSIGNED":
			report.Counts.Unsigned++
		default:
			report.Counts.Invalid++
			if len(result.ProposalMismatch) > 0 {
				report.Counts.ProposalMismatch++
			}
			id, _ := FrontmatterString(source, "id")
			report.Cases = append(report.Cases, AuditCase{File: path, SporeID: id, VerificationReport: result})
		}
		return nil
	})
	return report, err
}

var auditHeader = regexp.MustCompile(`(?m)^[ \t]*["']?(type["']?[ \t]*:[ \t]*["']?spore(["']|[ \t\r]|$)|signature["']?[ \t]*:)`)

func auditCandidate(source []byte) bool {
	if IsSpore(source) || HasSignature(source) {
		return true
	}
	// A malformed YAML block cannot supply semantic type/signature values.
	// Inspect its header only, so malformed claims still appear as INVALID.
	opening := bytes.IndexByte(source, '\n')
	if opening < 0 || string(bytes.TrimSpace(source[:opening])) != "---" {
		return false
	}
	header := source[opening+1:]
	for offset := 0; offset < len(header); {
		end := bytes.IndexByte(header[offset:], '\n')
		if end < 0 {
			end = len(header) - offset
		}
		end += offset
		if string(bytes.TrimSpace(header[offset:end])) == "---" {
			header = header[:offset]
			break
		}
		offset = end + 1
	}
	return auditHeader.Match(header)
}
