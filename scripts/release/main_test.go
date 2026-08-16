package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestParseTargets(t *testing.T) {
	items, err := parseTargets([]string{"linux/arm64", "darwin/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if got := items[0].String() + "," + items[1].String(); got != "darwin/amd64,linux/arm64" {
		t.Fatalf("targets = %q", got)
	}
	for _, values := range [][]string{{"plan9/amd64"}, {"linux/amd64", "linux/amd64"}} {
		if _, err := parseTargets(values); err == nil {
			t.Fatalf("parseTargets(%q) succeeded", values)
		}
	}
}

func TestArchiveWritersAreDeterministic(t *testing.T) {
	when := time.Unix(1_700_000_000, 0).UTC()
	entries := []archiveEntry{
		{Name: "LICENSE", Mode: 0o644, Body: []byte("license\n")},
		{Name: "wip", Mode: 0o755, Body: []byte("binary\x00bytes")},
	}
	for _, test := range []struct {
		name  string
		write func(string, string, []archiveEntry, time.Time) error
	}{
		{name: "tar.gz", write: writeTarGzip},
		{name: "zip", write: writeZip},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := filepath.Join(t.TempDir(), "first."+test.name)
			second := filepath.Join(t.TempDir(), "second."+test.name)
			if err := test.write(first, "release-root", entries, when); err != nil {
				t.Fatal(err)
			}
			if err := test.write(second, "release-root", entries, when); err != nil {
				t.Fatal(err)
			}
			firstBody, _ := os.ReadFile(first)
			secondBody, _ := os.ReadFile(second)
			if !bytes.Equal(firstBody, secondBody) {
				t.Fatal("archive bytes differ for identical inputs")
			}
		})
	}
}

func TestArchiveContentsAndModes(t *testing.T) {
	when := time.Unix(1_700_000_000, 0).UTC()
	entries := []archiveEntry{
		{Name: "README.md", Mode: 0o644, Body: []byte("read me\n")},
		{Name: "wip", Mode: 0o755, Body: []byte("binary")},
	}

	tarPath := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := writeTarGzip(tarPath, "root", entries, when); err != nil {
		t.Fatal(err)
	}
	tarFile, err := os.Open(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tarFile.Close(); err != nil {
			t.Errorf("close tar file: %v", err)
		}
	}()
	gzipReader, err := gzip.NewReader(tarFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := gzipReader.Close(); err != nil {
			t.Errorf("close gzip reader: %v", err)
		}
	}()
	tarReader := tar.NewReader(gzipReader)
	assertTarEntry(t, tarReader, "root/README.md", 0o644, "read me\n")
	assertTarEntry(t, tarReader, "root/wip", 0o755, "binary")
	if _, err := tarReader.Next(); err != io.EOF {
		t.Fatalf("extra tar entry: %v", err)
	}

	zipPath := filepath.Join(t.TempDir(), "release.zip")
	if err := writeZip(zipPath, "root", entries, when); err != nil {
		t.Fatal(err)
	}
	zipReader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := zipReader.Close(); err != nil {
			t.Errorf("close zip reader: %v", err)
		}
	}()
	if len(zipReader.File) != 2 {
		t.Fatalf("zip entries = %d", len(zipReader.File))
	}
	for index, name := range []string{"root/README.md", "root/wip"} {
		if zipReader.File[index].Name != name {
			t.Fatalf("zip entry %d = %q", index, zipReader.File[index].Name)
		}
	}
}

func TestWriteChecksumsUsesSortedArtifactInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checksums.txt")
	items := []artifact{
		{Name: "b.zip", SHA256: strings.Repeat("b", 64)},
		{Name: "a.tar.gz", SHA256: strings.Repeat("a", 64)},
	}
	if err := writeChecksums(path, items); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("a", 64) + "  a.tar.gz\n" + strings.Repeat("b", 64) + "  b.zip\n"
	if string(body) != want {
		t.Fatalf("checksums = %q", body)
	}
}

func TestBuildHostBinary(t *testing.T) {
	root := moduleRoot(t)
	binary := filepath.Join(t.TempDir(), "wip")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if err := buildBinary(root, binary, "0.1.0-test", target{OS: runtime.GOOS, Arch: runtime.GOARCH}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("built binary is empty")
	}
}

func TestBuildReleaseIsAtomicAndDeterministic(t *testing.T) {
	root := moduleRoot(t)
	parent := t.TempDir()
	first := filepath.Join(parent, "first")
	second := filepath.Join(parent, "second")
	version := "v0.1.0-test"
	commit := strings.Repeat("a", 40)
	epoch := int64(1_700_000_000)
	targets := []target{{OS: runtime.GOOS, Arch: runtime.GOARCH}}

	if err := buildRelease(root, first, version, commit, epoch, targets); err != nil {
		t.Fatal(err)
	}
	if err := buildRelease(root, second, version, commit, epoch, targets); err != nil {
		t.Fatal(err)
	}
	assertEqualDirectoryFiles(t, first, second)

	receiptBody, err := os.ReadFile(filepath.Join(first, "release-receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record receipt
	if err := json.Unmarshal(receiptBody, &record); err != nil {
		t.Fatal(err)
	}
	if record.SchemaVersion != receiptSchemaVersion || record.Version != version || record.SourceCommit != commit || record.SourceEpoch != epoch || len(record.Artifacts) != 1 {
		t.Fatalf("release receipt = %#v", record)
	}
	checksums, err := os.ReadFile(filepath.Join(first, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(checksums), record.Artifacts[0].SHA256+"  "+record.Artifacts[0].Name) {
		t.Fatalf("checksums do not contain artifact: %s", checksums)
	}
	if err := buildRelease(root, first, version, commit, epoch, targets); err == nil {
		t.Fatal("release builder overwrote an existing output directory")
	}

	failed := filepath.Join(parent, "failed")
	if err := buildRelease(root, failed, version, commit, epoch, []target{{OS: "invalid", Arch: "invalid"}}); err == nil {
		t.Fatal("release builder accepted an invalid Go target")
	}
	if _, err := os.Lstat(failed); !os.IsNotExist(err) {
		t.Fatalf("failed release left its output directory: %v", err)
	}
	if err := buildRelease(root, filepath.Join(parent, "empty"), version, commit, epoch, nil); err == nil {
		t.Fatal("release builder accepted no targets")
	}
}

func TestReleaseEnvironmentIsExplicit(t *testing.T) {
	environment := releaseEnvironment([]string{
		"PATH=/bin",
		"CGO_ENABLED=1",
		"GOARCH=old",
		"GOFLAGS=-mod=mod",
		"GOOS=old",
		"GOWORK=/tmp/work",
	}, target{OS: "linux", Arch: "arm64"})
	want := []string{"PATH=/bin", "CGO_ENABLED=0", "GOARCH=arm64", "GOOS=linux", "GOWORK=off"}
	if strings.Join(environment, "\n") != strings.Join(want, "\n") {
		t.Fatalf("release environment = %q, want %q", environment, want)
	}
}

func TestSourceVersionAndExclusiveFileCreation(t *testing.T) {
	version, err := sourceVersion(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if version != "0.1.0-beta.1" {
		t.Fatalf("source version = %q", version)
	}
	path := filepath.Join(t.TempDir(), "exclusive.txt")
	if err := writeNewFile(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeNewFile(path, []byte("second")); err == nil {
		t.Fatal("writeNewFile overwrote an existing file")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "first" {
		t.Fatalf("exclusive file = %q, %v", body, err)
	}
}

func TestRequireCleanFailsClosed(t *testing.T) {
	repository := t.TempDir()
	releaseGit(t, repository, "init", "-b", "main")
	releaseGit(t, repository, "config", "user.name", "Release Test")
	releaseGit(t, repository, "config", "user.email", "release@example.invalid")
	tracked := filepath.Join(repository, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	releaseGit(t, repository, "add", "tracked.txt")
	releaseGit(t, repository, "commit", "-m", "test: create clean release fixture")
	if err := requireClean(repository); err != nil {
		t.Fatalf("clean repository rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, "untracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "source checkout is not clean; commit the reviewed release inputs first"
	if err := requireClean(repository); err == nil || err.Error() != want {
		t.Fatalf("dirty repository error = %v, want %q", err, want)
	}
	if err := requireClean(t.TempDir()); err == nil {
		t.Fatal("non-repository passed the clean-source gate")
	}
	if _, err := gitOutput(filepath.Join(repository, "missing"), "status", "--porcelain=v1"); err == nil {
		t.Fatal("Git command in a missing directory returned success")
	}
}

func assertTarEntry(t *testing.T, reader *tar.Reader, name string, mode int64, body string) {
	t.Helper()
	header, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != name || header.Mode != mode {
		t.Fatalf("tar entry = %q mode %o", header.Name, header.Mode)
	}
	contents, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != body {
		t.Fatalf("tar entry %q body = %q", name, contents)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("find module root: %v", err)
	}
	return root
}

func assertEqualDirectoryFiles(t *testing.T, left, right string) {
	t.Helper()
	leftEntries, err := os.ReadDir(left)
	if err != nil {
		t.Fatal(err)
	}
	rightEntries, err := os.ReadDir(right)
	if err != nil {
		t.Fatal(err)
	}
	leftNames := make([]string, 0, len(leftEntries))
	rightNames := make([]string, 0, len(rightEntries))
	for _, entry := range leftEntries {
		leftNames = append(leftNames, entry.Name())
	}
	for _, entry := range rightEntries {
		rightNames = append(rightNames, entry.Name())
	}
	sort.Strings(leftNames)
	sort.Strings(rightNames)
	if strings.Join(leftNames, "\n") != strings.Join(rightNames, "\n") {
		t.Fatalf("release file sets differ: %v != %v", leftNames, rightNames)
	}
	for _, name := range leftNames {
		leftBody, err := os.ReadFile(filepath.Join(left, name))
		if err != nil {
			t.Fatal(err)
		}
		rightBody, err := os.ReadFile(filepath.Join(right, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(leftBody, rightBody) {
			t.Errorf("release file %s differs", name)
		}
	}
}

func releaseGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
