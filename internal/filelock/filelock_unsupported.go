//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package filelock

import (
	"errors"
	"os"
)

var errUnsupported = errors.New("advisory file locks are unsupported on this platform")

func tryExclusive(*os.File) (bool, error) { return false, errUnsupported }
func unlock(*os.File) error               { return errUnsupported }
