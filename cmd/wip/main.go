package main

import (
	"context"
	"io"
	"os"
)

// Source builds are always development builds. The release builder injects a
// SemVer and its exact v-prefixed tag together.
var version = "0.2.0-dev"

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	application := app{stdin: stdin, stdout: stdout, stderr: stderr}
	return application.run(ctx, args)
}
