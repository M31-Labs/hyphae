package crdtshadow

import (
	"errors"
	"os"
)

// readIfExists returns the file contents, or (nil, nil) if the file is
// absent. Any other error is returned as-is.
func readIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return nil, err
}
