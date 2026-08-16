package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/safeio"
	skillbundle "github.com/nstranquist/wip-commit/skills/wip-commit"
)

const (
	maxSkillEntries          = 16
	maxPortableSkillFileSize = 1 << 20
)

func portableSkillPath(directory string) string {
	return filepath.Join(directory, "wip-commit")
}

func inspectPortableSkill(directory string) (path string, exists, valid bool, err error) {
	path = portableSkillPath(directory)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, false, false, nil
	}
	if err != nil {
		return path, false, false, fail.Wrap("SKILL_INSTALL_FAILED", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return path, true, false, fail.New("SKILL_INSTALL_CONFLICT", "portable skill target exists and is not a regular directory")
	}
	expected := make(map[string][]byte, len(skillbundle.Paths))
	expectedDirectories := map[string]bool{}
	for _, name := range skillbundle.Paths {
		body, readErr := skillbundle.FS.ReadFile(name)
		if readErr != nil {
			return path, true, false, fail.Wrap("SKILL_INSTALL_FAILED", readErr)
		}
		if len(body) > maxPortableSkillFileSize {
			return path, true, false, fail.Errorf("SKILL_INSTALL_FAILED", "embedded portable skill file exceeds %d bytes: %s", maxPortableSkillFileSize, name)
		}
		relative := filepath.FromSlash(name)
		expected[relative] = body
		for directory := filepath.Dir(relative); directory != "."; directory = filepath.Dir(directory) {
			expectedDirectories[directory] = true
		}
	}
	var files []string
	entries := 0
	err = filepath.WalkDir(path, func(candidate string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if candidate == path {
			return nil
		}
		entries++
		if entries > maxSkillEntries {
			return fail.Errorf("SKILL_INSTALL_CONFLICT", "portable skill target exceeds %d entries", maxSkillEntries)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fail.New("SKILL_INSTALL_CONFLICT", "portable skill target contains a symlink")
		}
		relative, relErr := filepath.Rel(path, candidate)
		if relErr != nil {
			return relErr
		}
		if entry.IsDir() {
			if !expectedDirectories[relative] {
				return fail.New("SKILL_INSTALL_CONFLICT", "portable skill target has an unexpected directory: "+filepath.ToSlash(relative))
			}
			return nil
		}
		files = append(files, relative)
		return nil
	})
	if err != nil {
		var typed *fail.Error
		if errors.As(err, &typed) {
			return path, true, false, err
		}
		return path, true, false, fail.Wrap("SKILL_INSTALL_FAILED", err)
	}
	sort.Strings(files)
	for _, relative := range files {
		wanted, ok := expected[relative]
		if !ok {
			return path, true, false, fail.New("SKILL_INSTALL_CONFLICT", "portable skill target has an unexpected file: "+filepath.ToSlash(relative))
		}
		body, readErr := safeio.ReadRegular(filepath.Join(path, relative), maxPortableSkillFileSize)
		if readErr != nil {
			return path, true, false, fail.Wrap("SKILL_INSTALL_FAILED", readErr)
		}
		if !bytes.Equal(body, wanted) {
			return path, true, false, fail.New("SKILL_INSTALL_CONFLICT", "portable skill target differs from the embedded skill: "+filepath.ToSlash(relative))
		}
	}
	return path, true, len(files) == len(expected), nil
}

func installPortableSkill(directory string) (path string, installed, alreadyValid bool, err error) {
	path, exists, valid, err := inspectPortableSkill(directory)
	if err != nil {
		return path, false, false, err
	}
	if exists && valid {
		return path, false, true, nil
	}
	createdRoot := false
	if !exists {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return path, false, false, fail.Wrap("SKILL_INSTALL_FAILED", err)
		}
		mkdirErr := os.Mkdir(path, 0o755)
		if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return path, false, false, fail.Wrap("SKILL_INSTALL_FAILED", mkdirErr)
		}
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			if statErr != nil {
				return path, false, false, fail.Wrap("SKILL_INSTALL_FAILED", statErr)
			}
			return path, false, false, fail.New("SKILL_INSTALL_CONFLICT", "portable skill target is not a regular directory")
		}
		createdRoot = mkdirErr == nil
	}
	var createdFiles, createdDirectories []string
	for _, name := range skillbundle.Paths {
		body, readErr := skillbundle.FS.ReadFile(name)
		if readErr != nil {
			return path, false, false, fail.Wrap("SKILL_INSTALL_FAILED", readErr)
		}
		if len(body) > maxPortableSkillFileSize {
			return path, false, false, fail.Errorf("SKILL_INSTALL_FAILED", "embedded portable skill file exceeds %d bytes: %s", maxPortableSkillFileSize, name)
		}
		target := filepath.Join(path, filepath.FromSlash(name))
		parent := filepath.Dir(target)
		if parent != path {
			info, statErr := os.Lstat(parent)
			if errors.Is(statErr, os.ErrNotExist) {
				mkdirErr := os.Mkdir(parent, 0o755)
				if mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
					return path, false, false, fail.Wrap("SKILL_INSTALL_FAILED", mkdirErr)
				}
				if mkdirErr == nil {
					createdDirectories = append(createdDirectories, parent)
				}
				info, statErr = os.Lstat(parent)
			}
			if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				if statErr != nil {
					return path, false, false, fail.Wrap("SKILL_INSTALL_FAILED", statErr)
				}
				return path, false, false, fail.New("SKILL_INSTALL_CONFLICT", "portable skill install directory is not a regular directory")
			}
		}
		existing, readErr := safeio.ReadRegular(target, maxPortableSkillFileSize)
		if readErr == nil {
			if !bytes.Equal(existing, body) {
				return path, false, false, fail.New("SKILL_INSTALL_CONFLICT", "portable skill target differs from the embedded skill: "+filepath.ToSlash(name))
			}
			continue
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return path, false, false, fail.Wrap("SKILL_INSTALL_FAILED", readErr)
		}
		if err := atomicfile.CreateWithTempDir(target, directory, body, 0o644); err != nil {
			if errors.Is(err, atomicfile.ErrExists) {
				existing, readErr = safeio.ReadRegular(target, maxPortableSkillFileSize)
				if readErr == nil && bytes.Equal(existing, body) {
					continue
				}
				return path, false, false, fail.New("SKILL_INSTALL_CONFLICT", "portable skill target changed during installation: "+filepath.ToSlash(name))
			}
			return path, false, false, fail.Wrap("SKILL_INSTALL_FAILED", err)
		}
		createdFiles = append(createdFiles, target)
	}
	_, _, valid, err = inspectPortableSkill(directory)
	if err != nil || !valid {
		if err == nil {
			err = fail.New("SKILL_INSTALL_FAILED", "portable skill verification failed after installation")
		}
		return path, false, false, err
	}
	changed := createdRoot || len(createdDirectories) > 0 || len(createdFiles) > 0
	return path, changed, !changed, nil
}
