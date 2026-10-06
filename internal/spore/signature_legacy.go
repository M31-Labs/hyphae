package spore

import (
	"fmt"
	"strings"

	"m31labs.dev/hyphae/internal/identity"
	"m31labs.dev/mdpp"
)

func isUnsignedMarker(value any) bool {
	s, ok := value.(string)
	return ok && s == "none"
}

func verifyLegacy(source []byte, sig Signature, id identity.Identity, signature []byte, report VerificationReport) (VerificationReport, error) {
	doc, err := mdpp.Parse(source)
	if err != nil {
		return report, err
	}
	fm := doc.Frontmatter()
	report.BodyHash = hashBytes(signableBody(extractBodyBytes(doc)))
	report.ContentHash = report.BodyHash
	if sig.ContentHash != report.BodyHash {
		report.Changed = []string{"body"}
		return report, fmt.Errorf("spore: content hash does not match body")
	}
	bodyHex := strings.TrimPrefix(report.BodyHash, "sha256:")
	check := func(substance map[string]any, fmHash string) (bool, error) {
		payload, err := legacyPayload(substance, bodyHex, fmHash)
		return err == nil && identity.Verify(id, payload, signature), err
	}
	fmHash := computeFmSubstanceHash(fm)
	report.FrontmatterHash = "sha256:" + fmHash
	if sig.Version != 0 {
		valid, err := check(fm, fmHash)
		if err != nil {
			return report, err
		}
		if valid {
			report.Status = "VALID"
			report.FrontmatterHash = "sha256:" + fmHash
			report.Note = "v1: content_hash covers body only; frontmatter verified via payload"
			return report, nil
		}
	}
	valid, err := check(fm, "")
	if err != nil {
		return report, err
	}
	if valid {
		report.Status, report.Version = "V0_LEGACY", 0
		report.FrontmatterHash = ""
		report.Recorded.Version = 0
		report.CoveredFields = []string{"agent.id", "id", "created", "authored body"}
		report.ExcludedFields = []string{"frontmatter substance", "proposed_writes", "proposed_edges", "status", "signature block", "appended trace work log"}
		report.Note = "v0 legacy: covers body only; frontmatter and proposals NOT covered"
		return report, ErrLegacyUnverified
	}
	// This proves a matching preimage without those proposal fields. It cannot
	// establish when they were added or recover the original document history.
	if sig.Version != 0 {
		for _, fields := range [][]string{{"proposed_writes"}, {"proposed_edges"}, {"proposed_writes", "proposed_edges"}} {
			candidate := make(map[string]any, len(fm))
			for k, v := range fm {
				candidate[k] = v
			}
			present := true
			for _, field := range fields {
				if _, exists := candidate[field]; !exists {
					present = false
				}
				delete(candidate, field)
			}
			if !present {
				continue
			}
			if matches, _ := check(candidate, computeFmSubstanceHash(candidate)); matches {
				report.ProposalMismatch = fields
				report.Note = "signature matches only with these proposal fields omitted; current proposals are unverified"
				break
			}
		}
	}
	return report, fmt.Errorf("spore: signature verification failed (signed content or signature changed)")
}
