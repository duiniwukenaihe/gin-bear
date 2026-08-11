package atomicdir

import (
	"errors"
	"fmt"
	"runtime"
)

var errAtomicNoReplaceUnsupported = errors.New("atomic no-replace directory publication is unsupported")

// Publish moves a fully prepared directory into place without replacing an
// existing destination.
func Publish(staged, destination string) error {
	if err := renameNoReplace(staged, destination); err != nil {
		return fmt.Errorf("publish %q: %w", destination, err)
	}
	return nil
}

func unsupportedRenameNoReplace(_, _ string) error {
	return fmt.Errorf("%w on %s", errAtomicNoReplaceUnsupported, runtime.GOOS)
}
