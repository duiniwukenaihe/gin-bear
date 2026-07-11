//go:build darwin

package atomicdir

import "golang.org/x/sys/unix"

func renameNoReplace(staged, destination string) error {
	return unix.RenamexNp(staged, destination, unix.RENAME_EXCL)
}
