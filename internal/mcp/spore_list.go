package mcp

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"m31labs.dev/hyphae/internal/spore"
)

func osReadDir(dir string) ([]os.DirEntry, error) {
	return os.ReadDir(dir)
}

// sporeList enumerates inbox spores across installed spaces, matching the
// filters the cmd/hypha `spore list` command applies. Kept here so the MCP
// package doesn't import cmd/hypha.
func sporeList(installRoot, spaceFilter, statusFilter string, limit int) ([]sporeRow, error) {
	spaces, err := listSpaces(installRoot)
	if err != nil {
		return nil, err
	}
	var out []sporeRow
	for _, sp := range spaces {
		if spaceFilter != "" && !spaceMatches(sp, spaceFilter) {
			continue
		}
		inbox := filepath.Join(sp.Path, "inbox", "agents")
		entries, _ := os.ReadDir(inbox)
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
				continue
			}
			absPath := filepath.Join(inbox, ent.Name())
			content, rerr := os.ReadFile(absPath)
			if rerr != nil {
				continue
			}
			s, _ := spore.Parse(content)
			if s.ID == "" {
				continue
			}
			if statusFilter != "" && s.Status != statusFilter {
				continue
			}
			out = append(out, sporeRow{
				ID:          s.ID,
				Space:       s.SpaceID,
				Status:      s.Status,
				Path:        absPath,
				SubmittedAt: s.SubmittedAt,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SubmittedAt.After(out[j].SubmittedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
