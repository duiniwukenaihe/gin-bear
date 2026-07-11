package atomicdir

import "fmt"

// Publish moves a fully prepared directory into place without replacing an
// existing destination.
func Publish(staged, destination string) error {
	if err := renameNoReplace(staged, destination); err != nil {
		return fmt.Errorf("publish %q: %w", destination, err)
	}
	return nil
}
