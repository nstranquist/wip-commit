package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	defer tarFile.Close()
	gzipReader, err := gzip.NewReader(tarFile)
	if err != nil {
		t.Fatal(err)
	}
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
	defer zipReader.Close()
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
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("find module root: %v", err)
	}
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
