package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
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

func TestDryRunDoesNotInventSplitParentCommits(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "new core\n")
	write(t, repo.Root, "docs.txt", "new docs\n")
	engineGit(t, repo.Root, "add", "core.txt", "docs.txt")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	index := engineGitRaw(t, repo.Root, "ls-files", "-s", "-z")
	target := "refs/heads/wip/agent/dry-run-parents"
	engineGit(t, repo.Root, "update-ref", target, head, "")

	result, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
		AllowedPaths: []string{"core.txt", "docs.txt"},
		Groups: []Group{
			{Message: "fix(core): preview core change", Files: []string{"core.txt"}},
			{Message: "docs(guide): preview docs change", Files: []string{"docs.txt"}},
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Commits) != 2 || result.Commits[0].Parent != head || result.Commits[1].Parent != "" {
		t.Fatalf("dry-run parents = %#v", result.Commits)
	}
	if result.Commits[0].Commit != "" || result.Commits[1].Commit != "" || result.RefUpdated {
		t.Fatalf("dry-run published commit evidence: %#v", result)
	}
	if got := engineGit(t, repo.Root, "rev-parse", target); got != head {
		t.Fatalf("dry-run moved ref: %s -> %s", head, got)
	}
	if got := engineGitRaw(t, repo.Root, "ls-files", "-s", "-z"); got != index {
		t.Fatal("dry-run changed source index")
	}
}

func TestVerificationEnvironmentDoesNotInheritCaptureIdentity(t *testing.T) {
	for name, value := range map[string]string{
		"GIT_INDEX_FILE":     "/foreign/index",
		"WIP_AGENT":          "foreign-agent",
		"WIP_CANDIDATE_TREE": "foreign-tree",
		"WIP_COMMIT_OBJECT":  "foreign-commit",
		"WIP_LANE":           "foreign-lane",
		"WIP_SESSION":        "foreign-session",
		"WIP_TARGET_REF":     "refs/heads/wip/foreign/lane",
	} {
		t.Setenv(name, value)
	}
	environment := verificationEnvironment("candidate-tree")
	values := map[string]string{}
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		values[strings.ToUpper(name)] = value
	}
	for _, name := range []string{"GIT_INDEX_FILE", "WIP_AGENT", "WIP_COMMIT_OBJECT", "WIP_LANE", "WIP_SESSION", "WIP_TARGET_REF"} {
		if _, exists := values[name]; exists {
			t.Fatalf("verification inherited %s", name)
		}
	}
	if values["WIP_CANDIDATE_TREE"] != "candidate-tree" {
		t.Fatalf("candidate tree = %q", values["WIP_CANDIDATE_TREE"])
	}
}

func TestOversizedIntentFailsBeforeRefPublication(t *testing.T) {
	repo := engineTestRepo(t)
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/oversized-intent"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	paths := make([]string, 6_000)
	for index := range paths {
		paths[index] = fmt.Sprintf("generated/%04d/%080d.txt", index, index)
	}
	result := Result{
		TargetRef:         target,
		ExpectedRef:       head,
		FinalCommit:       strings.Repeat("b", len(head)),
		SourceHead:        head,
		SourceIndexDigest: "sha256:" + strings.Repeat("c", 64),
		FinalTree:         strings.Repeat("d", len(head)),
		HookDigest:        "sha256:" + strings.Repeat("e", 64),
		RequestedPaths:    paths,
		Commits: []PlannedCommit{{
			Index: 1, Message: "feat(records): exercise bounded intent", Parent: head,
			Commit: strings.Repeat("b", len(head)), Tree: strings.Repeat("d", len(head)), ChangedPaths: paths,
		}},
	}
	if _, err := newIntent(result, paths); fail.Code(err) != "INTENT_WRITE_FAILED" {
		t.Fatalf("oversized intent error = %v (%s)", err, fail.Code(err))
	}
	if got := engineGit(t, repo.Root, "rev-parse", target); got != head {
		t.Fatalf("oversized intent moved ref: %s -> %s", head, got)
	}
	intentRoot := filepath.Join(repo.CommonDir, "wip", "v1", "intents")
	if entries, err := os.ReadDir(intentRoot); err == nil && len(entries) != 0 {
		t.Fatalf("oversized intent left durable records: %#v", entries)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
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

func TestPrivateIndexDirectoryCannotFollowSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform-specific privileges")
	}
	for _, test := range []struct {
		name string
		link string
	}{
		{name: "state root", link: "wip"},
		{name: "index directory", link: "wip/indexes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := engineTestRepo(t)
			outside := t.TempDir()
			link := filepath.Join(repo.GitDir, filepath.FromSlash(test.link))
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			write(t, repo.Root, "core.txt", "new core\n")
			engineGit(t, repo.Root, "add", "core.txt")
			head := engineGit(t, repo.Root, "rev-parse", "HEAD")
			target := "refs/heads/wip/agent/index-symlink"
			engineGit(t, repo.Root, "update-ref", target, head, "")
			_, err := Run(context.Background(), Options{
				Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
				AllowedPaths: []string{"core.txt"},
				Groups:       []Group{{Message: "fix(index): reject escaped private index", Files: []string{"core.txt"}}},
			})
			if fail.Code(err) != "TEMP_INDEX_FAILED" {
				t.Fatalf("error = %v (%s)", err, fail.Code(err))
			}
			if got := engineGit(t, repo.Root, "rev-parse", target); got != head {
				t.Fatalf("target moved despite escaped private index: %s", got)
			}
			entries, readErr := os.ReadDir(outside)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("private-index files escaped Git directory: %#v", entries)
			}
		})
	}
}

func TestPrivateIndexDirectoryCreationConvergesConcurrently(t *testing.T) {
	gitDirectory := t.TempDir()
	const workers = 16
	start := make(chan struct{})
	errorsByWorker := make([]error, workers)
	paths := make([]string, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			paths[index], errorsByWorker[index] = preparePrivateIndexRoot(gitDirectory)
		}()
	}
	close(start)
	wait.Wait()
	wanted := filepath.Join(gitDirectory, "wip", "indexes")
	for index, err := range errorsByWorker {
		if err != nil || paths[index] != wanted {
			t.Fatalf("worker %d path=%q err=%v", index, paths[index], err)
		}
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

func TestValidateAppliedChecksGitObjectsAgainstReceipt(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Intent, string)
	}{
		{
			name: "message",
			mutate: func(intent *Intent, _ string) {
				intent.Commits[0].Message = "fix(receipt): substitute another valid message"
			},
		},
		{
			name: "tree",
			mutate: func(intent *Intent, oldTree string) {
				intent.Commits[0].Tree = oldTree
				intent.FinalTree = oldTree
			},
		},
		{
			name: "changed path set",
			mutate: func(intent *Intent, _ string) {
				intent.Commits[0].ChangedPaths = []string{"core.txt"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := engineTestRepo(t)
			write(t, repo.Root, "core.txt", "new core\n")
			write(t, repo.Root, "docs.txt", "new docs\n")
			engineGit(t, repo.Root, "add", "core.txt", "docs.txt")
			head := engineGit(t, repo.Root, "rev-parse", "HEAD")
			oldTree := engineGit(t, repo.Root, "show", "-s", "--format=%T", head)
			target := "refs/heads/wip/agent/git-evidence"
			engineGit(t, repo.Root, "update-ref", target, head, "")
			result, err := Run(context.Background(), Options{
				Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
				AllowedPaths: []string{"core.txt", "docs.txt"},
				Groups: []Group{{
					Message: "fix(receipt): verify immutable Git evidence",
					Files:   []string{"core.txt", "docs.txt"},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, clean, err := ValidateApplied(context.Background(), repo, result.PlanID, result.PlanDigest, target, head); err != nil || clean {
				t.Fatalf("valid applied receipt clean=%v err=%v", clean, err)
			}

			intent, path, err := LoadIntent(repo, result.PlanID, result.PlanDigest)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&intent, oldTree)
			intent.PlanDigest, err = digestIntent(intent)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.MarshalIndent(intent, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ValidateApplied(context.Background(), repo, result.PlanID, intent.PlanDigest, target, head); fail.Code(err) != "CAPTURE_RECEIPT_MISMATCH" {
				t.Fatalf("mismatched %s receipt error = %v (%s)", test.name, err, fail.Code(err))
			}
		})
	}
}

func TestFutureIntentSchemaRequiresMigration(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "new core\n")
	engineGit(t, repo.Root, "add", "core.txt")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/future-intent"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	result, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
		AllowedPaths: []string{"core.txt"},
		Groups:       []Group{{Message: "fix(intent): require an explicit migration", Files: []string{"core.txt"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, path, err := LoadIntent(repo, result.PlanID, result.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	intent.SchemaVersion = intentSchemaVersion + 1
	body, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadIntent(repo, result.PlanID, result.PlanDigest); fail.Code(err) != "MIGRATION_REQUIRED" {
		t.Fatalf("future intent error = %v (%s)", err, fail.Code(err))
	}
}

func TestLoadIntentRejectsSelfConsistentInvalidStructure(t *testing.T) {
	repo := engineTestRepo(t)
	write(t, repo.Root, "core.txt", "new core\n")
	engineGit(t, repo.Root, "add", "core.txt")
	head := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/invalid-intent"
	engineGit(t, repo.Root, "update-ref", target, head, "")
	result, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: head, ExpectedSourceHead: head,
		AllowedPaths: []string{"core.txt"},
		Groups:       []Group{{Message: "fix(intent): reject invalid structure", Files: []string{"core.txt"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, path, err := LoadIntent(repo, result.PlanID, result.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	intent.Commits[0].ChangedPaths = []string{"../outside"}
	intent.PlanDigest, err = digestIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadIntent(repo, result.PlanID, intent.PlanDigest); fail.Code(err) != "INVALID_INTENT" {
		t.Fatalf("invalid self-consistent intent error = %v (%s)", err, fail.Code(err))
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
