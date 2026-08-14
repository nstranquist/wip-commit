//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package atomicfile

import "os"

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
