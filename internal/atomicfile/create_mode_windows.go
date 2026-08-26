//go:build windows

package atomicfile

import "os"

// Removing one read-only Windows hard-link name can clear the shared read-only
// attribute from the surviving name. Keep the temporary source writable, remove
// it after publication, and then apply the reviewed final mode to the target.
func createSourceMode(mode os.FileMode) os.FileMode { return mode | 0o200 }

func finalizeCreatedFile(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}
