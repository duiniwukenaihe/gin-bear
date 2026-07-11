//go:build !darwin && !linux && !windows

package atomicdir

func renameNoReplace(staged, destination string) error {
	return unsupportedRenameNoReplace(staged, destination)
}
