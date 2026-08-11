//go:build darwin

package atomicdir

import "golang.org/x/sys/unix"

var nativeRenameNoReplace = func(staged, destination string) error {
	return unix.RenamexNp(staged, destination, unix.RENAME_EXCL)
}

func renameNoReplace(staged, destination string) error {
	return nativeRenameNoReplace(staged, destination)
}
