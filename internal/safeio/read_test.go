package safeio

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadRegularIsBoundedAndRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "record.json")
	if err := os.WriteFile(path, []byte("record"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := ReadRegular(path, 6)
	if err != nil || string(body) != "record" {
		t.Fatalf("read = %q, %v", body, err)
	}
	if _, err := ReadRegular(path, 5); err == nil {
		t.Fatal("oversized file passed")
	}
	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(directory, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegular(link, 64); err == nil {
		t.Fatal("symlink passed")
	}
}
