package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCreateAndReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "record.json")
	if err := Create(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Create(path, []byte("two"), 0o600); !errors.Is(err, ErrExists) {
		t.Fatalf("second create error = %v, want ErrExists", err)
	}
	if err := Write(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "two" {
		t.Fatalf("body = %q, err = %v", body, err)
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", info.Mode().Perm(), err)
	}
}
