package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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

func TestBoundedOutputCapsDiagnosticsWithoutShortWrite(t *testing.T) {
	output := newBoundedOutput(4)
	written, err := output.Write([]byte("abcdef"))
	if err != nil || written != 6 {
		t.Fatalf("write = %d, %v", written, err)
	}
	if got := output.String(); got != "abcd\n[output truncated]" {
		t.Fatalf("bounded output = %q", got)
	}
	exact := newBoundedOutput(4)
	_, _ = exact.Write([]byte("ab"))
	_, _ = exact.Write([]byte("cd"))
	if got := exact.String(); got != "abcd" {
		t.Fatalf("exact bounded output = %q", got)
	}
}

func TestReceiptSchemaRequiresUTCDateTimeSyntax(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "PUBLICATION-HANDOFF.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Properties map[string]struct {
			Format  string `json:"format"`
			Pattern string `json:"pattern"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	generated, ok := document.Properties["generated_at"]
	if !ok || generated.Format != "date-time" || generated.Pattern == "" {
		t.Fatalf("generated_at schema = %#v", generated)
	}
	pattern, err := regexp.Compile(generated.Pattern)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"2026-08-16T22:47:35Z",
		"2026-08-16T22:47:35.123456789Z",
	} {
		if !pattern.MatchString(value) {
			t.Errorf("valid UTC timestamp %q did not match", value)
		}
	}
	for _, value := range []string{
		"not-a-time",
		"2026-08-16T22:47:35-05:00",
		"2026-08-16T22:47:35.1234567890Z",
	} {
		if pattern.MatchString(value) {
			t.Errorf("invalid UTC timestamp %q matched", value)
		}
	}
}

func TestWriteReceiptRefusesCheckoutAndOverwrite(t *testing.T) {
	root := t.TempDir()
	value := receipt{SchemaVersion: receiptSchemaVersion, GeneratedAt: time.Unix(1_700_000_000, 0).UTC()}
	if err := writeReceipt(root, filepath.Join(root, "receipt.json"), value); err == nil {
		t.Fatal("receipt writer accepted a path in the candidate checkout")
	}
	out := filepath.Join(privatePublicationDirectory(t), "receipt.json")
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

func TestWriteReceiptRequiresPrivateResolvedParent(t *testing.T) {
	root := t.TempDir()
	value := receipt{SchemaVersion: receiptSchemaVersion, GeneratedAt: time.Unix(1_700_000_000, 0).UTC()}
	outside := t.TempDir()
	link := filepath.Join(outside, "checkout-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := writeReceipt(root, filepath.Join(link, "receipt.json"), value); err == nil || !strings.Contains(err.Error(), "resolve outside") {
		t.Fatalf("symlinked checkout output error = %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	openDirectory := t.TempDir()
	if err := os.Chmod(openDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeReceipt(root, filepath.Join(openDirectory, "receipt.json"), value); err == nil || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("open receipt directory error = %v", err)
	}
}

func TestRunWritesBoundReceiptWithDeterministicExternalEvidence(t *testing.T) {
	repo, bootstrap := publicationCandidate(t)
	out := filepath.Join(privatePublicationDirectory(t), "receipt.json")
	fixed := time.Date(2026, time.August, 16, 22, 55, 0, 123456789, time.UTC)
	var stdout bytes.Buffer
	err := runWithDependencies(context.Background(), []string{
		"--repo-dir", repo.Root,
		"--target", approvedTarget,
		"--bootstrap", bootstrap,
		"--paths-file", "docs/paths.txt",
		"--out", out,
	}, &stdout, dependencies{
		external: publicationExternalFixture,
		now:      func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		OK              bool   `json:"ok"`
		CandidateCommit string `json:"candidate_commit"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.CandidateCommit == "" {
		t.Fatalf("stdout envelope = %#v", envelope)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got receipt
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.GeneratedAt.Equal(fixed) || got.Candidate.Commit != envelope.CandidateCommit || got.Candidate.CommitCount != 1 {
		t.Fatalf("receipt = %#v", got)
	}
	if !got.GitHub.AuthenticatedOwner || got.GitHub.TargetRepositoryExists || !got.GitHub.PublicAuthorIdentityMatch {
		t.Fatalf("GitHub evidence = %#v", got.GitHub)
	}
	if got.Local.HistorySecretScan != "passed" || got.Local.WorktreeSecretScan != "passed" || got.Local.ObjectIntegrityCheck != "passed" {
		t.Fatalf("local evidence = %#v", got.Local)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %v", info.Mode())
	}
}

func TestRunRejectsIncompleteDependencies(t *testing.T) {
	if err := runWithDependencies(context.Background(), nil, io.Discard, dependencies{}); err == nil || !strings.Contains(err.Error(), "dependencies") {
		t.Fatalf("incomplete dependency error = %v", err)
	}
}

func TestExternalCommandWithTimeout(t *testing.T) {
	t.Setenv("GO_WANT_PUBLICATION_HELPER", "1")
	arguments := func(mode string) []string {
		return []string{"-test.run=^TestPublicationExternalHelperProcess$", "--", mode}
	}
	stdout, stderr, code, err := externalCommandWithTimeout(context.Background(), 2*time.Second, t.TempDir(), os.Args[0], arguments("success")...)
	if err != nil || code != 0 || stdout != "stdout" || stderr != "stderr" {
		t.Fatalf("success = stdout %q, stderr %q, code %d, err %v", stdout, stderr, code, err)
	}
	_, _, code, err = externalCommandWithTimeout(context.Background(), 2*time.Second, t.TempDir(), os.Args[0], arguments("failure")...)
	if err != nil || code != 7 {
		t.Fatalf("failure = code %d, err %v", code, err)
	}
	_, _, code, err = externalCommandWithTimeout(context.Background(), 50*time.Millisecond, t.TempDir(), os.Args[0], arguments("timeout")...)
	if err == nil || code != -1 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout = code %d, err %v", code, err)
	}
}

func TestPublicationExternalHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PUBLICATION_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "success":
		_, _ = fmt.Fprint(os.Stdout, "stdout")
		_, _ = fmt.Fprint(os.Stderr, "stderr")
		os.Exit(0)
	case "failure":
		os.Exit(7)
	case "timeout":
		time.Sleep(10 * time.Second)
		os.Exit(0)
	default:
		os.Exit(8)
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

func privatePublicationDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func publicationRepo(t *testing.T) (gitx.Repo, string) {
	t.Helper()
	directory := t.TempDir()
	publicationGit(t, directory, "init", "-b", "main")
	publicationGit(t, directory, "config", "user.name", "Public Author")
	publicationGit(t, directory, "config", "user.email", "public@example.invalid")
	writePublicationFile(t, directory, "README.md", "base\n")
	writePublicationFile(t, directory, "go.mod", "module github.com/nstranquist/wip-commit\n\ngo 1.25.0\n")
	writePublicationFile(t, directory, "docs/paths.txt", "docs/handoff.md\n")
	writePublicationFile(t, directory, "docs/OSS-PUBLIC-BETA.requirements.yaml", `{"requirements":[{"id":"OSS-001","status":"verified","human_gate":false,"evidence":[{"kind":"owner-approval","value":"owner-session-2026-08-16"}]}]}`+"\n")
	publicationGit(t, directory, "add", "README.md", "go.mod", "docs/paths.txt", "docs/OSS-PUBLIC-BETA.requirements.yaml")
	publicationGit(t, directory, "commit", "-m", "chore: create fixture")
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	return repo, publicationGit(t, directory, "rev-parse", "HEAD")
}

func publicationExternalFixture(_ context.Context, _ time.Duration, _ string, name string, args ...string) (string, string, int, error) {
	command := name + " " + strings.Join(args, " ")
	switch command {
	case "gh api user --jq .login":
		return "nstranquist\n", "", 0, nil
	case "gh api users/nstranquist --jq .email":
		return "public@example.invalid\n", "", 0, nil
	case "gh api repos/nstranquist/wip-commit":
		return "", "gh: Not Found (HTTP 404)\n", 1, nil
	case "gitleaks git --redact --exit-code 1 .", "gitleaks dir --redact --exit-code 1 .":
		return "", "", 0, nil
	default:
		return "", "", -1, fmt.Errorf("unexpected external command %q", command)
	}
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
