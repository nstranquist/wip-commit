//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package atomicfile

import "os"

func replaceFile(source, target string) error { return os.Rename(source, target) }
