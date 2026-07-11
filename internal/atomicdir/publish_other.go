//go:build !darwin && !linux && !windows

package atomicdir

import (
	"fmt"
	"os"
	"path/filepath"
)

func renameNoReplace(staged, destination string) (err error) {
	if err := os.Mkdir(destination, 0755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destination)
		}
	}()

	entries, err := os.ReadDir(staged)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(staged, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return fmt.Errorf("move %q: %w", entry.Name(), err)
		}
	}
	return os.Remove(staged)
}
