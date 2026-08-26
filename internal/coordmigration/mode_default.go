//go:build !windows

package coordmigration

import "os"

func canonicalMode(mode os.FileMode) os.FileMode { return mode.Perm() }

func confinedLinkSourceMode(mode os.FileMode) os.FileMode { return mode }

func finalizeConfinedLink(_ *os.Root, _ string, _ os.FileMode) error { return nil }

func repairExactMode(_ string, _, _ os.FileMode) (bool, error) { return false, nil }

func repairConfinedMode(_ *os.Root, _ string, _, _ os.FileMode) (bool, error) {
	return false, nil
}
