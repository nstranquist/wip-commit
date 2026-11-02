package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/pathid"
)

type SourceMode string

const (
	SourceStagedIndex SourceMode = "staged_index"
	SourceWorktree    SourceMode = "worktree_snapshot"
)

type UntrackedPolicy string

const (
	UntrackedNotApplicable UntrackedPolicy = "not_applicable"
	UntrackedForbid        UntrackedPolicy = "forbid"
	UntrackedInclude       UntrackedPolicy = "include"
)

type SourceSnapshot struct {
	Mode            SourceMode
	UntrackedPolicy UntrackedPolicy
	BaseCommit      string
	Paths           []string
	Entries         map[string]string
	Digest          string
}

func NormalizeSource(mode SourceMode, untracked UntrackedPolicy) (SourceMode, UntrackedPolicy, error) {
	if mode == "" {
		mode = SourceStagedIndex
	}
	switch mode {
	case SourceStagedIndex:
		if untracked == "" {
			untracked = UntrackedNotApplicable
		}
		if untracked != UntrackedNotApplicable {
			return "", "", fail.New("INVALID_SOURCE_POLICY", "staged-index capture requires untracked policy not_applicable")
		}
	case SourceWorktree:
		if untracked == "" {
			untracked = UntrackedForbid
		}
		if untracked != UntrackedForbid && untracked != UntrackedInclude {
			return "", "", fail.New("INVALID_SOURCE_POLICY", "worktree snapshot requires untracked policy forbid or include")
		}
	default:
		return "", "", fail.Errorf("INVALID_SOURCE_POLICY", "unsupported source mode %q", mode)
	}
	return mode, untracked, nil
}

// InspectSource returns an exact source projection without changing the caller
// index or the repository object database. Worktree blobs and trees are held
// in a temporary object directory that is removed before this function
// returns.
func InspectSource(ctx context.Context, repo gitx.Repo, baseCommit string, scopes []string, mode SourceMode, untracked UntrackedPolicy) (SourceSnapshot, error) {
	mode, untracked, err := NormalizeSource(mode, untracked)
	if err != nil {
		return SourceSnapshot{}, err
	}
	scopes, err = repo.NormalizePaths(scopes)
	if err != nil {
		return SourceSnapshot{}, err
	}
	if len(scopes) == 0 {
		return SourceSnapshot{}, fail.New("EMPTY_SELECTION", "source inspection requires at least one explicit path scope")
	}
	if mode == SourceStagedIndex {
		paths, err := changedPaths(ctx, repo, nil, baseCommit, scopes, true)
		if err != nil {
			return SourceSnapshot{}, err
		}
		entries, err := repo.IndexEntries(ctx, nil)
		if err != nil {
			return SourceSnapshot{}, fail.Wrap("GIT_FAILED", err)
		}
		return newSourceSnapshot(mode, untracked, baseCommit, paths, entries), nil
	}

	root, err := os.MkdirTemp("", "wip-source-inspect-*")
	if err != nil {
		return SourceSnapshot{}, fail.Wrap("SOURCE_SNAPSHOT_FAILED", err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	return snapshotWorktree(ctx, repo, baseCommit, scopes, untracked, root, true)
}

func captureSource(ctx context.Context, repo gitx.Repo, baseCommit string, scopes []string, mode SourceMode, untracked UntrackedPolicy, privateRoot string, isolateObjects bool) (SourceSnapshot, func(), error) {
	mode, untracked, err := NormalizeSource(mode, untracked)
	if err != nil {
		return SourceSnapshot{}, func() {}, err
	}
	if mode == SourceStagedIndex {
		entries, err := repo.IndexEntries(ctx, nil)
		if err != nil {
			return SourceSnapshot{}, func() {}, fail.Wrap("GIT_FAILED", err)
		}
		return newSourceSnapshot(mode, untracked, baseCommit, scopes, entries), func() {}, nil
	}
	snapshot, indexPath, err := snapshotWorktreeAt(ctx, repo, baseCommit, scopes, untracked, privateRoot, isolateObjects)
	if err != nil {
		return SourceSnapshot{}, func() {}, err
	}
	return snapshot, func() { _ = os.Remove(indexPath) }, nil
}

func snapshotWorktree(ctx context.Context, repo gitx.Repo, baseCommit string, scopes []string, untracked UntrackedPolicy, root string, isolateObjects bool) (SourceSnapshot, error) {
	snapshot, indexPath, err := snapshotWorktreeAt(ctx, repo, baseCommit, scopes, untracked, root, isolateObjects)
	if indexPath != "" {
		_ = os.Remove(indexPath)
	}
	return snapshot, err
}

func snapshotWorktreeAt(ctx context.Context, repo gitx.Repo, baseCommit string, scopes []string, untracked UntrackedPolicy, root string, isolateObjects bool) (SourceSnapshot, string, error) {
	if _, err := repo.Text(ctx, nil, "rev-parse", "--verify", baseCommit+"^{commit}"); err != nil {
		return SourceSnapshot{}, "", fail.Wrap("SOURCE_BASE_INVALID", err)
	}
	file, err := os.CreateTemp(root, "source-*.index")
	if err != nil {
		return SourceSnapshot{}, "", fail.Wrap("SOURCE_SNAPSHOT_FAILED", err)
	}
	indexPath := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(indexPath)
		return SourceSnapshot{}, "", fail.Wrap("SOURCE_SNAPSHOT_FAILED", err)
	}
	if err := os.Remove(indexPath); err != nil {
		return SourceSnapshot{}, "", fail.Wrap("SOURCE_SNAPSHOT_FAILED", err)
	}
	environment := []string{"GIT_INDEX_FILE=" + indexPath}
	if isolateObjects {
		objectEnvironment, err := isolatedObjectEnvironment(repo, root)
		if err != nil {
			return SourceSnapshot{}, indexPath, err
		}
		environment = append(environment, objectEnvironment...)
	}
	if _, err := repo.Text(ctx, environment, "read-tree", baseCommit); err != nil {
		return SourceSnapshot{}, indexPath, fail.Wrap("SOURCE_SNAPSHOT_FAILED", err)
	}
	tracked, err := changedPaths(ctx, repo, environment, baseCommit, scopes, false)
	if err != nil {
		return SourceSnapshot{}, indexPath, err
	}
	untrackedPaths, err := repo.NULPaths(ctx, environment, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return SourceSnapshot{}, indexPath, fail.Wrap("SOURCE_SNAPSHOT_FAILED", err)
	}
	untrackedPaths = coveredPaths(untrackedPaths, scopes)
	if len(untrackedPaths) > 0 && untracked == UntrackedForbid {
		return SourceSnapshot{}, indexPath, fail.Errorf("UNTRACKED_PATH_FORBIDDEN", "worktree source contains %d untracked path(s) in the selected scope; review and use untracked policy include to capture them", len(untrackedPaths))
	}
	paths := canonicalSourcePaths(tracked, untrackedPaths)
	if len(paths) == 0 {
		return newSourceSnapshot(SourceWorktree, untracked, baseCommit, nil, map[string]string{}), indexPath, nil
	}
	// Git can reject an exact tracked file when one of its parent directories
	// is ignored. Update base-tracked entries separately so ignore rules cannot
	// hide them. Add only the already-enumerated untracked files with --force;
	// this never expands an ignored directory or weakens the explicit include
	// policy.
	if len(tracked) > 0 {
		args := append([]string{"add", "--update", "--"}, tracked...)
		if _, err := repo.Text(ctx, environment, args...); err != nil {
			return SourceSnapshot{}, indexPath, fail.Wrap("SOURCE_SNAPSHOT_FAILED", err)
		}
	}
	if len(untrackedPaths) > 0 {
		args := append([]string{"add", "--force", "--"}, untrackedPaths...)
		if _, err := repo.Text(ctx, environment, args...); err != nil {
			return SourceSnapshot{}, indexPath, fail.Wrap("SOURCE_SNAPSHOT_FAILED", err)
		}
	}
	entries, err := repo.IndexEntries(ctx, environment)
	if err != nil {
		return SourceSnapshot{}, indexPath, fail.Wrap("SOURCE_SNAPSHOT_FAILED", err)
	}
	return newSourceSnapshot(SourceWorktree, untracked, baseCommit, paths, entries), indexPath, nil
}

func isolatedObjectEnvironment(repo gitx.Repo, root string) ([]string, error) {
	objectRoot := filepath.Join(root, "objects")
	if err := os.Mkdir(objectRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fail.Wrap("SOURCE_SNAPSHOT_FAILED", err)
	}
	return []string{
		"GIT_OBJECT_DIRECTORY=" + objectRoot,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(repo.CommonDir, "objects"),
	}, nil
}

func changedPaths(ctx context.Context, repo gitx.Repo, environment []string, baseCommit string, scopes []string, cached bool) ([]string, error) {
	args := []string{"diff"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "--no-renames", "--name-only", "-z", baseCommit, "--")
	paths, err := repo.NULPaths(ctx, environment, args...)
	if err != nil {
		return nil, fail.Wrap("SOURCE_SNAPSHOT_FAILED", err)
	}
	return coveredPaths(paths, scopes), nil
}

func coveredPaths(paths, scopes []string) []string {
	selected := make([]string, 0, len(paths))
	for _, path := range paths {
		if pathid.Covered(path, scopes) {
			selected = append(selected, path)
		}
	}
	sort.Strings(selected)
	return selected
}

func canonicalSourcePaths(groups ...[]string) []string {
	seen := map[string]bool{}
	var paths []string
	for _, group := range groups {
		for _, path := range group {
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	sort.Strings(paths)
	return paths
}

func newSourceSnapshot(mode SourceMode, untracked UntrackedPolicy, baseCommit string, paths []string, entries map[string]string) SourceSnapshot {
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	selected := make(map[string]string)
	for path, entry := range entries {
		if pathid.Covered(path, paths) {
			selected[path] = entry
		}
	}
	return SourceSnapshot{
		Mode: mode, UntrackedPolicy: untracked, BaseCommit: strings.TrimSpace(baseCommit),
		Paths: paths, Entries: selected, Digest: selectedDigest(selected, paths),
	}
}
