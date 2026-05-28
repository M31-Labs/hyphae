package crdtshadow_test

import "os"

// readSnapshotFile is a tiny test-only file reader. Lives in
// `_test.go` so it doesn't escape the test binary.
func readSnapshotFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
