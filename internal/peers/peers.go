// Package peers manages the install-root-level peer list — known
// remote Hyphae endpoints a user has paired with (`hypha peer add`).
//
// Phase 3 keeps it deliberately simple: a single JSON file at
// <install-root>/.peers.json. Phase 4's hub will read the same file
// to decide who's allowed to subscribe to which space.
package peers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/atomicfs"
)

// Filename is the per-install-root peer file under HYPHAE_HOME.
const Filename = ".peers.json"

// Peer is one known remote endpoint.
type Peer struct {
	Name    string    `json:"name"`
	URI     string    `json:"uri"`     // ws://host:port, https://host:port, etc.
	AddedAt time.Time `json:"added_at"`
}

// List reads the peer file at <installRoot>/.peers.json. Returns an
// empty list (not an error) when the file is absent.
func List(installRoot string) ([]Peer, error) {
	data, err := os.ReadFile(filepath.Join(installRoot, Filename))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("peers: read: %w", err)
	}
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("peers: parse: %w", err)
	}
	return doc.Peers, nil
}

// Add appends a peer. Returns an error if a peer with the same name
// or URI is already present.
func Add(installRoot, name, uri string) (Peer, error) {
	uri = strings.TrimSpace(uri)
	name = strings.TrimSpace(name)
	if uri == "" {
		return Peer{}, errors.New("peers: uri required")
	}
	if name == "" {
		name = deriveName(uri)
	}
	current, err := List(installRoot)
	if err != nil {
		return Peer{}, err
	}
	for _, p := range current {
		if p.URI == uri {
			return Peer{}, fmt.Errorf("peers: uri %q already present (name %q)", uri, p.Name)
		}
		if p.Name == name {
			return Peer{}, fmt.Errorf("peers: name %q already in use (uri %q)", name, p.URI)
		}
	}
	added := Peer{Name: name, URI: uri, AddedAt: time.Now().UTC()}
	current = append(current, added)
	if err := write(installRoot, current); err != nil {
		return Peer{}, err
	}
	return added, nil
}

// Remove deletes the peer whose name OR uri matches needle. Returns
// the removed peer or an error if not found.
func Remove(installRoot, needle string) (Peer, error) {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return Peer{}, errors.New("peers: name-or-uri required")
	}
	current, err := List(installRoot)
	if err != nil {
		return Peer{}, err
	}
	var kept []Peer
	var removed *Peer
	for i := range current {
		if removed == nil && (current[i].Name == needle || current[i].URI == needle) {
			cp := current[i]
			removed = &cp
			continue
		}
		kept = append(kept, current[i])
	}
	if removed == nil {
		return Peer{}, fmt.Errorf("peers: no peer matches %q", needle)
	}
	if err := write(installRoot, kept); err != nil {
		return Peer{}, err
	}
	return *removed, nil
}

// ─── internals ────────────────────────────────────────────────────────────

type fileDoc struct {
	Peers []Peer `json:"peers"`
}

func write(installRoot string, list []Peer) error {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	doc := fileDoc{Peers: list}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("peers: marshal: %w", err)
	}
	return atomicfs.WriteFile(filepath.Join(installRoot, Filename), append(data, '\n'), 0o644)
}

// deriveName builds a default name from a URI: scheme://<host>/<path>
// → host (or first path segment if host is missing).
func deriveName(uri string) string {
	s := uri
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	s = strings.TrimSuffix(s, "/")
	if idx := strings.IndexAny(s, "/:"); idx >= 0 {
		s = s[:idx]
	}
	if s == "" {
		return "peer"
	}
	return s
}
