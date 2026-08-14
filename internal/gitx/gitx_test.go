package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDiscoverAndNormalizePathsFailClosed(t *testing.T) {
	directory := t.TempDir()
	command := exec.Command("git", "-C", directory, "init", "-b", "main")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	repo, err := Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := repo.NormalizePaths([]string{"src/file.go", "src/file.go"})
	if err != nil || len(paths) != 1 || paths[0] != "src/file.go" {
		t.Fatalf("paths = %#v, %v", paths, err)
	}
	outside := filepath.Join(filepath.Dir(repo.Root), "outside.txt")
	invalid := [][]string{{"."}, {".GIT/config"}, {outside}, {"bad\x00path"}, {string([]byte{0xff})}}
	for _, input := range invalid {
		if _, err := repo.NormalizePaths(input); err == nil {
			t.Errorf("invalid path passed: %q", input)
		}
	}
	if _, err := repo.NormalizePaths([]string{"Docs/CAFÉ.md", "docs/cafe\u0301.md"}); err == nil {
		t.Fatal("portable Unicode aliases passed")
	}
	if _, err := os.Stat(repo.GitDir); err != nil {
		t.Fatal(err)
	}
}
