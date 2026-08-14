//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package atomicfile

import "errors"

func syncDirectory(string) error {
	return errors.New("durable directory sync is unsupported on this platform")
}
