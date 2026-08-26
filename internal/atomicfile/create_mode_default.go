//go:build !windows

package atomicfile

import "os"

func createSourceMode(mode os.FileMode) os.FileMode { return mode }

func finalizeCreatedFile(_ string, _ os.FileMode) error { return nil }
