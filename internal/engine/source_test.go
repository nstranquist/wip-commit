package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/policy"
)

func TestInspectWorktreeSourceIsExactAndDoesNotChangeCallerIndex(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "worktree core\n")
	write(t, repo.Root, "docs.txt", "staged docs\n")
	write(t, repo.Root, "new.txt", "untracked\n")
	engineGit(t, repo.Root, "add", "docs.txt")
	indexBefore := engineGitRaw(t, repo.Root, "ls-files", "-s", "-z")
	objectsBefore := objectNames(t, filepath.Join(repo.CommonDir, "objects"))
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")

	snapshot, err := InspectSource(context.Background(), repo, head, []string{"core.txt", "new.txt"}, SourceWorktree, UntrackedInclude)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Mode != SourceWorktree || snapshot.UntrackedPolicy != UntrackedInclude || len(snapshot.Paths) != 2 || snapshot.Paths[0] != "core.txt" || snapshot.Paths[1] != "new.txt" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Entries["core.txt"] == "" || snapshot.Entries["new.txt"] == "" || snapshot.Digest == "" {
		t.Fatalf("snapshot entries = %#v", snapshot)
	}
	if got := engineGitRaw(t, repo.Root, "ls-files", "-s", "-z"); got != indexBefore {
		t.Fatalf("caller index changed:\nwant %q\n got %q", indexBefore, got)
	}
	if got := objectNames(t, filepath.Join(repo.CommonDir, "objects")); !samePlanStringsForTest(got, objectsBefore) {
		t.Fatalf("inspection changed common object database:\nwant %q\n got %q", objectsBefore, got)
	}
}

func TestInspectWorktreeSourceRejectsUntrackedUnlessIncluded(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "new.txt", "untracked\n")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	if _, err := InspectSource(context.Background(), repo, head, []string{"new.txt"}, SourceWorktree, UntrackedForbid); failCode(err) != "UNTRACKED_PATH_FORBIDDEN" {
		t.Fatalf("error = %v, want UNTRACKED_PATH_FORBIDDEN", err)
	}
	if _, err := InspectSource(context.Background(), repo, head, []string{"new.txt"}, SourceStagedIndex, UntrackedInclude); failCode(err) != "INVALID_SOURCE_POLICY" {
		t.Fatalf("error = %v, want INVALID_SOURCE_POLICY", err)
	}
}

func TestInspectStagedSourceIgnoresUnstagedBytes(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "staged core\n")
	engineGit(t, repo.Root, "add", "core.txt")
	write(t, repo.Root, "core.txt", "unstaged core\n")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	snapshot, err := InspectSource(context.Background(), repo, head, []string{"core.txt"}, SourceStagedIndex, UntrackedNotApplicable)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Paths) != 1 || snapshot.Paths[0] != "core.txt" {
		t.Fatalf("paths = %q", snapshot.Paths)
	}
	staged, err := repo.IndexEntries(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Entries["core.txt"] != staged["core.txt"] {
		t.Fatalf("snapshot entry = %q, staged entry = %q", snapshot.Entries["core.txt"], staged["core.txt"])
	}
}

func TestRunCapturesWorktreeSnapshotWithoutUsingCallerIndex(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "worktree core\n")
	write(t, repo.Root, "new.txt", "worktree new\n")
	write(t, repo.Root, "foreign.txt", "foreign staged\n")
	engineGit(t, repo.Root, "add", "foreign.txt")
	indexBefore := engineGitRaw(t, repo.Root, "ls-files", "-s", "-z")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/worktree-source"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	inspection, err := InspectSource(context.Background(), repo, head, []string{"core.txt", "new.txt"}, SourceWorktree, UntrackedInclude)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
		SourceMode: SourceWorktree, UntrackedPolicy: UntrackedInclude,
		ExpectedSourceDigest: inspection.Digest, ReviewPlanDigest: "sha256:" + strings.Repeat("a", 64), MessagePolicy: policy.Default(),
		AllowedPaths: []string{"core.txt", "new.txt"},
		Groups: []Group{
			{Message: "fix(core): capture worktree source", Files: []string{"core.txt"}},
			{Message: "feat(core): add exact untracked source", Files: []string{"new.txt"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RefUpdated || result.SourceMode != SourceWorktree || result.UntrackedPolicy != UntrackedInclude || result.SourceDigest != inspection.Digest {
		t.Fatalf("result = %#v", result)
	}
	if got := engineGitRaw(t, repo.Root, "ls-files", "-s", "-z"); got != indexBefore {
		t.Fatalf("caller index changed:\nwant %q\n got %q", indexBefore, got)
	}
	if got := engineGit(t, repo.Root, "show", result.FinalCommit+":core.txt"); got != "worktree core" {
		t.Fatalf("captured core = %q", got)
	}
	if got := engineGit(t, repo.Root, "show", result.FinalCommit+":new.txt"); got != "worktree new" {
		t.Fatalf("captured new = %q", got)
	}
	if got := engineGit(t, repo.Root, "show", result.FinalCommit+":foreign.txt"); got != "base foreign" {
		t.Fatalf("foreign caller-index content leaked = %q", got)
	}
}

func TestRunRejectsWorktreeSourceMovementBeforeRefCAS(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "first worktree value\n")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/worktree-moved"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	inspection, err := InspectSource(context.Background(), repo, head, []string{"core.txt"}, SourceWorktree, UntrackedForbid)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
		SourceMode: SourceWorktree, UntrackedPolicy: UntrackedForbid,
		ExpectedSourceDigest: inspection.Digest, ReviewPlanDigest: "sha256:" + strings.Repeat("b", 64), MessagePolicy: policy.Default(),
		AllowedPaths: []string{"core.txt"}, Groups: []Group{{Message: "fix(core): reject moved source", Files: []string{"core.txt"}}},
		BeforePublish: func() error {
			write(t, repo.Root, "core.txt", "second worktree value\n")
			return nil
		},
	})
	if fail.Code(err) != "SOURCE_MOVED" || result.RefUpdated {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	if got := engineGit(t, repo.Root, "rev-parse", target); got != head {
		t.Fatalf("target moved to %s, want %s", got, head)
	}
}

func TestDryRunKeepsGeneratedGitObjectsOutsideRepository(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "dry-run core\n")
	engineGit(t, repo.Root, "add", "core.txt")
	objectsBefore := objectNames(t, filepath.Join(repo.CommonDir, "objects"))
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/dry-object-store"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	result, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
		AllowedPaths: []string{"core.txt"}, Groups: []Group{{Message: "fix(core): prove pure dry run", Files: []string{"core.txt"}}}, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != ResultSchemaVersion || result.RefUpdated || result.FinalCommit != "" || result.GateOutcome != "skipped" {
		t.Fatalf("result = %#v", result)
	}
	if result.Execution == nil || result.Execution.Policy != "no-exec" || result.Execution.PreCommitHooks != (ExecutionCount{}) || result.Execution.PostCommitHooks != (ExecutionCount{}) || result.Execution.VerifyCommands != (ExecutionCount{}) {
		t.Fatalf("dry-run execution report = %#v", result.Execution)
	}
	if got := objectNames(t, filepath.Join(repo.CommonDir, "objects")); !samePlanStringsForTest(got, objectsBefore) {
		t.Fatalf("dry-run changed common object database:\nwant %q\n got %q", objectsBefore, got)
	}
	if _, err := os.Lstat(filepath.Join(repo.GitDir, "wip")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created repository-private state: %v", err)
	}
}

func objectNames(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root || entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func samePlanStringsForTest(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func failCode(err error) string {
	if err == nil {
		return ""
	}
	return fail.Code(err)
}
