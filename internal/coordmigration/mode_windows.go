//go:build windows

package coordmigration

import "os"

func canonicalMode(mode os.FileMode) os.FileMode {
	if mode.IsDir() {
		// Chmod does not change Windows directory permissions. Canonical
		// manifests therefore record the private writable directory policy and
		// bind archive immutability through read-only files plus exact digests.
		return 0o700
	}
	if mode.Perm()&0o200 != 0 {
		return 0o600
	}
	return 0o400
}

// Windows maps a mode without 0200 to the read-only file attribute. Wine and
// some Windows filesystems reject hard-link creation when that attribute is
// already set. Publish the complete file while writable, remove the temporary
// name, and then apply the reviewed final mode before the caller can authorize
// the path in the migration journal.
func confinedLinkSourceMode(mode os.FileMode) os.FileMode { return mode | 0o200 }

func finalizeConfinedLink(root *os.Root, name string, mode os.FileMode) error {
	return root.Chmod(name, mode)
}

func repairExactMode(path string, actual, want os.FileMode) (bool, error) {
	if !windowsReadOnlyFinalizeWindow(actual, want) {
		return false, nil
	}
	return true, os.Chmod(path, want)
}

func repairConfinedMode(root *os.Root, name string, actual, want os.FileMode) (bool, error) {
	if !windowsReadOnlyFinalizeWindow(actual, want) {
		return false, nil
	}
	return true, root.Chmod(name, want)
}

func windowsReadOnlyFinalizeWindow(actual, want os.FileMode) bool {
	return actual.Perm()&0o200 != 0 && want.Perm()&0o200 == 0
}
