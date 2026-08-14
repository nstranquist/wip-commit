//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package process

import "os/exec"

func configurePlatform(*exec.Cmd) {}
