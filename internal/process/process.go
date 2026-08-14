package process

import (
	"context"
	"os/exec"
	"time"
)

const defaultWaitDelay = 2 * time.Second

func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	configurePlatform(cmd)
	cmd.WaitDelay = defaultWaitDelay
	return cmd
}
