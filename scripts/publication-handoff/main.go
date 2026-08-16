// Command publication-handoff creates a fail-closed receipt for the first
// public repository handoff. It does not create a repository, remote, tag, or
// Git ref, and it does not push.
package main

import (
	"bytes"
	"context"
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

	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/safeio"
)

const (
	receiptSchemaVersion      = "1.0.0"
	approvedTarget            = "nstranquist/wip-commit"
	approvedApprovalReference = "owner-session-2026-08-16"
	maximumPathsBytes         = 64 << 10
	maximumTrackerBytes       = 1 << 20
	maximumModuleBytes        = 64 << 10
	maximumCommandOutputBytes = 1 << 20
	githubCommandTimeout      = 30 * time.Second
	secretScanTimeout         = 5 * time.Minute
)

var objectPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type objectEvidence struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
}

type candidateEvidence struct {
	Commit          string `json:"commit"`
	Tree            string `json:"tree"`
	ImmediateParent string `json:"immediate_parent"`
	CommitCount     int    `json:"commit_count"`
}

type localEvidence struct {
	Clean                bool   `json:"clean"`
	RemoteCount          int    `json:"remote_count"`
	TagCount             int    `json:"tag_count"`
	HistoryEmailCount    int    `json:"history_identity_email_count"`
	HistorySecretScan    string `json:"history_secret_scan"`
	WorktreeSecretScan   string `json:"worktree_secret_scan"`
	ObjectIntegrityCheck string `json:"object_integrity_check"`
	ExpectedDeltaMatched bool   `json:"expected_delta_matched"`
	BootstrapIsAncestor  bool   `json:"bootstrap_is_ancestor"`
	LinearCandidateRange bool   `json:"linear_candidate_range"`
}

type githubEvidence struct {
	AuthenticatedOwner        bool `json:"authenticated_owner"`
	TargetRepositoryExists    bool `json:"target_repository_exists"`
	PublicAuthorIdentityMatch bool `json:"public_author_identity_match"`
}

type ownerApproval struct {
	Reference   string `json:"reference"`
	Name        bool   `json:"name"`
	ModulePath  bool   `json:"module_path"`
	CaptureOnly bool   `json:"capture_only"`
}

type authorityEvidence struct {
	AgentPushPermitted bool   `json:"agent_push_permitted"`
	RequiredActor      string `json:"required_actor"`
}

type receipt struct {
	SchemaVersion      string            `json:"schema_version"`
	GeneratedAt        time.Time         `json:"generated_at"`
	TargetRepository   string            `json:"target_repository"`
	TargetVisibility   string            `json:"target_visibility"`
	DefaultBranch      string            `json:"default_branch"`
	Bootstrap          objectEvidence    `json:"bootstrap"`
	Candidate          candidateEvidence `json:"candidate"`
	HistoryCommitCount int               `json:"history_commit_count"`
	DeltaPaths         []string          `json:"delta_paths"`
	Local              localEvidence     `json:"local"`
	GitHub             githubEvidence    `json:"github"`
	OwnerApproval      ownerApproval     `json:"owner_approval"`
	Authority          authorityEvidence `json:"authority"`
}

type gitEvidence struct {
	Bootstrap          objectEvidence
	Candidate          candidateEvidence
	HistoryCommitCount int
	DeltaPaths         []string
	HistoryEmails      []string
}

type requirementTracker struct {
	Requirements []struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		HumanGate bool   `json:"human_gate"`
		Evidence  []struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		} `json:"evidence"`
	} `json:"requirements"`
}

type externalRunner func(context.Context, time.Duration, string, string, ...string) (string, string, int, error)

type dependencies struct {
	external externalRunner
	now      func() time.Time
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "publication-handoff:", err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	return runWithDependencies(ctx, args, stdout, dependencies{
		external: externalCommandWithTimeout,
		now:      time.Now,
	})
}

func runWithDependencies(ctx context.Context, args []string, stdout io.Writer, deps dependencies) error {
	if deps.external == nil || deps.now == nil {
		return errors.New("publication dependencies are incomplete")
	}
	flags := flag.NewFlagSet("publication-handoff", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repoDir := flags.String("repo-dir", ".", "candidate checkout")
	target := flags.String("target", "nstranquist/wip-commit", "GitHub OWNER/REPO")
	bootstrap := flags.String("bootstrap", "", "exact bootstrap commit")
	pathsFile := flags.String("paths-file", "", "repository-relative expected-path file")
	out := flags.String("out", "", "new receipt outside the checkout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *target != approvedTarget {
		return fmt.Errorf("--target must equal the approved target %s", approvedTarget)
	}
	if !objectPattern.MatchString(*bootstrap) {
		return errors.New("--bootstrap must be a complete Git object ID")
	}
	if strings.TrimSpace(*pathsFile) == "" {
		return errors.New("--paths-file is required")
	}
	if strings.TrimSpace(*out) == "" {
		return errors.New("--out is required")
	}

	repo, err := gitx.Discover(ctx, *repoDir)
	if err != nil {
		return err
	}
	if err := verifyModuleTarget(repo.Root, *target); err != nil {
		return err
	}
	expectedPaths, err := loadExpectedPaths(repo, *pathsFile)
	if err != nil {
		return err
	}
	gitState, err := collectGitEvidence(ctx, repo, *bootstrap, expectedPaths)
	if err != nil {
		return err
	}
	approval, err := loadOwnerApproval(filepath.Join(repo.Root, "docs", "OSS-PUBLIC-BETA.requirements.yaml"), approvedApprovalReference)
	if err != nil {
		return err
	}
	githubState, err := collectGitHubEvidence(ctx, repo.Root, *target, gitState.HistoryEmails, deps.external)
	if err != nil {
		return err
	}
	if err := runSecretScans(ctx, repo.Root, deps.external); err != nil {
		return err
	}

	result := receipt{
		SchemaVersion:      receiptSchemaVersion,
		GeneratedAt:        deps.now().UTC(),
		TargetRepository:   *target,
		TargetVisibility:   "public",
		DefaultBranch:      "main",
		Bootstrap:          gitState.Bootstrap,
		Candidate:          gitState.Candidate,
		HistoryCommitCount: gitState.HistoryCommitCount,
		DeltaPaths:         gitState.DeltaPaths,
		Local: localEvidence{
			Clean:                true,
			RemoteCount:          0,
			TagCount:             0,
			HistoryEmailCount:    len(gitState.HistoryEmails),
			HistorySecretScan:    "passed",
			WorktreeSecretScan:   "passed",
			ObjectIntegrityCheck: "passed",
			ExpectedDeltaMatched: true,
			BootstrapIsAncestor:  true,
			LinearCandidateRange: true,
		},
		GitHub:        githubState,
		OwnerApproval: approval,
		Authority: authorityEvidence{
			AgentPushPermitted: false,
			RequiredActor:      "human",
		},
	}
	if err := writeReceipt(repo.Root, *out, result); err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"ok":               true,
		"action":           "publication-handoff",
		"candidate_commit": result.Candidate.Commit,
		"candidate_tree":   result.Candidate.Tree,
		"receipt":          filepath.Clean(*out),
	})
}

func loadExpectedPaths(repo gitx.Repo, name string) ([]string, error) {
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo.Root, path)
	}
	body, err := safeio.ReadRegular(path, maximumPathsBytes)
	if err != nil {
		return nil, fmt.Errorf("read expected paths: %w", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("expected-path file is empty")
	}
	normalized, err := repo.NormalizePaths(paths)
	if err != nil {
		return nil, fmt.Errorf("expected paths: %w", err)
	}
	if !equalStrings(paths, normalized) {
		return nil, errors.New("expected paths must be unique and sorted canonical repository paths")
	}
	return normalized, nil
}

func verifyModuleTarget(root, target string) error {
	body, err := safeio.ReadRegular(filepath.Join(root, "go.mod"), maximumModuleBytes)
	if err != nil {
		return fmt.Errorf("read go.mod: %w", err)
	}
	wanted := "module github.com/" + target
	moduleLines := 0
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "module ") {
			moduleLines++
			if strings.TrimSpace(line) != wanted {
				return fmt.Errorf("go.mod module must equal github.com/%s", target)
			}
		}
	}
	if moduleLines != 1 {
		return errors.New("go.mod must contain exactly one module declaration")
	}
	return nil
}

func collectGitEvidence(ctx context.Context, repo gitx.Repo, bootstrap string, expectedPaths []string) (gitEvidence, error) {
	var result gitEvidence
	shallow, err := repo.Text(ctx, nil, "rev-parse", "--is-shallow-repository")
	if err != nil || shallow != "false" {
		return result, errors.New("candidate history must be complete and non-shallow")
	}
	replacements, err := repo.Lines(ctx, nil, "for-each-ref", "--format=%(refname)", "refs/replace")
	if err != nil {
		return result, err
	}
	if len(replacements) != 0 {
		return result, errors.New("candidate repository has replacement refs")
	}
	if _, err := repo.Raw(ctx, nil, "fsck", "--strict", "--no-dangling"); err != nil {
		return result, fmt.Errorf("candidate object integrity check: %w", err)
	}
	status, err := repo.Raw(ctx, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return result, err
	}
	if status != "" {
		return result, errors.New("candidate checkout is not clean")
	}
	resolvedBootstrap, err := repo.Text(ctx, nil, "rev-parse", "--verify", bootstrap+"^{commit}")
	if err != nil || resolvedBootstrap != bootstrap {
		return result, errors.New("bootstrap commit does not resolve exactly")
	}
	head, err := repo.Text(ctx, nil, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	candidateCommits, immediateParent, err := collectLinearCandidateRange(ctx, repo, bootstrap, head)
	if err != nil {
		return result, err
	}
	bootstrapTree, err := repo.Text(ctx, nil, "show", "-s", "--format=%T", bootstrap)
	if err != nil {
		return result, err
	}
	candidateTree, err := repo.Text(ctx, nil, "show", "-s", "--format=%T", head)
	if err != nil {
		return result, err
	}
	changed, err := repo.NULPaths(ctx, nil, "diff", "--no-renames", "--name-only", "-z", bootstrap, head)
	if err != nil {
		return result, err
	}
	sort.Strings(changed)
	if !equalStrings(changed, expectedPaths) {
		return result, fmt.Errorf("candidate delta paths do not match the reviewed path file: got %q", changed)
	}
	remotes, err := repo.Lines(ctx, nil, "remote")
	if err != nil {
		return result, err
	}
	if len(remotes) != 0 {
		return result, fmt.Errorf("candidate has %d configured remotes", len(remotes))
	}
	tags, err := repo.Lines(ctx, nil, "for-each-ref", "--format=%(refname)", "refs/tags")
	if err != nil {
		return result, err
	}
	if len(tags) != 0 {
		return result, fmt.Errorf("candidate has %d local tags", len(tags))
	}
	countText, err := repo.Text(ctx, nil, "rev-list", "--count", "HEAD")
	if err != nil {
		return result, err
	}
	count, err := strconv.Atoi(countText)
	if err != nil || count < 2 {
		return result, fmt.Errorf("invalid history commit count %q", countText)
	}
	emailOutput, err := repo.Raw(ctx, nil, "log", "-z", "--format=%ae%x00%ce", "HEAD")
	if err != nil {
		return result, err
	}
	emails := uniqueNULValues(emailOutput)
	if len(emails) == 0 {
		return result, errors.New("candidate history has no author identity")
	}
	result.Bootstrap = objectEvidence{Commit: bootstrap, Tree: bootstrapTree}
	result.Candidate = candidateEvidence{
		Commit:          head,
		Tree:            candidateTree,
		ImmediateParent: immediateParent,
		CommitCount:     len(candidateCommits),
	}
	result.HistoryCommitCount = count
	result.DeltaPaths = append([]string(nil), changed...)
	result.HistoryEmails = emails
	return result, nil
}

func collectLinearCandidateRange(ctx context.Context, repo gitx.Repo, bootstrap, head string) ([]string, string, error) {
	commits, err := repo.Lines(ctx, nil, "rev-list", "--reverse", "--topo-order", bootstrap+".."+head)
	if err != nil {
		return nil, "", fmt.Errorf("inspect candidate range: %w", err)
	}
	if len(commits) == 0 || commits[len(commits)-1] != head {
		return nil, "", errors.New("bootstrap must be an ancestor of a non-empty candidate range")
	}
	previous := bootstrap
	immediateParent := ""
	for _, commit := range commits {
		parents, err := repo.Text(ctx, nil, "rev-list", "--parents", "-n", "1", commit)
		if err != nil {
			return nil, "", fmt.Errorf("inspect candidate commit %s: %w", commit, err)
		}
		fields := strings.Fields(parents)
		if len(fields) != 2 || fields[0] != commit {
			return nil, "", fmt.Errorf("candidate range is not linear at commit %s", commit)
		}
		if fields[1] != previous {
			return nil, "", fmt.Errorf("candidate range does not continue from the reviewed bootstrap at commit %s", commit)
		}
		immediateParent = previous
		previous = commit
	}
	return commits, immediateParent, nil
}

func loadOwnerApproval(path, expectedReference string) (ownerApproval, error) {
	var tracker requirementTracker
	body, err := safeio.ReadRegular(path, maximumTrackerBytes)
	if err != nil {
		return ownerApproval{}, fmt.Errorf("read public-beta tracker: %w", err)
	}
	if err := json.Unmarshal(body, &tracker); err != nil {
		return ownerApproval{}, fmt.Errorf("decode public-beta tracker: %w", err)
	}
	for _, requirement := range tracker.Requirements {
		if requirement.ID != "OSS-001" {
			continue
		}
		if requirement.Status != "verified" || requirement.HumanGate {
			return ownerApproval{}, errors.New("OSS-001 owner approval is not verified")
		}
		for _, evidence := range requirement.Evidence {
			if evidence.Kind == "owner-approval" && evidence.Value == expectedReference {
				return ownerApproval{Reference: evidence.Value, Name: true, ModulePath: true, CaptureOnly: true}, nil
			}
		}
		return ownerApproval{}, errors.New("OSS-001 has no matching owner-approval evidence")
	}
	return ownerApproval{}, errors.New("OSS-001 is missing")
}

func collectGitHubEvidence(ctx context.Context, directory, target string, historyEmails []string, runExternal externalRunner) (githubEvidence, error) {
	owner := strings.SplitN(target, "/", 2)[0]
	login, _, code, err := runExternal(ctx, githubCommandTimeout, directory, "gh", "api", "user", "--jq", ".login")
	if err != nil {
		return githubEvidence{}, err
	}
	if code != 0 || strings.TrimSpace(login) != owner {
		return githubEvidence{}, errors.New("authenticated GitHub account does not match the target owner")
	}
	publicEmail, _, code, err := runExternal(ctx, githubCommandTimeout, directory, "gh", "api", "users/"+owner, "--jq", ".email")
	publicEmail = strings.TrimSpace(publicEmail)
	if err != nil {
		return githubEvidence{}, err
	}
	if code != 0 || publicEmail == "" || publicEmail == "null" {
		return githubEvidence{}, errors.New("target owner's public GitHub email is unavailable")
	}
	for _, email := range historyEmails {
		if !strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(publicEmail)) {
			return githubEvidence{}, errors.New("candidate history identity does not match the owner's public GitHub profile")
		}
	}
	_, stderr, code, err := runExternal(ctx, githubCommandTimeout, directory, "gh", "api", "repos/"+target)
	if err != nil {
		return githubEvidence{}, err
	}
	if code == 0 {
		return githubEvidence{}, errors.New("target GitHub repository already exists")
	}
	if code != 1 || !strings.Contains(stderr, "HTTP 404") {
		return githubEvidence{}, fmt.Errorf("inspect target GitHub repository: %s", strings.TrimSpace(stderr))
	}
	return githubEvidence{AuthenticatedOwner: true, TargetRepositoryExists: false, PublicAuthorIdentityMatch: true}, nil
}

func runSecretScans(ctx context.Context, directory string, runExternal externalRunner) error {
	for _, args := range [][]string{
		{"git", "--redact", "--exit-code", "1", "."},
		{"dir", "--redact", "--exit-code", "1", "."},
	} {
		stdout, stderr, code, err := runExternal(ctx, secretScanTimeout, directory, "gitleaks", args...)
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("gitleaks %s failed: %s", args[0], commandFailureOutput(stdout, stderr))
		}
	}
	return nil
}

func externalCommandWithTimeout(ctx context.Context, timeout time.Duration, directory, name string, args ...string) (string, string, int, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr, code, err := externalCommand(commandContext, directory, name, args...)
	if commandContext.Err() != nil {
		return stdout, stderr, -1, fmt.Errorf("run %s: %w", name, commandContext.Err())
	}
	return stdout, stderr, code, err
}

func commandFailureOutput(stdout, stderr string) string {
	parts := make([]string, 0, 2)
	if value := strings.TrimSpace(stdout); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(stderr); value != "" {
		parts = append(parts, value)
	}
	if len(parts) == 0 {
		return "no diagnostic output"
	}
	return strings.Join(parts, "\n")
}

func externalCommand(ctx context.Context, directory, name string, args ...string) (string, string, int, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = gitx.Environment(nil)
	stdout := newBoundedOutput(maximumCommandOutputBytes)
	stderr := newBoundedOutput(maximumCommandOutputBytes)
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return stdout.String(), stderr.String(), -1, ctx.Err()
	}
	if err == nil {
		return stdout.String(), stderr.String(), 0, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return stdout.String(), stderr.String(), exitError.ExitCode(), nil
	}
	return stdout.String(), stderr.String(), -1, fmt.Errorf("run %s: %w", name, err)
}

type boundedOutput struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newBoundedOutput(maximum int) boundedOutput {
	return boundedOutput{remaining: maximum}
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	length := len(value)
	writeLength := min(output.remaining, length)
	if writeLength > 0 {
		_, _ = output.buffer.Write(value[:writeLength])
		output.remaining -= writeLength
	}
	if writeLength < length {
		output.truncated = true
	}
	return length, nil
}

func (output *boundedOutput) String() string {
	if output.truncated {
		return output.buffer.String() + "\n[output truncated]"
	}
	return output.buffer.String()
}

func writeReceipt(root, name string, value receipt) error {
	out, err := validatedReceiptOutput(root, name)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create receipt: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(out)
		}
	}()
	if _, err := file.Write(append(body, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}

func validatedReceiptOutput(root, name string) (string, error) {
	out, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve candidate checkout: %w", err)
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(out))
	if err != nil {
		return "", fmt.Errorf("resolve receipt directory: %w", err)
	}
	resolvedOut := filepath.Join(resolvedParent, filepath.Base(out))
	relative, err := filepath.Rel(resolvedRoot, resolvedOut)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("--out must resolve outside the candidate checkout")
	}
	info, err := os.Stat(resolvedParent)
	if err != nil || !info.IsDir() {
		return "", errors.New("receipt parent must be an existing directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("receipt directory must not grant group or other permissions")
	}
	return resolvedOut, nil
}

func uniqueNULValues(output string) []string {
	seen := map[string]bool{}
	var values []string
	for _, value := range strings.Split(output, "\x00") {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
}

func equalStrings(left, right []string) bool {
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
