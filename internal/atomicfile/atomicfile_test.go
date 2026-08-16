package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("create left temporary entries: %#v, err=%v", entries, err)
	}
}

func TestConcurrentCreatePublishesOneCompleteWinner(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "record.json")
	bodies := [][]byte{[]byte("alpha"), []byte("bravo"), []byte("charlie"), []byte("delta")}
	errorsByBody := make([]error, len(bodies))
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range bodies {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsByBody[index] = Create(path, bodies[index], 0o600)
		}()
	}
	close(start)
	wait.Wait()
	winners := 0
	var wanted []byte
	for index, err := range errorsByBody {
		if err == nil {
			winners++
			wanted = bodies[index]
			continue
		}
		if !errors.Is(err, ErrExists) {
			t.Fatalf("create %d error = %v", index, err)
		}
	}
	if winners != 1 {
		t.Fatalf("successful creators = %d, errors=%v", winners, errorsByBody)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(wanted) {
		t.Fatalf("published body = %q, want %q, err=%v", got, wanted, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "record.json" {
		t.Fatalf("concurrent create left temporary entries: %#v, err=%v", entries, err)
	}
}

func TestCreateWithExternalTempDirectoryKeepsDestinationCanonical(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	targetDirectory := filepath.Join(root, "bundle", "references")
	target := filepath.Join(targetDirectory, "automation.md")
	if err := CreateWithTempDir(target, staging, []byte("canonical\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(targetDirectory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "automation.md" {
		t.Fatalf("destination entries = %#v, err=%v", entries, err)
	}
	stagingEntries, err := os.ReadDir(staging)
	if err != nil || len(stagingEntries) != 0 {
		t.Fatalf("staging entries = %#v, err=%v", stagingEntries, err)
	}
}

func TestWriteWithExternalTempDirectoryKeepsDestinationCanonical(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	targetDirectory := filepath.Join(root, "state", "lanes")
	target := filepath.Join(targetDirectory, "lane.json")
	if err := WriteWithTempDir(target, staging, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteWithTempDir(target, staging, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "second\n" {
		t.Fatalf("replacement body = %q, err=%v", body, err)
	}
	entries, err := os.ReadDir(targetDirectory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "lane.json" {
		t.Fatalf("destination entries = %#v, err=%v", entries, err)
	}
	stagingEntries, err := os.ReadDir(staging)
	if err != nil || len(stagingEntries) != 0 {
		t.Fatalf("staging entries = %#v, err=%v", stagingEntries, err)
	}
}
