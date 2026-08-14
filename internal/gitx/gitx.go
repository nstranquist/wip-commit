package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/pathid"
	"github.com/nstranquist/wip-commit/internal/process"
)

type Repo struct {
	Root      string
	CommonDir string
	GitDir    string
}

func Discover(ctx context.Context, directory string) (Repo, error) {
	if directory == "" {
		var err error
		directory, err = os.Getwd()
		if err != nil {
			return Repo{}, fail.Wrap("NOT_A_REPOSITORY", err)
		}
	}
	root, err := commandText(ctx, directory, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repo{}, fail.Wrap("NOT_A_REPOSITORY", err)
	}
	common, err := commandText(ctx, root, nil, "rev-parse", "--git-common-dir")
	if err != nil {
		return Repo{}, fail.Wrap("GIT_FAILED", err)
	}
	gitDir, err := commandText(ctx, root, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return Repo{}, fail.Wrap("GIT_FAILED", err)
	}
	root, err = canonical(root)
	if err != nil {
		return Repo{}, fail.Wrap("INVALID_PATH", err)
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	common, err = canonical(common)
	if err != nil {
		return Repo{}, fail.Wrap("INVALID_PATH", err)
	}
	gitDir, err = canonical(gitDir)
	if err != nil {
		return Repo{}, fail.Wrap("INVALID_PATH", err)
	}
	return Repo{Root: root, CommonDir: common, GitDir: gitDir}, nil
}

func canonical(path string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}

func (repo Repo) Raw(ctx context.Context, env []string, args ...string) (string, error) {
	return commandRaw(ctx, repo.Root, env, args...)
}

func (repo Repo) Text(ctx context.Context, env []string, args ...string) (string, error) {
	return commandText(ctx, repo.Root, env, args...)
}

func (repo Repo) Lines(ctx context.Context, env []string, args ...string) ([]string, error) {
	text, err := repo.Text(ctx, env, args...)
	if err != nil || text == "" {
		return nil, err
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

func (repo Repo) NULPaths(ctx context.Context, env []string, args ...string) ([]string, error) {
	text, err := repo.Raw(ctx, env, args...)
	if err != nil || text == "" {
		return nil, err
	}
	rows := strings.Split(text, "\x00")
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if row != "" {
			out = append(out, row)
		}
	}
	return out, nil
}

func (repo Repo) Exit(ctx context.Context, env []string, args ...string) (int, error) {
	cmd := process.CommandContext(ctx, "git", append([]string{"-C", repo.Root}, args...)...)
	cmd.Env = mergedEnvironment(env)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(stderr.String()), err)
}

func (repo Repo) IndexEntries(ctx context.Context, env []string) (map[string]string, error) {
	output, err := repo.Raw(ctx, env, "ls-files", "-s", "-z")
	if err != nil {
		return nil, err
	}
	entries := map[string]string{}
	for _, row := range strings.Split(output, "\x00") {
		if row == "" {
			continue
		}
		parts := strings.SplitN(row, "\t", 2)
		if len(parts) == 2 {
			entries[parts[1]] = parts[0]
		}
	}
	return entries, nil
}

func (repo Repo) NormalizePaths(input []string) ([]string, error) {
	seen := map[string]string{}
	out := make([]string, 0, len(input))
	for _, raw := range input {
		if raw == "" || strings.ContainsRune(raw, '\x00') || !utf8.ValidString(raw) {
			return nil, fail.New("INVALID_PATH", "path must be non-empty valid UTF-8 without NUL")
		}
		absolute := raw
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(repo.Root, raw)
		}
		absolute, err := filepath.Abs(absolute)
		if err != nil {
			return nil, fail.Wrap("INVALID_PATH", err)
		}
		relative, err := filepath.Rel(repo.Root, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fail.Errorf("PATH_OUTSIDE_REPO", "path %q is outside %s", raw, repo.Root)
		}
		relative = filepath.ToSlash(filepath.Clean(relative))
		if relative == "." {
			return nil, fail.New("INVALID_PATH", "repository root is too broad; pass explicit paths")
		}
		gitKey, relativeKey := pathid.Key(".git"), pathid.Key(relative)
		if relativeKey == gitKey || strings.HasPrefix(relativeKey, gitKey+"/") {
			return nil, fail.New("INVALID_PATH", "Git-internal paths cannot be selected")
		}
		key := pathid.Key(relative)
		if prior, exists := seen[key]; exists && prior != relative {
			return nil, fail.Errorf("PATH_ALIAS_CONFLICT", "paths %q and %q have the same portable identity", prior, relative)
		}
		if _, exists := seen[key]; !exists {
			seen[key] = relative
			out = append(out, relative)
		}
	}
	sort.Strings(out)
	return out, nil
}

func commandText(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	output, err := commandRaw(ctx, dir, env, args...)
	return strings.TrimSpace(output), err
}

func commandRaw(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := process.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = mergedEnvironment(env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(stderr.String()), err)
	}
	return stdout.String(), nil
}

func mergedEnvironment(extra []string) []string {
	return append(append([]string(nil), os.Environ()...), extra...)
}
