// Package identity manages Ed25519 identities for Hyphae participants.
//
// Each identity has a public record stored as an mdpp document (a .md file
// with YAML frontmatter of type "identity") and a private-key sidecar (.key
// file, mode 0600). The public record is safe to share; the sidecar never
// leaves the local machine.
//
// URI mapping: the frontmatter "id" field uses the slug form
// "identity.<name>" for storage. The runtime Identity.ID field holds the
// full URI "identity://<authority>/<name>". Generate and Save write the slug;
// Load reconstructs the URI by reading the authority from the file path
// context passed by the caller (or embedded in the frontmatter id).
// To keep things simple the authority is stored as a non-standard
// "authority" frontmatter field written by Save and read back by Load.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/mdpp"
)

// Identity is the public-facing record. Matches the .md frontmatter shape.
type Identity struct {
	ID        string     // "identity://<authority>/<name>" or "agent://..." etc.
	Kind      string     // "human" | "agent" | "ci" | "service"
	Space     string     // owning space, e.g. "hypha://m31labs/hyphae"
	Status    string     // "active" | "rotated" | "revoked"
	KeyAlg    string     // "ed25519"
	PublicKey string     // "ed25519:base64:<32 bytes base64-std>"
	CreatedAt time.Time
	ExpiresAt *time.Time
	Succeeds  string // optional: previous identity id this rotates
	FilePath  string // not in frontmatter; set at runtime after Load
}

// PrivateKey wraps an ed25519.PrivateKey with helpers. Loaded from the .key
// sidecar (mode 0600); never written to the identity .md.
type PrivateKey struct {
	Key ed25519.PrivateKey
}

// Generate creates a new Identity + PrivateKey pair. authority is the URI
// authority (e.g. "m31labs"); name is the bare username (e.g. "odvcencio");
// space is the owning space URI (e.g. "hypha://m31labs/hyphae").
func Generate(authority, name, space string) (Identity, PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, PrivateKey{}, fmt.Errorf("identity: generate key: %w", err)
	}

	pubEncoded := "ed25519:base64:" + base64.StdEncoding.EncodeToString(pub)

	id := Identity{
		ID:        "identity://" + authority + "/" + name,
		Kind:      "human",
		Space:     space,
		Status:    "active",
		KeyAlg:    "ed25519",
		PublicKey: pubEncoded,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}

	return id, PrivateKey{Key: priv}, nil
}

// Save writes the Identity to <dir>/<name>.md and the PrivateKey to
// <dir>/<name>.key (mode 0600). Returns the absolute paths to both files.
// Refuses to overwrite an existing identity file.
func Save(dir string, id Identity, priv PrivateKey) (mdPath, keyPath string, err error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("identity: abs dir: %w", err)
	}

	name := nameFromID(id.ID)
	authority := authorityFromID(id.ID)

	mdPath = filepath.Join(absDir, name+".md")
	keyPath = filepath.Join(absDir, name+".key")

	if _, err := os.Stat(mdPath); err == nil {
		return "", "", fmt.Errorf("identity: %s already exists; refusing to overwrite", mdPath)
	}

	// Build frontmatter + body as a plain string.
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(`mdpp: "0.1"` + "\n")
	sb.WriteString("id: identity." + name + "\n")
	sb.WriteString("type: identity\n")
	sb.WriteString("authority: " + authority + "\n")
	sb.WriteString("kind: " + id.Kind + "\n")
	sb.WriteString("space: " + id.Space + "\n")
	sb.WriteString("status: " + id.Status + "\n")
	sb.WriteString("key_alg: " + id.KeyAlg + "\n")
	sb.WriteString("public_key: " + id.PublicKey + "\n")
	sb.WriteString("created_at: " + id.CreatedAt.Format(time.RFC3339) + "\n")
	if id.ExpiresAt != nil {
		sb.WriteString("expires_at: " + id.ExpiresAt.Format(time.RFC3339) + "\n")
	}
	if id.Succeeds != "" {
		sb.WriteString("succeeds: " + id.Succeeds + "\n")
	}
	sb.WriteString("---\n")
	sb.WriteString("\n# Identity: " + name + "\n\n")
	sb.WriteString("Active identity for " + name + " under the " + authority + " authority.\n")

	if err := os.WriteFile(mdPath, []byte(sb.String()), 0644); err != nil {
		return "", "", fmt.Errorf("identity: write %s: %w", mdPath, err)
	}

	// Encode private key: "ed25519:<base64-std-encoded 64-byte private key>"
	privEncoded := "ed25519:" + base64.StdEncoding.EncodeToString(priv.Key)
	if err := os.WriteFile(keyPath, []byte(privEncoded+"\n"), 0600); err != nil {
		// Clean up the .md we just wrote.
		_ = os.Remove(mdPath)
		return "", "", fmt.Errorf("identity: write %s: %w", keyPath, err)
	}

	return mdPath, keyPath, nil
}

// Load reads an Identity from <dir>/<name>.md. Does NOT load the private key.
func Load(dir, name string) (Identity, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return Identity{}, fmt.Errorf("identity: abs dir: %w", err)
	}

	mdPath := filepath.Join(absDir, name+".md")
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return Identity{}, fmt.Errorf("identity: read %s: %w", mdPath, err)
	}

	doc, err := mdpp.Parse(data)
	if err != nil {
		return Identity{}, fmt.Errorf("identity: parse %s: %w", mdPath, err)
	}

	fm := doc.Frontmatter()
	if fm == nil {
		return Identity{}, fmt.Errorf("identity: no frontmatter in %s", mdPath)
	}

	return identityFromFrontmatter(fm, mdPath)
}

// LoadPrivate reads the private key sidecar for <name>. Refuses to load if
// the file's permissions are not exactly 0600.
func LoadPrivate(dir, name string) (PrivateKey, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return PrivateKey{}, fmt.Errorf("identity: abs dir: %w", err)
	}

	keyPath := filepath.Join(absDir, name+".key")

	fi, err := os.Stat(keyPath)
	if err != nil {
		return PrivateKey{}, fmt.Errorf("identity: stat %s: %w", keyPath, err)
	}

	if fi.Mode().Perm() != 0600 {
		return PrivateKey{}, fmt.Errorf(
			"identity: private key file %s has unsafe permissions; expected 0600",
			keyPath,
		)
	}

	data, err := os.ReadFile(keyPath)
	if err != nil {
		return PrivateKey{}, fmt.Errorf("identity: read %s: %w", keyPath, err)
	}

	line := strings.TrimSpace(string(data))
	// Format: "ed25519:<base64>"
	const prefix = "ed25519:"
	if !strings.HasPrefix(line, prefix) {
		return PrivateKey{}, fmt.Errorf("identity: malformed key file %s: missing ed25519: prefix", keyPath)
	}

	rawB64 := line[len(prefix):]
	keyBytes, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		return PrivateKey{}, fmt.Errorf("identity: decode key %s: %w", keyPath, err)
	}

	if len(keyBytes) != ed25519.PrivateKeySize {
		return PrivateKey{}, fmt.Errorf(
			"identity: key %s has wrong length %d, want %d",
			keyPath, len(keyBytes), ed25519.PrivateKeySize,
		)
	}

	return PrivateKey{Key: ed25519.PrivateKey(keyBytes)}, nil
}

// List returns every Identity .md file in dir. Files that fail to parse are
// silently skipped (a best-effort directory listing).
func List(dir string) ([]Identity, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("identity: abs dir: %w", err)
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("identity: read dir %s: %w", absDir, err)
	}

	var ids []Identity
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		id, err := Load(absDir, name)
		if err != nil {
			continue // skip unparseable files
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Sign produces an Ed25519 signature over data.
func Sign(priv PrivateKey, data []byte) []byte {
	return ed25519.Sign(priv.Key, data)
}

// Verify checks an Ed25519 signature using the Identity's public key.
// Returns false if PublicKey is malformed.
func Verify(id Identity, data, sig []byte) bool {
	pub, err := decodePublicKey(id.PublicKey)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, data, sig)
}

// --- helpers ---

// nameFromID extracts the bare name from a full identity URI.
// "identity://m31labs/odvcencio" → "odvcencio"
// Falls back to the full string if the URI doesn't match the expected form.
func nameFromID(id string) string {
	// Handle "identity://<authority>/<name>" and similar URI forms.
	for _, scheme := range []string{"identity://", "agent://", "service://"} {
		if strings.HasPrefix(id, scheme) {
			rest := id[len(scheme):]
			parts := strings.SplitN(rest, "/", 2)
			if len(parts) == 2 {
				return parts[1]
			}
		}
	}
	return id
}

// authorityFromID extracts the authority from a full identity URI.
// "identity://m31labs/odvcencio" → "m31labs"
func authorityFromID(id string) string {
	for _, scheme := range []string{"identity://", "agent://", "service://"} {
		if strings.HasPrefix(id, scheme) {
			rest := id[len(scheme):]
			parts := strings.SplitN(rest, "/", 2)
			if len(parts) >= 1 {
				return parts[0]
			}
		}
	}
	return ""
}

// identityFromFrontmatter builds an Identity from parsed frontmatter.
// The "id" field in frontmatter is the slug form "identity.<name>"; we
// reconstruct the full URI using the "authority" field also written by Save.
func identityFromFrontmatter(fm map[string]any, filePath string) (Identity, error) {
	slug, _ := fm["id"].(string)   // e.g. "identity.odvcencio"
	authority, _ := fm["authority"].(string)

	// Reconstruct the full URI from slug + authority.
	// slug format: "identity.<name>"  →  "identity://<authority>/<name>"
	var fullID string
	if strings.HasPrefix(slug, "identity.") {
		name := slug[len("identity."):]
		if authority != "" {
			fullID = "identity://" + authority + "/" + name
		} else {
			fullID = "identity:///" + name // degenerate: no authority stored
		}
	} else {
		// Already a URI or unknown form — use as-is.
		fullID = slug
	}

	kind, _ := fm["kind"].(string)
	space, _ := fm["space"].(string)
	status, _ := fm["status"].(string)
	keyAlg, _ := fm["key_alg"].(string)
	publicKey, _ := fm["public_key"].(string)
	succeeds, _ := fm["succeeds"].(string)

	createdAt, err := parseTimeField(fm, "created_at")
	if err != nil {
		return Identity{}, fmt.Errorf("identity: created_at in %s: %w", filePath, err)
	}

	var expiresAt *time.Time
	if _, ok := fm["expires_at"]; ok {
		t, err := parseTimeField(fm, "expires_at")
		if err != nil {
			return Identity{}, fmt.Errorf("identity: expires_at in %s: %w", filePath, err)
		}
		expiresAt = &t
	}

	return Identity{
		ID:        fullID,
		Kind:      kind,
		Space:     space,
		Status:    status,
		KeyAlg:    keyAlg,
		PublicKey: publicKey,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
		Succeeds:  succeeds,
		FilePath:  filePath,
	}, nil
}

// parseTimeField reads a time value from a frontmatter map. YAML parses RFC
// 3339 timestamps as time.Time directly; plain strings are also accepted.
func parseTimeField(fm map[string]any, key string) (time.Time, error) {
	v, ok := fm[key]
	if !ok {
		return time.Time{}, nil
	}
	switch t := v.(type) {
	case time.Time:
		return t.UTC(), nil
	case string:
		parsed, err := time.Parse(time.RFC3339, t)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse %q: %w", t, err)
		}
		return parsed.UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("unexpected type %T for %s", v, key)
	}
}

// decodePublicKey decodes a "ed25519:base64:<b64>" encoded public key.
func decodePublicKey(encoded string) (ed25519.PublicKey, error) {
	const prefix = "ed25519:base64:"
	if !strings.HasPrefix(encoded, prefix) {
		return nil, errors.New("identity: public key missing ed25519:base64: prefix")
	}
	b64 := encoded[len(prefix):]
	keyBytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("identity: decode public key: %w", err)
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf(
			"identity: public key has wrong length %d, want %d",
			len(keyBytes), ed25519.PublicKeySize,
		)
	}
	return ed25519.PublicKey(keyBytes), nil
}

