package coordmigration

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/safeio"
)

func manifestTree(commonDir, logicalRoot string) (TreeManifest, error) {
	manifest := TreeManifest{LogicalRoot: filepath.ToSlash(logicalRoot), Entries: []ManifestEntry{}}
	root, err := confinedPath(commonDir, logicalRoot)
	if err != nil {
		return manifest, err
	}
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		manifest.Digest, err = manifestDigest(manifest)
		return manifest, err
	}
	if err != nil {
		return manifest, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return manifest, fmt.Errorf("%s is not a regular directory", logicalRoot)
	}
	manifest.Exists = true
	var totalBytes int64
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(manifest.Entries) >= maxManifestEntries {
			return fmt.Errorf("manifest exceeds %d entries", maxManifestEntries)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		item := ManifestEntry{Path: relative, Mode: modeString(info.Mode())}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("manifest rejects symlink %s", filepath.ToSlash(filepath.Join(logicalRoot, relative)))
		case info.IsDir():
			item.Type = "directory"
		case info.Mode().IsRegular():
			item.Type = "file"
			item.Size = info.Size()
			totalBytes += info.Size()
			if totalBytes > maxManifestBytes {
				return fmt.Errorf("manifest exceeds %d bytes", maxManifestBytes)
			}
			body, err := safeio.ReadRegular(path, maxManifestBytes)
			if err != nil {
				return err
			}
			item.SHA256 = digest(body)
		default:
			return fmt.Errorf("manifest rejects non-regular path %s", filepath.ToSlash(filepath.Join(logicalRoot, relative)))
		}
		manifest.Entries = append(manifest.Entries, item)
		return nil
	})
	if err != nil {
		return manifest, err
	}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].Path < manifest.Entries[j].Path })
	manifest.Digest, err = manifestDigest(manifest)
	return manifest, err
}

func manifestDigest(manifest TreeManifest) (string, error) {
	manifest.Digest = ""
	body, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	return digest(body), nil
}

func confinedPath(commonDir, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("state path must be relative: %s", relative)
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("state path escapes Git common directory: %s", relative)
	}
	return filepath.Join(commonDir, clean), nil
}

func modeString(mode os.FileMode) string { return fmt.Sprintf("%04o", canonicalMode(mode)) }

func parseMode(value string) (os.FileMode, error) {
	parsed, err := strconv.ParseUint(value, 8, 32)
	if err != nil || parsed > 0o777 {
		return 0, fmt.Errorf("invalid file mode %q", value)
	}
	return os.FileMode(parsed), nil
}

func readJSONFile(path string, target any) ([]byte, error) {
	return readJSONFileLimit(path, target, maxRecordBytes)
}

func readJSONFileLimit(path string, target any, limit int64) ([]byte, error) {
	body, err := safeio.ReadRegular(path, limit)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("record contains multiple JSON values")
		}
		return nil, err
	}
	return body, nil
}

func normalizedJSON(value any) ([]byte, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func createExact(path string, body []byte, mode os.FileMode) error {
	if err := atomicfile.Create(path, body, mode); err == nil {
		return nil
	} else if !errors.Is(err, atomicfile.ErrExists) {
		return err
	}
	existing, err := safeio.ReadRegular(path, int64(len(body)))
	if err != nil || !bytes.Equal(existing, body) {
		return fmt.Errorf("destination conflict: %s", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect destination mode %s: %w", path, err)
	}
	if actual, want := modeString(info.Mode()), modeString(mode); actual != want {
		repaired, repairErr := repairExactMode(path, info.Mode(), mode)
		if repairErr != nil {
			return fmt.Errorf("finalize destination mode %s: %w", path, repairErr)
		}
		if !repaired {
			return fmt.Errorf("destination mode conflict: %s has %s (%s), want %s", path, actual, info.Mode(), want)
		}
		info, err = os.Lstat(path)
		if err != nil || modeString(info.Mode()) != want {
			return fmt.Errorf("destination mode repair failed: %s", path)
		}
		return syncDirectory(filepath.Dir(path))
	}
	return nil
}

func copyConfinedExact(source, commonDir, destination, expectedDigest string, mode os.FileMode) error {
	body, err := safeio.ReadRegular(source, maxManifestBytes)
	if err != nil {
		return err
	}
	if digest(body) != expectedDigest {
		return fmt.Errorf("source moved while copying %s", source)
	}
	return createConfinedExact(commonDir, destination, body, mode)
}

// createConfinedExact publishes one immutable file through an opened root.
// Every parent is inspected without following a symlink before it becomes the
// next opened root. Publication uses a same-directory hard link, so readers
// can see only the complete file or no file.
func createConfinedExact(commonDir, relative string, body []byte, mode os.FileMode) error {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("destination is outside the Git common directory: %s", relative)
	}
	parts := splitPath(clean)
	if len(parts) < 2 {
		return fmt.Errorf("destination has no confined parent: %s", relative)
	}
	root, err := os.OpenRoot(commonDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, component := range parts[:len(parts)-1] {
		info, statErr := root.Lstat(component)
		if statErr != nil {
			return fmt.Errorf("destination parent %s is unavailable: %w", component, statErr)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination parent is not a regular directory: %s", component)
		}
		next, openErr := root.OpenRoot(component)
		if openErr != nil {
			return openErr
		}
		_ = root.Close()
		root = next
	}
	base := parts[len(parts)-1]
	if info, statErr := root.Lstat(base); statErr == nil {
		if err := verifyConfinedContent(root, base, info, body); err != nil {
			return fmt.Errorf("destination conflict: %s", relative)
		}
		if actual, want := modeString(info.Mode()), modeString(mode); actual != want {
			repaired, repairErr := repairConfinedMode(root, base, info.Mode(), mode)
			if repairErr != nil {
				return fmt.Errorf("finalize destination mode %s: %w", relative, repairErr)
			}
			if !repaired {
				return fmt.Errorf("destination mode conflict: %s has %s (%s), want %s", relative, actual, info.Mode(), want)
			}
			info, statErr = root.Lstat(base)
			if statErr != nil || modeString(info.Mode()) != want {
				return fmt.Errorf("destination mode repair failed: %s", relative)
			}
			return syncDirectory(filepath.Join(commonDir, filepath.Dir(clean)))
		}
		return nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := fmt.Sprintf(".~%s.%x", base, nonce[:])
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, confinedLinkSourceMode(mode))
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = root.Remove(temporary)
		}
	}()
	if written, writeErr := file.Write(body); writeErr != nil {
		return writeErr
	} else if written != len(body) {
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Link(temporary, base); errors.Is(err, os.ErrExist) {
		return createConfinedExact(commonDir, relative, body, mode)
	} else if err != nil {
		return err
	}
	if err := root.Remove(temporary); err != nil {
		return err
	}
	removeTemporary = false
	if err := finalizeConfinedLink(root, base, mode); err != nil {
		return err
	}
	info, err := root.Lstat(base)
	if err != nil {
		return err
	}
	if err := verifyConfinedFile(root, base, info, body, mode); err != nil {
		return err
	}
	return syncDirectory(filepath.Join(commonDir, filepath.Dir(clean)))
}

func verifyConfinedFile(root *os.Root, base string, info os.FileInfo, body []byte, mode os.FileMode) error {
	if err := verifyConfinedContent(root, base, info, body); err != nil {
		return err
	}
	if modeString(info.Mode()) != modeString(mode) {
		return fmt.Errorf("file mode is %s, want %s", modeString(info.Mode()), modeString(mode))
	}
	return nil
}

func verifyConfinedContent(root *os.Root, base string, info os.FileInfo, body []byte) error {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is not a regular file")
	}
	file, err := root.Open(base)
	if err != nil {
		return err
	}
	existing, readErr := io.ReadAll(io.LimitReader(file, int64(len(body))+1))
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !bytes.Equal(existing, body) {
		return errors.New("file content does not match")
	}
	return nil
}

func loadPlan(path string) (Plan, error) {
	var plan Plan
	_, err := readJSONFileLimit(path, &plan, MaxPlanBytes)
	return plan, err
}

func writeJSON(path string, value any, mode os.FileMode, replace bool) error {
	body, err := normalizedJSON(value)
	if err != nil {
		return err
	}
	if replace {
		return atomicfile.Write(path, body, mode)
	}
	return createExact(path, body, mode)
}

func removeExactFile(path, expectedDigest string) error {
	body, err := safeio.ReadRegular(path, maxManifestBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if digest(body) != expectedDigest {
		return fmt.Errorf("refusing to remove changed migration output %s", path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func sealArchiveTree(root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive contains symlink %s", path)
		}
		if info.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("archive contains non-regular path %s", path)
		}
		return os.Chmod(path, 0o400)
	})
	if err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, directory := range directories {
		if err := os.Chmod(directory, 0o500); err != nil {
			return err
		}
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}
