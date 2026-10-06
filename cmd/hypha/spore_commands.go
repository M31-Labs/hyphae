package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"m31labs.dev/hyphae/internal/atomicfs"
	"m31labs.dev/hyphae/internal/envelope"
	"m31labs.dev/hyphae/internal/spore"
)

const sporeUsage = `Usage: hypha spore <command> [options]

  new      Scaffold a spore proposing a decision, report, or spec
  submit   Validate and submit a file, optionally signing it
  amend    Replace an unreviewed spore, optionally signing it again
  verify   Check a file or installed spore's signature (read-only)
  audit    Summarize signatures and list invalid proposals (read-only)
  list     List inbox spores with space, status, and time filters
  accept   Record acceptance without applying proposals
  reject   Mark an unreviewed spore rejected
  reopen   Return a partial or rejected spore to unreviewed
  lint     Validate a file and preview whether its writes can apply

Use hypha spore <command> --help for usage and flags.
`

var sporeCommandUsage = map[string]string{
	"new":    "--space <uri> --kind decision|report|spec [--title <title>] [--out <file>]",
	"audit":  "[--space <uri>] [--format text|json]",
	"verify": "<file|spore-id> [--space <uri>] [--format text|json]",
	"submit": "<file> [--sign --as <identity-uri>] [--format text|json|jsonline|compact]",
	"amend":  "<file> [--sign --as <identity-uri>] [--format text|json|jsonline|compact]",
	"list":   "[--space <uri>] [--status <state>] [--since 24h] [--limit N] [--format text|json|jsonline|compact]",
	"accept": "<spore-id> --as <identity-uri> [--reason <text>] [--space <uri>] [--format text|json|jsonline|compact]",
	"reject": "<spore-id> --as <identity-uri> [--reason <text>] [--space <uri>] [--format text|json|jsonline|compact]",
	"reopen": "<spore-id> --as <identity-uri> [--reason <text>] [--space <uri>] [--format text|json|jsonline|compact]",
	"lint":   "<file> [--format text|json|jsonline|compact]",
}

func sporeFlagSet(command string) *flag.FlagSet {
	fs := flag.NewFlagSet("spore "+command, flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: hypha spore %s %s\n\n", command, sporeCommandUsage[command])
		fs.PrintDefaults()
	}
	return fs
}

// The flag set knows which flags are boolean, so a leading --sign or --json
// cannot accidentally consume the file/id that follows it.
func parseCommandFlags(fs *flag.FlagSet, args []string) error {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if declared := fs.Lookup(name); declared != nil {
			if boolFlag, ok := declared.Value.(interface{ IsBoolFlag() bool }); ok && boolFlag.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	return fs.Parse(append(append(flags, "--"), positional...))
}

func cmdSporeNew(args []string) error {
	fs := sporeFlagSet("new")
	space := fs.String("space", "", "destination space URI (required)")
	kind := fs.String("kind", "", "decision | report | spec (required)")
	title := fs.String("title", "", "title of the proposed document")
	out := fs.String("out", "", "output spore file (default: generated spore id + .md)")
	as := fs.String("as", "agent://hypha/draft", "author agent or identity URI")
	source := fs.String("source", "", "source reference URI (default: destination space)")
	path := fs.String("path", "", "proposed document path inside the space")
	format := formatFlag(fs)
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if _, err := envelope.ParseFormat(*format); err != nil {
		return err
	}
	spaceURI, uriErr := url.Parse(*space)
	if fs.NArg() != 0 || uriErr != nil || spaceURI.Scheme != "hypha" || spaceURI.Host == "" || strings.Trim(spaceURI.Path, "/") == "" || spaceURI.RawQuery != "" || spaceURI.Fragment != "" {
		return errors.New("usage: hypha spore new --space <hypha://authority/space> --kind decision|report|spec [--out <file>]")
	}
	folders := map[string]string{"decision": "decisions", "report": "reports", "spec": "specs"}
	folder, ok := folders[*kind]
	if !ok {
		return errors.New("spore new: --kind must be decision, report, or spec")
	}
	if *title == "" {
		*title = "New " + *kind
	}
	if strings.ContainsAny(*title, "\r\n") {
		return errors.New("spore new: --title must be one line")
	}
	if *source == "" {
		*source = *space
	}
	now := time.Now().UTC().Truncate(time.Second)
	short := uuid.NewString()[:8]
	id := "spore." + now.Format("2006-01-02") + ".draft." + short
	docID := *kind + "." + short
	if *path == "" {
		*path = folder + "/" + docID + ".md"
	}
	if filepath.IsAbs(*path) || strings.Contains(*path, "\\") || filepath.Clean(*path) == ".." || strings.HasPrefix(filepath.Clean(*path), "../") {
		return errors.New("spore new: --path must stay inside the destination space")
	}
	canonicalFM, err := yaml.Marshal(map[string]any{"mdpp": "0.1", "id": docID, "type": *kind, "space": *space, "status": "draft", "created": now})
	if err != nil {
		return err
	}
	body := "---\n" + string(canonicalFM) + "---\n\n# " + *title + "\n\nDescribe the proposal and cite its sources.\n"
	fm, err := yaml.Marshal(map[string]any{
		"mdpp": "0.1", "id": id, "type": "spore", "space": *space,
		"status": "unreviewed", "created": now, "confidence": "medium",
		"agent": map[string]any{"id": *as, "kind": "ephemeral"}, "source_refs": []string{*source},
		"proposed_writes": []any{map[string]any{"kind": "create_file", "path": *path, "body": body}},
	})
	if err != nil {
		return err
	}
	content := []byte("---\n" + string(fm) + "---\n\n# " + *title + "\n\nExplain what should change and why.\n")
	if _, errs := spore.Parse(content); len(errs) > 0 {
		return fmt.Errorf("spore new: %v", errs)
	}
	if *out == "" {
		*out = id + ".md"
	}
	if err := atomicfs.CreateFile(*out, content, 0o644); err != nil {
		return fmt.Errorf("spore new: create file: %w", err)
	}
	return emit("spore new", map[string]any{"spore_id": id, "file_path": *out, "kind": *kind}, *format, func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "Created: %s\nNext: edit the proposal, then hypha spore submit %s\n", *out, *out)
		return nil
	})
}

func cmdSporeVerify(args []string) error {
	fs := sporeFlagSet("verify")
	space := fs.String("space", "", "space URI to search when using a spore id")
	format := formatFlag(fs)
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: hypha spore verify <file|spore-id> [--space <uri>] [--format text|json]")
	}
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	path := fs.Arg(0)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && strings.HasPrefix(path, "spore.") {
		path, err = resolveVerificationSpore(root, path, *space)
		if err != nil {
			return err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	report, verifyErr := spore.VerifyDetailed(data, identityResolver(root))
	view := func(w io.Writer, _ any) error {
		fmt.Fprintf(w, "%s\n", report.Status)
		if report.Signer != "" {
			fmt.Fprintf(w, "  Signer: %s\n  Signature version: %d\n", report.Signer, report.Version)
			fmt.Fprintf(w, "  Covered: %s\n  Excluded: %s\n", strings.Join(report.CoveredFields, ", "), strings.Join(report.ExcludedFields, ", "))
			fmt.Fprintf(w, "  Body hash:        %s\n  Frontmatter hash: %s\n  Content hash:     %s\n", report.BodyHash, report.FrontmatterHash, report.ContentHash)
		}
		if len(report.Changed) > 0 {
			fmt.Fprintf(w, "  Changed: %s (compared with recorded hashes)\n", strings.Join(report.Changed, ", "))
		}
		if len(report.ProposalMismatch) > 0 {
			fmt.Fprintf(w, "  Proposal mismatch: %s (unverified)\n", strings.Join(report.ProposalMismatch, ", "))
		}
		if report.Note != "" {
			fmt.Fprintf(w, "  %s\n", report.Note)
		}
		if report.Error != "" {
			fmt.Fprintf(w, "  %s\n", report.Error)
		}
		return nil
	}
	f, err := envelope.ParseFormat(*format)
	if err != nil {
		return err
	}
	if f == envelope.FormatText {
		if err := view(os.Stdout, report); err != nil {
			return err
		}
		return verifyErr
	}
	env := envelope.New("spore verify", report)
	if verifyErr != nil {
		env.OK = false
		env.Errors = append(env.Errors, envelope.Note{Code: report.Status, Message: verifyErr.Error()})
	}
	if err := envelope.Emit(os.Stdout, env, f, view); err != nil {
		return err
	}
	return verifyErr
}

// Search both inbox records and accepted documents without opening the index.
func resolveVerificationSpore(root, id, space string) (string, error) {
	dir := filepath.Join(root, "spaces")
	if space != "" {
		var err error
		dir, err = spaceURIToPath(root, space)
		if err != nil {
			return "", err
		}
	}
	var matches []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || (!strings.HasSuffix(path, ".md") && !strings.HasSuffix(path, ".mdpp")) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		parsed, _ := spore.Parse(data)
		if parsed.ID == id {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("spore verify: found %d files for %q; pass a file path or --space to disambiguate", len(matches), id)
	}
	return matches[0], nil
}

func cmdSporeAudit(args []string) error {
	fs := sporeFlagSet("audit")
	space := fs.String("space", "", "space URI to audit (default: all installed spaces)")
	format := formatFlag(fs)
	if err := parseCommandFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: hypha spore audit [--space <uri>] [--format text|json]")
	}
	root, err := resolveRoot("")
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "spaces")
	if *space != "" {
		dir, err = spaceURIToPath(root, *space)
		if err != nil {
			return err
		}
	}
	report, err := spore.Audit(dir, identityResolver(root))
	if err != nil {
		return err
	}
	return emit("spore audit", report, *format, func(w io.Writer, _ any) error {
		c := report.Counts
		fmt.Fprintf(w, "Audited %d spores\nVALID: %d (v2: %d, v1: %d)\nv0-legacy: %d\nINVALID: %d\nUNSIGNED: %d\nProposal mismatch: %d (included in INVALID)\n", c.Total, c.Valid, c.V2, c.V1, c.V0Legacy, c.Invalid, c.Unsigned, c.ProposalMismatch)
		for _, issue := range report.Cases {
			fmt.Fprintf(w, "\nINVALID %s\n  %s\n", issue.File, issue.Error)
			if len(issue.ProposalMismatch) > 0 {
				fmt.Fprintf(w, "  Proposal mismatch: %s (unverified)\n", strings.Join(issue.ProposalMismatch, ", "))
			}
			if issue.Note != "" {
				fmt.Fprintf(w, "  %s\n", issue.Note)
			}
		}
		return nil
	})
}
