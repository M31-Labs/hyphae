// Package crdtconflict detects same-key concurrent writes in a
// Hyphae shadow's change log.
//
// A conflict is a flat key that has been Put by two or more distinct
// actors with different bytes, where neither actor's view supersedes
// the other through an explicit "resolve" Put. In the merged state
// gosx LWW silently picks one winner; this package surfaces the
// suppressed alternatives so a human can decide.
//
// Resolution is a normal Put: writing the chosen bytes again creates
// a fresh op with a fresh counter that wins LWW. Because the resolve
// op has the same value as one of the existing entries, the conflict
// self-clears on the next Detect (all entries' values are then equal).
package crdtconflict

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"m31labs.dev/gosx/crdt"
	"m31labs.dev/hyphae/internal/crdtdb"
)

// Entry is one actor's latest write to a conflicting flat key.
type Entry struct {
	ChangeHash string    `json:"change_hash"`
	ActorID    string    `json:"actor_id"`
	Time       time.Time `json:"time"`
	Message    string    `json:"message,omitempty"`
	Value      []byte    `json:"-"` // bytes; omitted from JSON to keep responses tight
	ValueLen   int       `json:"value_len"`
	OpCounter  uint64    `json:"op_counter"`
}

// Conflict groups the per-actor latest writes for one flat key.
type Conflict struct {
	ID      string  `json:"id"` // stable id = sha256-prefix of the key
	Key     string  `json:"key"`
	Prefix  string  `json:"prefix,omitempty"`  // parsed flat-key prefix when recognizable
	Tail    string  `json:"tail,omitempty"`    // parsed rest of the key
	Entries []Entry `json:"entries"`
}

// Detect walks every stored change and returns one Conflict per
// flat key with multi-actor divergent writes that haven't been
// resolved. Within an actor only the highest-op-counter Put per
// key is kept. A "conflict-resolve:<key>" commit explicitly clears
// any conflicting writes whose op-counter is ≤ the resolve's
// op-counter; subsequent diverging writes form a new active
// conflict.
func Detect(store *crdtdb.Store) ([]Conflict, error) {
	changes, err := store.AllChanges()
	if err != nil {
		return nil, err
	}
	type aKey struct{ actor, key string }
	latest := map[aKey]Entry{}
	// Highest op-counter of any "conflict-resolve:<key>" Put per key.
	// Writes with op-counter ≤ this counter are considered superseded
	// by the resolution.
	resolveCounter := map[string]uint64{}

	for _, ch := range changes {
		isResolve := false
		resolveKey := ""
		if strings.HasPrefix(ch.Message, "conflict-resolve:") {
			resolveKey = strings.TrimPrefix(ch.Message, "conflict-resolve:")
			isResolve = true
		}
		for _, op := range ch.Ops {
			if op.Action != "put" || op.Obj != crdt.Root {
				continue
			}
			key := string(op.Prop)
			if op.Value.Kind != crdt.ValueKindBytes {
				continue
			}
			if isResolve && key == resolveKey {
				if op.ID.Counter > resolveCounter[key] {
					resolveCounter[key] = op.ID.Counter
				}
			}
			ak := aKey{actor: ch.ActorID, key: key}
			cur, ok := latest[ak]
			if ok && cur.OpCounter >= op.ID.Counter {
				continue
			}
			latest[ak] = Entry{
				ChangeHash: hex.EncodeToString(ch.Hash[:]),
				ActorID:    ch.ActorID,
				Time:       ch.Time,
				Message:    ch.Message,
				Value:      append([]byte{}, op.Value.Bytes...),
				ValueLen:   len(op.Value.Bytes),
				OpCounter:  op.ID.Counter,
			}
		}
	}

	// Group entries by key and emit any with multiple actors holding
	// distinct values, after suppressing entries whose op-counter
	// fell at or below a resolve op for that key.
	byKey := map[string][]Entry{}
	for ak, e := range latest {
		if rc, ok := resolveCounter[ak.key]; ok && e.OpCounter <= rc {
			continue
		}
		byKey[ak.key] = append(byKey[ak.key], e)
	}

	var out []Conflict
	for key, entries := range byKey {
		if len(entries) <= 1 {
			continue
		}
		if allEqual(entries) {
			continue
		}
		sort.Slice(entries, func(i, j int) bool {
			if !entries[i].Time.Equal(entries[j].Time) {
				return entries[i].Time.After(entries[j].Time)
			}
			return entries[i].ActorID < entries[j].ActorID
		})
		prefix, tail := splitKey(key)
		out = append(out, Conflict{
			ID:      conflictID(key),
			Key:     key,
			Prefix:  prefix,
			Tail:    tail,
			Entries: entries,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func allEqual(entries []Entry) bool {
	if len(entries) <= 1 {
		return true
	}
	base := entries[0].Value
	for i := 1; i < len(entries); i++ {
		if !bytes.Equal(entries[i].Value, base) {
			return false
		}
	}
	return true
}

// conflictID is a short, stable id for a conflict — first 12 hex chars
// of the key's SHA-256 would be ideal but for v0.2 the key itself
// (sanitized) works fine because flat keys are deterministic.
func conflictID(key string) string {
	// "<prefix>:<tail-with-NULs-as-:>" → short readable form.
	s := strings.ReplaceAll(key, "\x00", ":")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// splitKey returns the flat-key prefix and the rest of the key
// when the key is in the documented shape; otherwise (prefix, key, false).
func splitKey(key string) (string, string) {
	idx := strings.IndexByte(key, '\x00')
	if idx < 0 {
		return "", key
	}
	return key[:idx], strings.ReplaceAll(key[idx+1:], "\x00", ":")
}

// Find returns the Conflict whose ID matches needle, or an error if
// nothing matches. Convenience for the CLI.
func Find(conflicts []Conflict, needle string) (Conflict, error) {
	for _, c := range conflicts {
		if c.ID == needle || c.Key == needle {
			return c, nil
		}
	}
	// Allow short-prefix matches as long as they're unambiguous.
	var matches []Conflict
	for _, c := range conflicts {
		if strings.HasPrefix(c.ID, needle) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Conflict{}, fmt.Errorf("ambiguous needle %q (%d matches)", needle, len(matches))
	}
	return Conflict{}, fmt.Errorf("no conflict matching %q", needle)
}

// PickEntry returns the entry whose actor id starts with the given
// prefix. Errors if no match or multiple match.
func PickEntry(c Conflict, actorPrefix string) (Entry, error) {
	var matches []Entry
	for _, e := range c.Entries {
		if strings.HasPrefix(e.ActorID, actorPrefix) {
			matches = append(matches, e)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return Entry{}, fmt.Errorf("no actor matching %q (have: %s)", actorPrefix, actorList(c.Entries))
	}
	return Entry{}, fmt.Errorf("ambiguous actor prefix %q (%d matches)", actorPrefix, len(matches))
}

func actorList(entries []Entry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		short := e.ActorID
		if len(short) > 8 {
			short = short[:8]
		}
		parts = append(parts, short)
	}
	return strings.Join(parts, ", ")
}
