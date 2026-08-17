// Command release builds deterministic wip-commit beta archives.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	receiptSchemaVersion = 1
	commandPackage       = "./cmd/wip"
)

var (
	semverTagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	versionPattern   = regexp.MustCompile(`(?m)^var version = "([^"]+)"\r?$`)
	supportedTargets = []target{
		{OS: "darwin", Arch: "amd64"},
		{OS: "darwin", Arch: "arm64"},
		{OS: "linux", Arch: "amd64"},
		{OS: "linux", Arch: "arm64"},
		{OS: "windows", Arch: "amd64"},
		{OS: "windows", Arch: "arm64"},
	}
)

type target struct {
	OS   string
	Arch string
}

func (value target) String() string { return value.OS + "/" + value.Arch }

type targetList []string

func (values *targetList) String() string { return strings.Join(*values, ",") }
func (values *targetList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type receipt struct {
	SchemaVersion int        `json:"schema_version"`
	Version       string     `json:"version"`
	SourceCommit  string     `json:"source_commit"`
	SourceEpoch   int64      `json:"source_epoch"`
	GoVersion     string     `json:"go_version"`
	Artifacts     []artifact `json:"artifacts"`
	Checksums     artifact   `json:"checksums"`
}

type archiveEntry struct {
	Name string
	Mode os.FileMode
	Body []byte
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(2)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("release", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	version := flags.String("version", "", "release tag, for example v0.1.0-beta.1")
	out := flags.String("out", "", "new output directory")
	var rawTargets targetList
	flags.Var(&rawTargets, "target", "supported GOOS/GOARCH pair; repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if !semverTagPattern.MatchString(*version) {
		return errors.New("--version must be a SemVer tag that starts with v")
	}
	if strings.TrimSpace(*out) == "" {
		return errors.New("--out is required")
	}
	targets, err := parseTargets(rawTargets)
	if err != nil {
		return err
	}
	root, err := gitOutput("", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	root = filepath.Clean(root)
	if err := requireClean(root); err != nil {
		return err
	}
	commandVersion, err := sourceVersion(root)
	if err != nil {
		return err
	}
	if *version != "v"+commandVersion {
		return fmt.Errorf("release tag %q does not match command version %q", *version, "v"+commandVersion)
	}
	commit, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	epochText, err := gitOutput(root, "show", "-s", "--format=%ct", "HEAD")
	if err != nil {
		return err
	}
	epoch, err := strconv.ParseInt(epochText, 10, 64)
	if err != nil || epoch <= 0 {
		return fmt.Errorf("invalid source commit epoch %q", epochText)
	}
	outPath, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err := buildRelease(root, outPath, *version, commit, epoch, targets); err != nil {
		return err
	}
	fmt.Printf("built %d release archives in %s\n", len(targets), outPath)
	return nil
}

func parseTargets(values []string) ([]target, error) {
	if len(values) == 0 {
		return append([]target(nil), supportedTargets...), nil
	}
	allowed := make(map[string]target, len(supportedTargets))
	for _, item := range supportedTargets {
		allowed[item.String()] = item
	}
	seen := make(map[string]bool, len(values))
	result := make([]target, 0, len(values))
	for _, value := range values {
		item, ok := allowed[strings.TrimSpace(value)]
		if !ok {
			return nil, fmt.Errorf("unsupported target %q", value)
		}
		if seen[item.String()] {
			return nil, fmt.Errorf("duplicate target %q", value)
		}
		seen[item.String()] = true
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result, nil
}

func requireClean(root string) error {
	output, err := gitOutput(root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if output != "" {
		return errors.New("source checkout is not clean; commit the reviewed release inputs first")
	}
	return nil
}

func sourceVersion(root string) (string, error) {
	body, err := os.ReadFile(filepath.Join(root, "cmd", "wip", "main.go"))
	if err != nil {
		return "", err
	}
	match := versionPattern.FindSubmatch(body)
	if len(match) != 2 {
		return "", errors.New("cmd/wip/main.go has no static command version")
	}
	return string(match[1]), nil
}

func gitOutput(dir string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func buildRelease(root, outPath, version, commit string, epoch int64, targets []target) error {
	if len(targets) == 0 {
		return errors.New("at least one release target is required")
	}
	if _, err := os.Lstat(outPath); err == nil {
		return fmt.Errorf("output path already exists: %s", outPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(outPath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".wip-release-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	buildDir, err := os.MkdirTemp(staging, ".build-")
	if err != nil {
		return err
	}

	when := time.Unix(epoch, 0).UTC()
	artifacts := make([]artifact, 0, len(targets))
	for _, item := range targets {
		binaryName := "wip"
		if item.OS == "windows" {
			binaryName += ".exe"
		}
		binaryPath := filepath.Join(buildDir, item.OS+"-"+item.Arch+"-"+binaryName)
		if err := buildBinary(root, binaryPath, strings.TrimPrefix(version, "v"), item); err != nil {
			return err
		}
		entries, err := releaseEntries(root, binaryPath, binaryName)
		if err != nil {
			return err
		}
		stem := "wip-commit_" + strings.TrimPrefix(version, "v") + "_" + item.OS + "_" + item.Arch
		archiveName := stem + ".tar.gz"
		archivePath := filepath.Join(staging, archiveName)
		if item.OS == "windows" {
			archiveName = stem + ".zip"
			archivePath = filepath.Join(staging, archiveName)
			err = writeZip(archivePath, stem, entries, when)
		} else {
			err = writeTarGzip(archivePath, stem, entries, when)
		}
		if err != nil {
			return err
		}
		itemArtifact, err := fileArtifact(archivePath)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, itemArtifact)
	}
	if err := os.RemoveAll(buildDir); err != nil {
		return err
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
	checksumPath := filepath.Join(staging, "checksums.txt")
	if err := writeChecksums(checksumPath, artifacts); err != nil {
		return err
	}
	checksumArtifact, err := fileArtifact(checksumPath)
	if err != nil {
		return err
	}
	record := receipt{
		SchemaVersion: receiptSchemaVersion,
		Version:       version,
		SourceCommit:  commit,
		SourceEpoch:   epoch,
		GoVersion:     runtime.Version(),
		Artifacts:     artifacts,
		Checksums:     checksumArtifact,
	}
	if err := writeReceipt(filepath.Join(staging, "release-receipt.json"), record); err != nil {
		return err
	}
	if err := os.Rename(staging, outPath); err != nil {
		return err
	}
	return nil
}

func buildBinary(root, output, version string, item target) error {
	args := []string{
		"build",
		"-mod=readonly",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags=-s -w -buildid= -X main.version=" + version,
		"-o", output,
		commandPackage,
	}
	command := exec.Command("go", args...)
	command.Dir = root
	command.Env = releaseEnvironment(os.Environ(), item)
	outputText, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build %s: %w: %s", item, err, strings.TrimSpace(string(outputText)))
	}
	return nil
}

func releaseEnvironment(current []string, item target) []string {
	blocked := map[string]bool{
		"CGO_ENABLED": true,
		"GOARCH":      true,
		"GOFLAGS":     true,
		"GOOS":        true,
		"GOWORK":      true,
	}
	result := make([]string, 0, len(current)+4)
	for _, value := range current {
		name, _, _ := strings.Cut(value, "=")
		if !blocked[name] {
			result = append(result, value)
		}
	}
	return append(result, "CGO_ENABLED=0", "GOARCH="+item.Arch, "GOOS="+item.OS, "GOWORK=off")
}

func releaseEntries(root, binaryPath, binaryName string) ([]archiveEntry, error) {
	specs := []struct {
		path string
		name string
		mode os.FileMode
	}{
		{path: binaryPath, name: binaryName, mode: 0o755},
		{path: filepath.Join(root, "LICENSE"), name: "LICENSE", mode: 0o644},
		{path: filepath.Join(root, "README.md"), name: "README.md", mode: 0o644},
		{path: filepath.Join(root, "THREAT-MODEL.md"), name: "THREAT-MODEL.md", mode: 0o644},
	}
	entries := make([]archiveEntry, 0, len(specs))
	for _, spec := range specs {
		body, err := os.ReadFile(spec.path)
		if err != nil {
			return nil, err
		}
		entries = append(entries, archiveEntry{Name: spec.name, Mode: spec.mode, Body: body})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func writeTarGzip(path, root string, entries []archiveEntry, when time.Time) (returnErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer closeWithError(file, &returnErr)
	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return err
	}
	gzipWriter.ModTime = when
	gzipWriter.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     root + "/" + entry.Name,
			Mode:     int64(entry.Mode.Perm()),
			Size:     int64(len(entry.Body)),
			ModTime:  when,
			Typeflag: tar.TypeReg,
			Format:   tar.FormatUSTAR,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tarWriter.Write(entry.Body); err != nil {
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	return nil
}

func writeZip(path, root string, entries []archiveEntry, when time.Time) (returnErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer closeWithError(file, &returnErr)
	zipWriter := zip.NewWriter(file)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: root + "/" + entry.Name, Method: zip.Deflate}
		header.SetMode(entry.Mode)
		header.Modified = when
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := writer.Write(entry.Body); err != nil {
			return err
		}
	}
	return zipWriter.Close()
}

func closeWithError(closer io.Closer, returnErr *error) {
	if err := closer.Close(); *returnErr == nil && err != nil {
		*returnErr = err
	}
}

func fileArtifact(path string) (artifact, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return artifact{}, err
	}
	digest := sha256.Sum256(body)
	return artifact{
		Name:   filepath.Base(path),
		SHA256: hex.EncodeToString(digest[:]),
		Size:   int64(len(body)),
	}, nil
}

func writeChecksums(path string, artifacts []artifact) error {
	ordered := append([]artifact(nil), artifacts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	var body bytes.Buffer
	for _, item := range ordered {
		fmt.Fprintf(&body, "%s  %s\n", item.SHA256, item.Name)
	}
	return writeNewFile(path, body.Bytes())
}

func writeReceipt(path string, value receipt) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeNewFile(path, append(body, '\n'))
}

func writeNewFile(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
