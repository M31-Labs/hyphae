package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"m31labs.dev/hyphae/internal/identity"
	"m31labs.dev/hyphae/internal/indexer"
	"m31labs.dev/hyphae/internal/spore"
)

// Execute the real CLI entrypoint to check exit codes and both output streams.
func TestCLIProcess(t *testing.T) {
	if os.Getenv("HYPHA_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"hypha"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func cli(t *testing.T, root string, args ...string) (string, string, int) {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIProcess$", "--"}, args...)...)
	command.Env = append(os.Environ(), "HYPHA_TEST_HELPER=1", "HYPHAE_HOME="+root, "HYPHAE_FORMAT=compact")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

func writeCLIFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSporeHelp(t *testing.T) {
	root := t.TempDir()
	for _, sub := range []string{"", "new", "verify", "audit", "submit", "amend", "list", "accept", "reject", "reopen", "lint"} {
		t.Run(sub, func(t *testing.T) {
			args := []string{"spore"}
			if sub != "" {
				args = append(args, sub)
			}
			args = append(args, "--help")
			out, stderr, code := cli(t, root, args...)
			if code != 0 || stderr != "" || !strings.Contains(out, "Usage: hypha spore") {
				t.Fatalf("code %d stdout %q stderr %q", code, out, stderr)
			}
			if sub != "" && !strings.Contains(out, "-format") {
				t.Fatal("missing real flag help")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, ".index")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("help created index")
	}
}

func TestSporeScaffoldAndValidation(t *testing.T) {
	root := t.TempDir()
	for _, kind := range []string{"decision", "report", "spec"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(root, kind+".md")
			out, stderr, code := cli(t, root, "spore", "new", "--space", "hypha://example/knowledge", "--kind", kind, "--title", "Example: proposal", "--out", path, "--format", "text")
			if code != 0 {
				t.Fatalf("%s %s", out, stderr)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			parsed, errs := spore.Parse(data)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			if len(parsed.ProposedWrites) != 1 {
				t.Fatal("missing proposed write")
			}
			var fm map[string]any
			body := parsed.ProposedWrites[0].Payload["body"].(string)
			front, _ := splitFrontmatter([]byte(body))
			if err := yaml.Unmarshal(bytes.TrimSuffix(bytes.TrimPrefix(front, []byte("---\n")), []byte("---\n")), &fm); err != nil {
				t.Fatal(err)
			}
			if fm["type"] != kind || fm["id"] == nil {
				t.Fatalf("invalid canonical scaffold: %+v", fm)
			}
			if _, _, code := cli(t, root, "spore", "new", "--space", "hypha://example/knowledge", "--kind", kind, "--out", path); code == 0 {
				t.Fatal("overwrote existing file")
			}
			again, _ := os.ReadFile(path)
			if !bytes.Equal(data, again) {
				t.Fatal("overwrite changed user file")
			}
		})
	}
	for _, args := range [][]string{{"--kind", "unknown"}, {"--space", "invalid", "--kind", "report"}, {"--space", "hypha://example/knowledge", "--kind", "report", "--path", "../outside.md"}} {
		if _, _, code := cli(t, root, append([]string{"spore", "new"}, args...)...); code == 0 {
			t.Fatal("invalid scaffold accepted")
		}
	}
	path := filepath.Join(root, "invalid.md")
	data, _ := os.ReadFile(filepath.Join(root, "report.md"))
	var fm map[string]any
	front, body := splitFrontmatter(data)
	if err := yaml.Unmarshal(front[4:len(front)-4], &fm); err != nil {
		t.Fatal(err)
	}
	fm["proposed_writes"] = []string{"reports/example.md"}
	encoded, _ := yaml.Marshal(fm)
	writeCLIFile(t, path, append([]byte("---\n"+string(encoded)+"---\n"), body...))
	_, stderr, code := cli(t, root, "spore", "submit", path, "--format", "text")
	if code != 1 || !strings.Contains(stderr, "each entry must be a mapping") || !strings.Contains(stderr, "kind: create_file, path:") || !strings.Contains(stderr, "body:") {
		t.Fatalf("validation shape missing: %d %s", code, stderr)
	}
}

func TestSporeVerifyAndSubmitCLI(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "proposal.md")
	out, stderr, code := cli(t, root, "spore", "new", "--space", "hypha://example/knowledge", "--kind", "report", "--out", path, "--format", "text")
	if code != 0 {
		t.Fatalf("%s %s", out, stderr)
	}
	id, priv, err := identity.Generate("example", "reviewer", "hypha://example/knowledge")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".catalog", "identities"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := identity.Save(filepath.Join(root, ".catalog", "identities"), id, priv); err != nil {
		t.Fatal(err)
	}
	unsigned, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := spore.Sign(unsigned, priv, id.ID)
	if err != nil {
		t.Fatal(err)
	}
	signedPath := filepath.Join(root, "signed.md")
	writeCLIFile(t, signedPath, signed)
	tampered := bytes.Replace(signed, []byte("Describe the proposal"), []byte("Changed the proposal"), 1)
	tamperedPath := filepath.Join(root, "tampered.md")
	writeCLIFile(t, tamperedPath, tampered)
	for _, test := range []struct {
		path, status, changed string
		code                  int
	}{{signedPath, "VALID", "", 0}, {tamperedPath, "INVALID", "frontmatter", 1}, {path, "UNSIGNED", "", 1}} {
		for _, format := range []string{"text", "json"} {
			t.Run(test.status+format, func(t *testing.T) {
				out, stderr, code := cli(t, root, "spore", "verify", test.path, "--format", format)
				if code != test.code {
					t.Fatalf("code %d: %s %s", code, out, stderr)
				}
				if format == "text" {
					if !strings.HasPrefix(out, test.status+"\n") {
						t.Fatal(out)
					}
					if test.changed != "" && !strings.Contains(out, "Changed: "+test.changed) {
						t.Fatal(out)
					}
				} else {
					var env struct {
						OK   bool                     `json:"ok"`
						Data spore.VerificationReport `json:"data"`
					}
					if err := json.Unmarshal([]byte(out), &env); err != nil {
						t.Fatal(err)
					}
					if env.OK != (test.code == 0) || env.Data.Status != test.status {
						t.Fatalf("bad envelope: %+v", env)
					}
					if test.status != "UNSIGNED" && (env.Data.Version != 2 || env.Data.Signer != id.ID) {
						t.Fatalf("missing signer/scope: %+v", env.Data)
					}
				}
			})
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".index")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("verify wrote index")
	}
	spaceRoot := filepath.Join(root, "spaces", "example-knowledge")
	if err := os.MkdirAll(spaceRoot, 0755); err != nil {
		t.Fatal(err)
	}
	out, stderr, code = cli(t, root, "spore", "submit", "--sign", path, "--as", id.ID, "--format", "json")
	if code != 0 {
		t.Fatalf("submit: %s %s", out, stderr)
	}
	var env struct {
		Data struct {
			Path    string `json:"file_path"`
			Receipt struct {
				ContentHash string `json:"ContentHash"`
			} `json:"receipt"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(env.Data.Path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := spore.VerifyDetailed(stored, func(string) (identity.Identity, error) { return id, nil })
	if err != nil {
		t.Fatal(err)
	}
	if env.Data.Receipt.ContentHash != report.ContentHash {
		t.Fatal("CLI receipt/signature hash mismatch")
	}
	parsed, _ := spore.Parse(stored)
	out, stderr, code = cli(t, root, "spore", "verify", parsed.ID, "--space", "hypha://example/knowledge", "--format", "text")
	if code != 0 || !strings.HasPrefix(out, "VALID\n") {
		t.Fatalf("id verification: %s %s", out, stderr)
	}
	// Verify id lookup also works after a spore is moved into accepted documents.
	accepted := filepath.Join(spaceRoot, "reports", "accepted-spore.md")
	writeCLIFile(t, accepted, stored)
	if err := os.Remove(env.Data.Path); err != nil {
		t.Fatal(err)
	}
	if out, stderr, code := cli(t, root, "spore", "verify", parsed.ID, "--format", "text"); code != 0 {
		t.Fatalf("accepted document: %s %s", out, stderr)
	}
	// Pre-signed submissions preserve source and its signature without --sign.
	if out, stderr, code := cli(t, root, "spore", "submit", signedPath, "--format", "json"); code != 0 {
		t.Fatalf("presigned submission: %s %s", out, stderr)
	}
	entries, err := os.ReadDir(filepath.Join(spaceRoot, "inbox", "agents"))
	if err != nil || len(entries) != 1 {
		t.Fatal(err)
	}
	stored, err = os.ReadFile(filepath.Join(spaceRoot, "inbox", "agents", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, signed) {
		t.Fatal("presigned source was rewritten")
	}
}

func TestLegacyVerifyCLI(t *testing.T) {
	root := t.TempDir()
	public, err := os.ReadFile("../../internal/spore/testdata/v1-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var id map[string]string
	if err := json.Unmarshal(public, &id); err != nil {
		t.Fatal(err)
	}
	fm, _ := yaml.Marshal(map[string]any{"id": "identity.reviewer", "authority": "example", "type": "identity", "kind": "human", "space": "hypha://example/knowledge", "status": "active", "key_alg": "ed25519", "public_key": id["public_key"]})
	writeCLIFile(t, filepath.Join(root, ".catalog", "identities", "reviewer.md"), []byte("---\n"+string(fm)+"---\n"))
	out, stderr, code := cli(t, root, "spore", "verify", "../../internal/spore/testdata/v1.md", "--format", "text")
	if code != 0 || !strings.HasPrefix(out, "VALID\n") || !strings.Contains(out, "v1: content_hash covers body only; frontmatter verified via payload") {
		t.Fatalf("%s %s", out, stderr)
	}
}

func TestShowFormats(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "example-knowledge")
	path := filepath.Join(space, "reports", "example.md")
	content := []byte("---\nid: report.example\ntype: report\nspace: hypha://example/knowledge\nstatus: canonical\n---\n\n# Example\n\nContent.\n")
	writeCLIFile(t, path, content)
	conn, err := openIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.PromoteFile(conn, space, "hypha://example/knowledge", path); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	for _, args := range [][]string{{"show", "report.example", "--format", "json"}, {"show", "--json", "report.example"}} {
		out, stderr, code := cli(t, root, args...)
		if code != 0 {
			t.Fatalf("%s %s", out, stderr)
		}
		var env struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatal(err)
		}
		if env.Data["id"] != "report.example" {
			t.Fatal(out)
		}
	}
	out, stderr, code := cli(t, root, "show", "report.example", "--format", "text")
	if code != 0 || out != string(content) {
		t.Fatalf("%s %s", out, stderr)
	}
	if _, _, code := cli(t, root, "show", "report.example", "--format", "invalid"); code == 0 {
		t.Fatal("invalid format accepted")
	}
}

func TestPreservedYAMLSubmitAmendReview(t *testing.T) {
	for _, status := range []string{"'unreviewed'", "\"unreviewed\" # pending", "unreviewed # pending"} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "proposal.md")
			space := filepath.Join(root, "spaces", "example-knowledge")
			if err := os.MkdirAll(space, 0755); err != nil {
				t.Fatal(err)
			}
			if out, stderr, code := cli(t, root, "spore", "new", "--space", "hypha://example/knowledge", "--kind", "report", "--out", path); code != 0 {
				t.Fatalf("%s %s", out, stderr)
			}
			data, _ := os.ReadFile(path)
			parsed, _ := spore.Parse(data)
			data = bytes.Replace(data, []byte("id: "+parsed.ID), []byte("id: '"+parsed.ID+"' # identifier"), 1)
			data = bytes.Replace(data, []byte("status: unreviewed"), []byte("status: "+status), 1)
			writeCLIFile(t, path, data)
			if out, stderr, code := cli(t, root, "spore", "submit", path); code != 0 {
				t.Fatalf("submit: %s %s", out, stderr)
			}
			data = bytes.Replace(data, []byte("Explain what should change"), []byte("Updated: explain what should change"), 1)
			writeCLIFile(t, path, data)
			if out, stderr, code := cli(t, root, "spore", "amend", path); code != 0 {
				t.Fatalf("amend: %s %s", out, stderr)
			}
			if out, stderr, code := cli(t, root, "spore", "accept", parsed.ID, "--as", "identity://example/reviewer"); code != 0 {
				t.Fatalf("review: %s %s", out, stderr)
			}
			installed, err := findSporeFilePath(space, parsed.ID)
			if err != nil {
				t.Fatal(err)
			}
			result, _ := os.ReadFile(installed)
			if got, _ := spore.FrontmatterString(result, "status"); got != "accepted" {
				t.Fatalf("status: %q", got)
			}
			if !bytes.Contains(result, []byte("id: '"+parsed.ID+"' # identifier")) {
				t.Fatal("id representation changed")
			}
			if strings.Contains(status, "# pending") && !bytes.Contains(result, []byte("# pending")) {
				t.Fatal("status comment lost")
			}
		})
	}
}

func TestDocumentedSporeWorkflow(t *testing.T) {
	root := t.TempDir()
	space := filepath.Join(root, "spaces", "example-knowledge")
	path := filepath.Join(root, "proposal.md")
	if err := os.MkdirAll(space, 0755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".catalog", "identities")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	id, priv, err := identity.Generate("example", "reviewer", "hypha://example/knowledge")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := identity.Save(dir, id, priv); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"spore", "new", "--space", "hypha://example/knowledge", "--kind", "decision", "--title", "Record the decision", "--out", path},
		{"spore", "submit", path, "--sign", "--as", id.ID},
	} {
		if out, stderr, code := cli(t, root, args...); code != 0 {
			t.Fatalf("%v: %s %s", args, out, stderr)
		}
	}
	unsigned, _ := os.ReadFile(path)
	parsed, _ := spore.Parse(unsigned)
	installed, err := findSporeFilePath(space, parsed.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(installed)
	for _, args := range [][]string{
		{"spore", "verify", parsed.ID, "--format", "text"},
		{"graft", parsed.ID, "--as", id.ID, "--verify", "--dry-run", "--diff", "--format", "text"},
	} {
		if out, stderr, code := cli(t, root, args...); code != 0 {
			t.Fatalf("%v: %s %s", args, out, stderr)
		}
	}
	after, _ := os.ReadFile(installed)
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed spore")
	}
	target := filepath.Join(space, parsed.ProposedWrites[0].Payload["path"].(string))
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview created canonical document")
	}
	if out, stderr, code := cli(t, root, "graft", parsed.ID, "--as", id.ID, "--verify", "--apply", "--format", "text"); code != 0 {
		t.Fatalf("apply: %s %s", out, stderr)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
	if out, stderr, code := cli(t, root, "spore", "verify", parsed.ID, "--format", "text"); code != 0 {
		t.Fatalf("signature after graft: %s %s", out, stderr)
	}
	stored, _ := os.ReadFile(installed)
	if got, _ := spore.FrontmatterString(stored, "status"); got != "accepted" {
		t.Fatal(got)
	}
}

func TestLegacyV0VerifyAuditAndGraftCLI(t *testing.T) {
	root := t.TempDir()
	source, err := os.ReadFile("../../internal/spore/testdata/v0.md")
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile("../../internal/spore/testdata/v0-identity.json")
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(public, &record); err != nil {
		t.Fatal(err)
	}
	identitySource := "---\nid: identity.reviewer\ntype: identity\nauthority: example\nkey_alg: ed25519\npublic_key: " + record.PublicKey + "\n---\n"
	writeCLIFile(t, filepath.Join(root, ".catalog", "identities", "reviewer.md"), []byte(identitySource))
	space := filepath.Join(root, "spaces", "example-knowledge")
	path := filepath.Join(space, "inbox", "agents", "legacy.md")
	writeCLIFile(t, path, source)
	for _, format := range []string{"text", "json"} {
		out, stderr, code := cli(t, root, "spore", "verify", path, "--format", format)
		if code != 1 || !strings.Contains(out, "V0_LEGACY") || !strings.Contains(out, "proposals NOT covered") {
			t.Fatalf("verify: %s %s (%d)", out, stderr, code)
		}
	}
	forged := bytes.ReplaceAll(source, []byte("key: identity://example/reviewer"), []byte("key: identity://trusted/reviewer"))
	writeCLIFile(t, filepath.Join(space, "inbox", "agents", "forged.md"), forged)
	writeCLIFile(t, filepath.Join(space, "inbox", "agents", "unsigned.md"), bytes.Replace(source, []byte("signature:"), []byte("unsigned_signature:"), 1))
	for _, args := range [][]string{{"spore", "audit", "--format", "json"}, {"spore", "audit", "--space", "hypha://example/knowledge", "--format", "json"}} {
		out, stderr, code := cli(t, root, args...)
		var env struct {
			Data spore.AuditReport `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatal(err)
		}
		if code != 0 || env.Data.Counts.V0Legacy != 1 || env.Data.Counts.Invalid != 1 || env.Data.Counts.Unsigned != 1 || len(env.Data.Cases) != 1 {
			t.Fatalf("audit: %s %s (%d)", out, stderr, code)
		}
		if env.Data.Cases[0].Signer != "identity://example/reviewer" {
			t.Fatal("reported forged signer")
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".index")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("verify or audit created index")
	}
	if err := os.Remove(filepath.Join(space, "inbox", "agents", "forged.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(space, "inbox", "agents", "unsigned.md")); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := cli(t, root, "graft", "spore.2026-10-05.example.legacy", "--space", "hypha://example/knowledge", "--as", "identity://example/reviewer")
	if code != 1 || !strings.Contains(stderr, "--allow-legacy-proposals") {
		t.Fatalf("graft gate: %s %s (%d)", out, stderr, code)
	}
	out, stderr, code = cli(t, root, "graft", "--allow-legacy-proposals", "spore.2026-10-05.example.legacy", "--space", "hypha://example/knowledge", "--as", "identity://example/reviewer", "--verify")
	if code != 0 || !strings.Contains(stderr, "proposals NOT covered") {
		t.Fatalf("explicit graft: %s %s (%d)", out, stderr, code)
	}
}
