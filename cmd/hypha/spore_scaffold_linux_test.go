package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestScaffoldLimitedWriteProcess(t *testing.T) {
	if os.Getenv("HYPHA_TEST_LIMIT_WRITE") != "1" {
		return
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 256, Max: 256}); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"hypha", "spore", "new", "--space", "hypha://example/knowledge", "--kind", "report", "--out", os.Getenv("HYPHA_TEST_SCAFFOLD_PATH")}
	main()
	os.Exit(0)
}

func TestScaffoldFailedWriteLeavesNoDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proposal.md")
	command := exec.Command(os.Args[0], "-test.run=^TestScaffoldLimitedWriteProcess$")
	command.Env = append(os.Environ(), "HYPHA_TEST_LIMIT_WRITE=1", "HYPHA_TEST_SCAFFOLD_PATH="+path, "HYPHAE_HOME="+dir)
	if out, err := command.CombinedOutput(); err == nil {
		t.Fatalf("limited write succeeded: %s", out)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial scaffold: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed scaffold left files: %v %v", entries, err)
	}
	if out, stderr, code := cli(t, dir, "spore", "new", "--space", "hypha://example/knowledge", "--kind", "report", "--out", path); code != 0 {
		t.Fatalf("retry: %s %s", out, stderr)
	}
}
