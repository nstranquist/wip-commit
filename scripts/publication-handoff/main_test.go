package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/wip-commit/internal/gitx"
)

func TestCollectGitEvidenceBindsOneCleanReviewedDelta(t *testing.T) {
	repo, bootstrap := publicationRepo(t)
	writePublicationFile(t, repo.Root, "docs/handoff.md", "handoff\n")
	publicationGit(t, repo.Root, "add", "docs/handoff.md")
	publicationGit(t, repo.Root, "commit", "-m", "docs(release): add handoff")

	evidence, err := collectGitEvidence(context.Background(), repo, bootstrap, []string{"docs/handoff.md"})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Candidate.ImmediateParent != bootstrap || evidence.Candidate.CommitCount != 1 || evidence.HistoryCommitCount != 2 {
		t.Fatalf("evidence = %#v", evidence)
	}
	if len(evidence.DeltaPaths) != 1 || evidence.DeltaPaths[0] != "docs/handoff.md" {
		t.Fatalf("delta paths = %#v", evidence.DeltaPaths)
	}
	if len(evidence.HistoryEmails) != 1 || evidence.HistoryEmails[0] != "public@example.invalid" {
		t.Fatalf("history emails = %#v", evidence.HistoryEmails)
	}

	writePublicationFile(t, repo.Root, "dirty.txt", "dirty\n")
	if _, err := collectGitEvidence(context.Background(), repo, bootstrap, []string{"docs/handoff.md"}); err == nil || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("dirty candidate error = %v", err)
	}
}

func TestCollectGitEvidenceRejectsUnexpectedHistoryState(t *testing.T) {
	t.Run("unexpected path", func(t *testing.T) {
		repo, bootstrap := publicationCandidate(t)
		if _, err := collectGitEvidence(context.Background(), repo, bootstrap, []string{"other.md"}); err == nil || !strings.Contains(err.Error(), "delta paths") {
			t.Fatalf("unexpected path error = %v", err)
		}
	})
	t.Run("remote", func(t *testing.T) {
		repo, bootstrap := publicationCandidate(t)
		publicationGit(t, repo.Root, "remote", "add", "origin", "https://example.invalid/repo.git")
		if _, err := collectGitEvidence(context.Background(), repo, bootstrap, []string{"docs/handoff.md"}); err == nil || !strings.Contains(err.Error(), "configured remotes") {
			t.Fatalf("remote error = %v", err)
		}
	})
	t.Run("tag", func(t *testing.T) {
		repo, bootstrap := publicationCandidate(t)
		publicationGit(t, repo.Root, "tag", "v0.1.0-beta.1")
		if _, err := collectGitEvidence(context.Background(), repo, bootstrap, []string{"docs/handoff.md"}); err == nil || !strings.Contains(err.Error(), "local tags") {
			t.Fatalf("tag error = %v", err)
		}
	})
	t.Run("replacement ref", func(t *testing.T) {
		repo, bootstrap := publicationCandidate(t)
		publicationGit(t, repo.Root, "replace", "HEAD", bootstrap)
		if _, err := collectGitEvidence(context.Background(), repo, bootstrap, []string{"docs/handoff.md"}); err == nil || !strings.Contains(err.Error(), "replacement refs") {
			t.Fatalf("replacement ref error = %v", err)
		}
	})
	t.Run("merge commit", func(t *testing.T) {
		repo, bootstrap := publicationRepo(t)
		publicationGit(t, repo.Root, "switch", "-c", "other")
		writePublicationFile(t, repo.Root, "other.txt", "other\n")
		publicationGit(t, repo.Root, "add", "other.txt")
		publicationGit(t, repo.Root, "commit", "-m", "test: add other parent")
		publicationGit(t, repo.Root, "switch", "main")
		writePublicationFile(t, repo.Root, "docs/handoff.md", "handoff\n")
		publicationGit(t, repo.Root, "add", "docs/handoff.md")
		publicationGit(t, repo.Root, "commit", "-m", "docs(release): add handoff")
		publicationGit(t, repo.Root, "merge", "--no-ff", "other", "-m", "test: merge other parent")
		if _, err := collectGitEvidence(context.Background(), repo, bootstrap, []string{"docs/handoff.md", "other.txt"}); err == nil || !strings.Contains(err.Error(), "candidate range") {
			t.Fatalf("merge candidate error = %v", err)
		}
	})
}

func TestCollectGitEvidenceAcceptsReviewedSplitCommitRange(t *testing.T) {
	repo, bootstrap := publicationRepo(t)
	writePublicationFile(t, repo.Root, "scripts/preflight.go", "package scripts\n")
	publicationGit(t, repo.Root, "add", "scripts/preflight.go")
	publicationGit(t, repo.Root, "commit", "-m", "feat(release): add preflight")
	first := publicationGit(t, repo.Root, "rev-parse", "HEAD")
	writePublicationFile(t, repo.Root, "docs/handoff.md", "handoff\n")
	publicationGit(t, repo.Root, "add", "docs/handoff.md")
	publicationGit(t, repo.Root, "commit", "-m", "docs(release): add handoff")

	evidence, err := collectGitEvidence(context.Background(), repo, bootstrap, []string{"docs/handoff.md", "scripts/preflight.go"})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Candidate.CommitCount != 2 || evidence.Candidate.ImmediateParent != first {
		t.Fatalf("candidate evidence = %#v", evidence.Candidate)
	}
}

func TestCollectGitEvidenceRejectsShallowHistory(t *testing.T) {
	source, _ := publicationCandidate(t)
	parent := t.TempDir()
	clone := filepath.Join(parent, "shallow")
	publicationGit(t, parent, "clone", "--depth=1", "--no-local", source.Root, clone)
	publicationGit(t, clone, "remote", "remove", "origin")
	repo, err := gitx.Discover(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}
	head := publicationGit(t, clone, "rev-parse", "HEAD")
	if _, err := collectGitEvidence(context.Background(), repo, head, []string{"docs/handoff.md"}); err == nil || !strings.Contains(err.Error(), "non-shallow") {
		t.Fatalf("shallow history error = %v", err)
	}
}

func TestExpectedPathsAndApprovalFailClosed(t *testing.T) {
	root := t.TempDir()
	repo := gitx.Repo{Root: root}
	paths := filepath.Join(root, "paths.txt")
	if err := os.WriteFile(paths, []byte("docs/a.md\nscripts/tool.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadExpectedPaths(repo, paths)
	if err != nil || strings.Join(got, ",") != "docs/a.md,scripts/tool.go" {
		t.Fatalf("paths = %#v, %v", got, err)
	}
	if err := os.WriteFile(paths, []byte("scripts/tool.go\ndocs/a.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadExpectedPaths(repo, paths); err == nil {
		t.Fatal("unsorted expected paths passed")
	}
	link := filepath.Join(root, "paths-link.txt")
	if err := os.Symlink(paths, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadExpectedPaths(repo, link); err == nil || !strings.Contains(err.Error(), "stable regular file") {
		t.Fatalf("symlink expected paths error = %v", err)
	}

	tracker := filepath.Join(root, "requirements.json")
	body := `{"requirements":[{"id":"OSS-001","status":"verified","human_gate":false,"evidence":[{"kind":"owner-approval","value":"owner-session-2026-08-16"}]}]}`
	if err := os.WriteFile(tracker, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	approval, err := loadOwnerApproval(tracker, approvedApprovalReference)
	if err != nil || approval.Reference != "owner-session-2026-08-16" || !approval.Name || !approval.ModulePath || !approval.CaptureOnly {
		t.Fatalf("approval = %#v, %v", approval, err)
	}
}

func TestVerifyModuleTarget(t *testing.T) {
	root := t.TempDir()
	writePublicationFile(t, root, "go.mod", "module github.com/nstranquist/wip-commit\n\ngo 1.25.0\n")
	if err := verifyModuleTarget(root, approvedTarget); err != nil {
		t.Fatal(err)
	}
	writePublicationFile(t, root, "go.mod", "module example.invalid/other\n")
	if err := verifyModuleTarget(root, approvedTarget); err == nil {
		t.Fatal("wrong module target passed")
	}
}

func TestCommandFailureOutputIncludesBothStreams(t *testing.T) {
	got := commandFailureOutput("finding on stdout\n", "diagnostic on stderr\n")
	if got != "finding on stdout\ndiagnostic on stderr" {
		t.Fatalf("failure output = %q", got)
	}
	if got := commandFailureOutput("", ""); got != "no diagnostic output" {
		t.Fatalf("empty failure output = %q", got)
	}
}

func TestWriteReceiptRefusesCheckoutAndOverwrite(t *testing.T) {
	root := t.TempDir()
	value := receipt{SchemaVersion: receiptSchemaVersion, GeneratedAt: time.Unix(1_700_000_000, 0).UTC()}
	if err := writeReceipt(root, filepath.Join(root, "receipt.json"), value); err == nil {
		t.Fatal("receipt writer accepted a path in the candidate checkout")
	}
	out := filepath.Join(t.TempDir(), "receipt.json")
	if err := writeReceipt(root, out, value); err != nil {
		t.Fatal(err)
	}
	if err := writeReceipt(root, out, value); err == nil {
		t.Fatal("receipt writer overwrote an existing receipt")
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var decoded receipt
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.SchemaVersion != receiptSchemaVersion {
		t.Fatalf("receipt = %#v, %v", decoded, err)
	}
}

func publicationCandidate(t *testing.T) (gitx.Repo, string) {
	t.Helper()
	repo, bootstrap := publicationRepo(t)
	writePublicationFile(t, repo.Root, "docs/handoff.md", "handoff\n")
	publicationGit(t, repo.Root, "add", "docs/handoff.md")
	publicationGit(t, repo.Root, "commit", "-m", "docs(release): add handoff")
	return repo, bootstrap
}

func publicationRepo(t *testing.T) (gitx.Repo, string) {
	t.Helper()
	directory := t.TempDir()
	publicationGit(t, directory, "init", "-b", "main")
	publicationGit(t, directory, "config", "user.name", "Public Author")
	publicationGit(t, directory, "config", "user.email", "public@example.invalid")
	writePublicationFile(t, directory, "README.md", "base\n")
	publicationGit(t, directory, "add", "README.md")
	publicationGit(t, directory, "commit", "-m", "chore: create fixture")
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	return repo, publicationGit(t, directory, "rev-parse", "HEAD")
}

func writePublicationFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func publicationGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = gitx.Environment(nil)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
