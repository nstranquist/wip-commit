package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
)

func TestRunSplitPlanPreservesHeadAndFullIndex(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "new core\n")
	write(t, repo.Root, "docs.txt", "new docs\n")
	write(t, repo.Root, "foreign.txt", "foreign staged\n")
	engineGit(t, repo.Root, "add", "core.txt", "docs.txt", "foreign.txt")
	headBefore := engineGit(t, repo.Root, "rev-parse", "HEAD")
	indexBefore := engineGitRaw(t, repo.Root, "ls-files", "-s", "-z")
	target := "refs/heads/wip/agent/split"
	engineGit(t, repo.Root, "update-ref", target, headBefore, "")
	result, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: headBefore, ExpectedSourceHead: headBefore,
		AllowedPaths: []string{"core.txt", "docs.txt"},
		Groups: []Group{
			{Message: "feat(core): capture exact core change", Files: []string{"core.txt"}},
			{Message: "docs(guide): capture exact docs change", Files: []string{"docs.txt"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RefUpdated || result.IntentState != "applied" || len(result.Commits) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if got := engineGit(t, repo.Root, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD moved: %s -> %s", headBefore, got)
	}
	if got := engineGitRaw(t, repo.Root, "ls-files", "-s", "-z"); got != indexBefore {
		t.Fatalf("shared index changed:\nwant %q\n got %q", indexBefore, got)
	}
	if got := engineGit(t, repo.Root, "show", result.FinalCommit+":foreign.txt"); got != "base foreign" {
		t.Fatalf("foreign staged content leaked into lane tree: %q", got)
	}
	if got := engineGit(t, repo.Root, "rev-parse", target); got != result.FinalCommit {
		t.Fatalf("target = %s, want %s", got, result.FinalCommit)
	}
	intent, _, err := LoadIntent(repo, result.PlanID, result.PlanDigest)
	if err != nil || intent.State != "applied" {
		t.Fatalf("intent = %#v, %v", intent, err)
	}
	if _, err := MarkIntent(repo, result.PlanID, result.PlanDigest, "complete"); err != nil {
		t.Fatal(err)
	}
	if _, clean, err := ValidateApplied(context.Background(), repo, result.PlanID, result.PlanDigest, target, result.FinalCommit); err != nil || !clean {
		t.Fatalf("completed validation clean=%v err=%v", clean, err)
	}
}

func TestRunCapturesPartialHunkWithoutTouchingWorktree(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "partial.txt", "one\ntwo\n")
	engineGit(t, repo.Root, "add", "partial.txt")
	engineGit(t, repo.Root, "commit", "-m", "test: add partial fixture")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	write(t, repo.Root, "partial.txt", "ONE\ntwo\n")
	engineGit(t, repo.Root, "add", "partial.txt")
	write(t, repo.Root, "partial.txt", "ONE\nTWO\n")
	indexBefore := engineGitRaw(t, repo.Root, "ls-files", "-s", "-z")
	target := "refs/heads/wip/agent/partial"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	result, err := Run(context.Background(), Options{Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head, AllowedPaths: []string{"partial.txt"}, Groups: []Group{{Message: "fix(index): capture only staged hunk", Files: []string{"partial.txt"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := engineGit(t, repo.Root, "show", result.FinalCommit+":partial.txt"); got != "ONE\ntwo" {
		t.Fatalf("captured content = %q", got)
	}
	body, _ := os.ReadFile(filepath.Join(repo.Root, "partial.txt"))
	if string(body) != "ONE\nTWO\n" {
		t.Fatalf("worktree changed: %q", body)
	}
	if got := engineGitRaw(t, repo.Root, "ls-files", "-s", "-z"); got != indexBefore {
		t.Fatal("shared index changed")
	}
}

func TestRunIsAtomicWhenLateVerifyFails(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "new core\n")
	write(t, repo.Root, "docs.txt", "new docs\n")
	engineGit(t, repo.Root, "add", "core.txt", "docs.txt")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/failing"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	result, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
		AllowedPaths: []string{"core.txt", "docs.txt"},
		Groups: []Group{
			{Message: "feat(core): prepare core change", Files: []string{"core.txt"}},
			{Message: "docs(guide): prepare docs change", Files: []string{"docs.txt"}, Verify: []VerifyCommand{{Argv: []string{"wip-command-that-must-not-exist"}}}},
		},
	})
	if fail.Code(err) != "VERIFY_FAILED" {
		t.Fatalf("error = %v (%s), result=%#v", err, fail.Code(err), result)
	}
	if got := engineGit(t, repo.Root, "rev-parse", target); got != head {
		t.Fatalf("target moved after late failure: %s", got)
	}
	if result.RefUpdated || result.PlanID != "" {
		t.Fatalf("failure was published: %#v", result)
	}
}

func TestRunDetectsSelectedIndexMovementAfterFinalGate(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "first staged value\n")
	engineGit(t, repo.Root, "add", "core.txt")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/index-race"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	_, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
		AllowedPaths: []string{"core.txt"}, Groups: []Group{{Message: "fix(index): detect late staged movement", Files: []string{"core.txt"}}},
		BeforePublish: func() error {
			write(t, repo.Root, "core.txt", "second staged value\n")
			engineGit(t, repo.Root, "add", "core.txt")
			return nil
		},
	})
	if fail.Code(err) != "SOURCE_INDEX_MOVED" {
		t.Fatalf("error = %v (%s)", err, fail.Code(err))
	}
	if got := engineGit(t, repo.Root, "rev-parse", target); got != head {
		t.Fatalf("target moved despite index race: %s", got)
	}
}

func TestRunTreatsRenameAsDeleteAndAdd(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "old-name.txt", "rename me\n")
	engineGit(t, repo.Root, "add", "old-name.txt")
	engineGit(t, repo.Root, "commit", "-m", "test: add rename fixture")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	if err := os.Rename(filepath.Join(repo.Root, "old-name.txt"), filepath.Join(repo.Root, "new-name.txt")); err != nil {
		t.Fatal(err)
	}
	engineGit(t, repo.Root, "add", "-A", "--", "old-name.txt", "new-name.txt")
	target := "refs/heads/wip/agent/rename"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	result, err := Run(context.Background(), Options{Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head, AllowedPaths: []string{"old-name.txt", "new-name.txt"}, Groups: []Group{{Message: "refactor(files): rename tracked fixture", Files: []string{"old-name.txt", "new-name.txt"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"new-name.txt", "old-name.txt"}
	got := append([]string(nil), result.Commits[0].ChangedPaths...)
	sort.Strings(got)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("changed paths = %q, want %q", got, want)
	}
}

func TestRunHandlesNewlineFilename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git for Windows filename rules differ")
	}
	repo := engineTestRepo(t)
	name := "odd\nname.txt"
	write(t, repo.Root, name, "odd value\n")
	engineGit(t, repo.Root, "add", "--", name)
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/odd-name"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	result, err := Run(context.Background(), Options{Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head, AllowedPaths: []string{name}, Groups: []Group{{Message: "test(paths): capture newline filename", Files: []string{name}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Commits[0].ChangedPaths) != 1 || result.Commits[0].ChangedPaths[0] != name {
		t.Fatalf("changed paths = %#v", result.Commits[0].ChangedPaths)
	}
}

func TestVerifyDirectoryCannotFollowSymlinkOutsideCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform-specific privileges")
	}
	repo := engineTestRepo(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo.Root, "escape")); err != nil {
		t.Fatal(err)
	}
	engineGit(t, repo.Root, "add", "escape")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/symlink"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	_, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
		AllowedPaths: []string{"escape"},
		Groups: []Group{{
			Message: "test(verify): reject escaped directory",
			Files:   []string{"escape"},
			Verify:  []VerifyCommand{{Argv: []string{"git", "--version"}, Directory: "escape"}},
		}},
	})
	if fail.Code(err) != "INVALID_VERIFY_COMMAND" {
		t.Fatalf("error = %v (%s)", err, fail.Code(err))
	}
	if got := engineGit(t, repo.Root, "rev-parse", target); got != head {
		t.Fatalf("target moved despite escaped verify directory: %s", got)
	}
}

func TestIntentDigestAndCompletedRefAreRevalidated(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "new core\n")
	engineGit(t, repo.Root, "add", "core.txt")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/receipt"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	result, err := Run(context.Background(), Options{Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head, AllowedPaths: []string{"core.txt"}, Groups: []Group{{Message: "fix(receipt): validate immutable plan", Files: []string{"core.txt"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkIntent(repo, result.PlanID, result.PlanDigest, "complete"); err != nil {
		t.Fatal(err)
	}
	engineGit(t, repo.Root, "update-ref", target, head, result.FinalCommit)
	if _, _, err := ValidateApplied(context.Background(), repo, result.PlanID, result.PlanDigest, target, result.FinalCommit); fail.Code(err) != "REF_MOVED" {
		t.Fatalf("moved completed ref error = %v (%s)", err, fail.Code(err))
	}
	engineGit(t, repo.Root, "update-ref", target, result.FinalCommit, head)
	intent, path, err := LoadIntent(repo, result.PlanID, result.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	intent.Commits[0].Message = "fix(receipt): tamper immutable plan"
	body, _ := json.MarshalIndent(intent, "", "  ")
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ValidateApplied(context.Background(), repo, result.PlanID, result.PlanDigest, target, result.FinalCommit); fail.Code(err) != "INTENT_DIGEST_MISMATCH" {
		t.Fatalf("tampered receipt error = %v (%s)", err, fail.Code(err))
	}
}

func TestHookSnapshotMovementFailsBeforeRefUpdate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX hook")
	}
	repo := engineTestRepo(t)
	hook := engineGit(t, repo.Root, "rev-parse", "--git-path", "hooks/pre-commit")
	if !filepath.IsAbs(hook) {
		hook = filepath.Join(repo.Root, hook)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, repo.Root, "core.txt", "new core\n")
	engineGit(t, repo.Root, "add", "core.txt")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/hook-race"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	_, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
		AllowedPaths: []string{"core.txt"}, Groups: []Group{{Message: "fix(hooks): detect source movement", Files: []string{"core.txt"}}},
		BeforePublish: func() error {
			return os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700)
		},
	})
	if fail.Code(err) != "HOOK_SOURCE_MOVED" {
		t.Fatalf("error = %v (%s)", err, fail.Code(err))
	}
	if got := engineGit(t, repo.Root, "rev-parse", target); got != head {
		t.Fatalf("target moved despite hook race: %s", got)
	}
}

func TestPostHookSeesEachCommitPrivateTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX hook")
	}
	repo := engineTestRepo(t)
	hook := engineGit(t, repo.Root, "rev-parse", "--git-path", "hooks/post-commit")
	if !filepath.IsAbs(hook) {
		hook = filepath.Join(repo.Root, hook)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf '%s %s\\n' \"$WIP_COMMIT_OBJECT\" \"$(git write-tree)\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, repo.Root, "core.txt", "new core\n")
	write(t, repo.Root, "docs.txt", "new docs\n")
	engineGit(t, repo.Root, "add", "core.txt", "docs.txt")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/post-hook"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	var output bytes.Buffer
	var hookErrors bytes.Buffer
	result, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
		AllowedPaths: []string{"core.txt", "docs.txt"},
		Groups: []Group{
			{Message: "fix(core): capture core hook tree", Files: []string{"core.txt"}},
			{Message: "docs(guide): capture docs hook tree", Files: []string{"docs.txt"}},
		},
		Output: &output, ErrorOutput: &hookErrors,
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("post-hook output = %q, errors = %q", output.String(), hookErrors.String())
	}
	for index, line := range lines {
		want := result.Commits[index].Commit + " " + result.Commits[index].Tree
		if line != want {
			t.Fatalf("hook line %d = %q, want %q", index+1, line, want)
		}
	}
}

func engineTestRepo(t *testing.T) gitx.Repo {
	t.Helper()
	directory := t.TempDir()
	engineGit(t, directory, "init", "-b", "main")
	engineGit(t, directory, "config", "user.name", "WIP Tests")
	engineGit(t, directory, "config", "user.email", "wip@example.invalid")
	write(t, directory, "core.txt", "base core\n")
	write(t, directory, "docs.txt", "base docs\n")
	write(t, directory, "foreign.txt", "base foreign\n")
	engineGit(t, directory, "add", ".")
	engineGit(t, directory, "commit", "-m", "test: create fixture")
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func engineGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(engineGitRaw(t, directory, args...))
}

func engineGitRaw(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
