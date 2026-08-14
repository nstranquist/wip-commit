package main

import (
	"context"
	"io"
	"os"
)

var version = "0.1.0-beta.1"

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	application := app{stdin: stdin, stdout: stdout, stderr: stderr}
	return application.run(ctx, args)
}
