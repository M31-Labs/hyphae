// Package atomicfs writes files atomically: write to a temp file in the
// same directory, fsync, then rename over the target. On POSIX systems
// `rename` is atomic when source and destination are on the same
// filesystem, so a reader will either see the previous file or the new
// one — never a half-written intermediate.
//
// Use this in place of os.WriteFile anywhere a half-written file would
// corrupt agent-visible state: spores, traces, receipts, canonical .md.
package atomicfs

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile atomically writes data to path with the given permissions.
//
// Process: create `<path>.tmp.<pid>` in the same directory, write all
// data, fsync the file, close it, then rename over path. Failures at
// any step leave the original path untouched and clean up the temp file.
//
// perm is applied to the temp file before rename; the post-rename file
// inherits it (os.Rename preserves the source's perms on the destination).
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return writeFile(path, data, perm, false, func(f *os.File, b []byte) error { _, err := f.Write(b); return err })
}

// CreateFile publishes a complete file atomically without overwriting an existing
// destination. A hard link from the synced temporary file provides no-replace
// semantics on the same filesystem. All failure paths remove the temporary file.
func CreateFile(path string, data []byte, perm os.FileMode) error {
	return writeFile(path, data, perm, true, func(f *os.File, b []byte) error { _, err := f.Write(b); return err })
}

func writeFile(path string, data []byte, perm os.FileMode, exclusive bool, write func(*os.File, []byte) error) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp.")
	if err != nil {
		return fmt.Errorf("atomicfs: create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	cleanup := func() {
		_ = os.Remove(tmpPath)
	}

	if err := write(tmp, data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("atomicfs: write %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("atomicfs: chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("atomicfs: fsync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("atomicfs: close %s: %w", tmpPath, err)
	}
	publish := os.Rename
	if exclusive {
		publish = os.Link
	}
	if err := publish(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("atomicfs: publish %s → %s: %w", tmpPath, path, err)
	}
	cleanup()
	return nil
}
