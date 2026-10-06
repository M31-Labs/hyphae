package atomicfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestWriteFileOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := WriteFile(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("got %q, want %q", got, "new")
	}
}

func TestWriteFileLeavesNoTempOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected exactly 1 file after WriteFile, got %d: %v", len(entries), names)
	}
}

func TestWriteFilePerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")
	if err := WriteFile(path, []byte("hush"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 0600", info.Mode().Perm())
	}
}

func TestCreateFileNoOverwriteAndCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	if err := CreateFile(path, []byte("complete"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CreateFile(path, []byte("replacement"), 0644); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite error: %v", err)
	}
	data, _ := os.ReadFile(path)
	entries, _ := os.ReadDir(dir)
	if string(data) != "complete" || len(entries) != 1 {
		t.Fatalf("%q %v", data, entries)
	}
}

func TestCreateFileFailedWriteLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	err := writeFile(path, []byte("complete proposal"), 0644, true, func(f *os.File, data []byte) error {
		if _, err := f.Write(data[:4]); err != nil {
			return err
		}
		return io.ErrShortWrite
	})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial destination: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("temporary files remain: %v", entries)
	}
	if err := CreateFile(path, []byte("retry succeeds"), 0644); err != nil {
		t.Fatal(err)
	}
}
