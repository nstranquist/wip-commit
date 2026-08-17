// Command publication-correction creates a fail-closed receipt for a hosted
// candidate correction. It reads local and GitHub evidence. It does not change
// a repository, pull request, workflow run, tag, or Git ref.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	verifierCommandPath       = "scripts/publication-correction/main.go"
	verifierSchemaPath        = "docs/PUBLICATION-CORRECTION.schema.json"
	maximumManifestBytes      = 64 << 10
	maximumFirstReceiptBytes  = 2 << 20
	maximumVerifierFileBytes  = 2 << 20
	maximumCommandOutputBytes = 2 << 20
	gitCommandTimeout         = 2 * time.Minute
	githubCommandTimeout      = 30 * time.Second
	secretScanTimeout         = 5 * time.Minute
)

var (
	objectPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	targetPattern     = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	runLabelPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	runEventPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	sha256Pattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	allowedPhaseNames = map[string]bool{"pre-main": true, "finalized": true}
)

type typedError struct {
	Code string
	Err  error
}

func (err *typedError) Error() string { return err.Err.Error() }
func (err *typedError) Unwrap() error { return err.Err }

func problem(code, format string, arguments ...any) error {
	return &typedError{Code: code, Err: fmt.Errorf(format, arguments...)}
}

func wrapProblem(code string, err error, format string, arguments ...any) error {
	if err == nil {
		return nil
	}
	prefix := fmt.Sprintf(format, arguments...)
	if prefix == "" {
		return &typedError{Code: code, Err: err}
	}
	return &typedError{Code: code, Err: fmt.Errorf("%s: %w", prefix, err)}
}

func errorCode(err error) string {
	var typed *typedError
	if errors.As(err, &typed) && typed.Code != "" {
		return typed.Code
	}
	return "INTERNAL_ERROR"
}

type objectEvidence struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
}

type firstReceiptEvidence struct {
	SchemaVersion string `json:"schema_version"`
	Digest        string `json:"digest"`
	Bootstrap     string `json:"bootstrap_commit"`
	BootstrapTree string `json:"bootstrap_tree"`
	Candidate     string `json:"candidate_commit"`
	CandidateTree string `json:"candidate_tree"`
}

type verifierEvidence struct {
	Commit               string `json:"commit"`
	Tree                 string `json:"tree"`
	CommandPath          string `json:"command_path"`
	CommandDigest        string `json:"command_digest"`
	SchemaPath           string `json:"schema_path"`
	SchemaDigest         string `json:"schema_digest"`
	Clean                bool   `json:"clean"`
	CompleteHistory      bool   `json:"complete_history"`
	ReplacementRefCount  int    `json:"replacement_ref_count"`
	ObjectIntegrityCheck string `json:"object_integrity_check"`
}

type correctionCommit struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
	Parent string `json:"parent"`
}

type correctionEvidence struct {
	OldCandidate        objectEvidence     `json:"old_candidate"`
	NewCandidate        objectEvidence     `json:"new_candidate"`
	Commits             []correctionCommit `json:"commits"`
	BootstrapDelta      []string           `json:"bootstrap_delta_paths"`
	CorrectionDelta     []string           `json:"correction_delta_paths"`
	MergeFree           bool               `json:"merge_free"`
	OldIsAncestor       bool               `json:"old_is_ancestor"`
	BootstrapIsAncestor bool               `json:"bootstrap_is_ancestor"`
}

type localEvidence struct {
	Clean                bool   `json:"clean"`
	CompleteHistory      bool   `json:"complete_history"`
	ReplacementRefCount  int    `json:"replacement_ref_count"`
	ObjectIntegrityCheck string `json:"object_integrity_check"`
	HistorySecretScan    string `json:"history_secret_scan"`
	WorktreeSecretScan   string `json:"worktree_secret_scan"`
	PathManifestMatched  bool   `json:"path_manifest_matched"`
	CheckedOutCandidate  bool   `json:"checked_out_candidate"`
}

type identityEvidence struct {
	HistoryCommitCount    int    `json:"history_commit_count"`
	UniqueAuthorNames     int    `json:"unique_author_names"`
	UniqueAuthorEmails    int    `json:"unique_author_emails"`
	UniqueCommitterNames  int    `json:"unique_committer_names"`
	UniqueCommitterEmails int    `json:"unique_committer_emails"`
	IdentitySetDigest     string `json:"identity_set_digest"`
	PublicEmailMatch      bool   `json:"public_email_match"`
	PublicNameMatch       bool   `json:"public_name_match"`
}

type repositoryEvidence struct {
	FullName           string `json:"full_name"`
	Visibility         string `json:"visibility"`
	DefaultBranch      string `json:"default_branch"`
	AuthenticatedOwner bool   `json:"authenticated_owner"`
}

type remoteEvidence struct {
	CandidateRef    string `json:"candidate_ref"`
	CandidateCommit string `json:"candidate_commit"`
	MainRef         string `json:"main_ref"`
	MainCommit      string `json:"main_commit"`
}

type pullRequestEvidence struct {
	Number      int    `json:"number"`
	State       string `json:"state"`
	HeadRef     string `json:"head_ref"`
	HeadCommit  string `json:"head_commit"`
	BaseRef     string `json:"base_ref"`
	BaseCommit  string `json:"base_commit"`
	Merged      bool   `json:"merged"`
	MergeCommit string `json:"merge_commit,omitempty"`
}

type runEvidence struct {
	Label      string `json:"label"`
	ID         int64  `json:"id"`
	Event      string `json:"event"`
	HeadBranch string `json:"head_branch"`
	HeadCommit string `json:"head_commit"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Attempt    int    `json:"attempt"`
	Workflow   string `json:"workflow"`
}

type requiredCheckEvidence struct {
	Context       string `json:"context"`
	IntegrationID int64  `json:"integration_id"`
	CheckRunID    int64  `json:"check_run_id"`
	Status        string `json:"status"`
	Conclusion    string `json:"conclusion"`
}

type rulesetEvidence struct {
	ID                           int64    `json:"id"`
	Name                         string   `json:"name"`
	Enforcement                  string   `json:"enforcement"`
	NoBypassActors               bool     `json:"no_bypass_actors"`
	DeletionBlocked              bool     `json:"deletion_blocked"`
	NonFastForwardBlocked        bool     `json:"non_fast_forward_blocked"`
	LinearHistoryRequired        bool     `json:"linear_history_required"`
	PullRequestRequired          bool     `json:"pull_request_required"`
	RequiredApprovingReviewCount int      `json:"required_approving_review_count"`
	DismissStaleReviews          bool     `json:"dismiss_stale_reviews"`
	LastPushApprovalRequired     bool     `json:"last_push_approval_required"`
	ReviewThreadsResolved        bool     `json:"review_threads_resolved"`
	AllowedMergeMethods          []string `json:"allowed_merge_methods"`
	StrictRequiredChecks         bool     `json:"strict_required_checks"`
	RequiredChecksOnCreate       bool     `json:"required_checks_on_create"`
}

type receipt struct {
	SchemaVersion  string                  `json:"schema_version"`
	GeneratedAt    time.Time               `json:"generated_at"`
	Target         string                  `json:"target_repository"`
	Phase          string                  `json:"phase"`
	Verifier       verifierEvidence        `json:"verifier"`
	Bootstrap      objectEvidence          `json:"bootstrap"`
	FirstReceipt   firstReceiptEvidence    `json:"first_receipt"`
	Correction     correctionEvidence      `json:"correction"`
	Local          localEvidence           `json:"local"`
	Identity       identityEvidence        `json:"identity"`
	Repository     repositoryEvidence      `json:"repository"`
	Remote         remoteEvidence          `json:"remote"`
	PullRequest    pullRequestEvidence     `json:"pull_request"`
	HostedRuns     []runEvidence           `json:"hosted_runs"`
	Ruleset        rulesetEvidence         `json:"ruleset"`
	RequiredChecks []requiredCheckEvidence `json:"required_checks"`
}

type identitySet struct {
	HistoryCommitCount int
	AuthorNames        []string
	AuthorEmails       []string
	CommitterNames     []string
	CommitterEmails    []string
	Digest             string
}

type gitEvidence struct {
	Bootstrap  objectEvidence
	Correction correctionEvidence
	Identity   identitySet
}

type firstReceiptDocument struct {
	SchemaVersion    string         `json:"schema_version"`
	TargetRepository string         `json:"target_repository"`
	TargetVisibility string         `json:"target_visibility"`
	DefaultBranch    string         `json:"default_branch"`
	Bootstrap        objectEvidence `json:"bootstrap"`
	Candidate        struct {
		Commit string `json:"commit"`
		Tree   string `json:"tree"`
	} `json:"candidate"`
	HistoryCommitCount int      `json:"history_commit_count"`
	DeltaPaths         []string `json:"delta_paths"`
	Local              struct {
		Clean                bool   `json:"clean"`
		HistorySecretScan    string `json:"history_secret_scan"`
		WorktreeSecretScan   string `json:"worktree_secret_scan"`
		ObjectIntegrityCheck string `json:"object_integrity_check"`
		ExpectedDeltaMatched bool   `json:"expected_delta_matched"`
		BootstrapIsAncestor  bool   `json:"bootstrap_is_ancestor"`
		LinearCandidateRange bool   `json:"linear_candidate_range"`
	} `json:"local"`
	GitHub struct {
		AuthenticatedOwner        bool `json:"authenticated_owner"`
		TargetRepositoryExists    bool `json:"target_repository_exists"`
		PublicAuthorIdentityMatch bool `json:"public_author_identity_match"`
	} `json:"github"`
}

type runSpec struct {
	Label, Event, Branch string
	ID                   int64
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type externalRunner func(context.Context, time.Duration, string, string, ...string) (string, string, int, error)

type dependencies struct {
	external externalRunner
	now      func() time.Time
}

type options struct {
	RepoDir, VerifierDir, Target, Phase, Bootstrap, OldCandidate, Candidate, CandidateRef string
	FirstReceipt, PathsFile, Out                                                          string
	PullRequest                                                                           int
	Ruleset                                                                               int64
	Runs                                                                                  []runSpec
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "publication-correction: %s: %s\n", errorCode(err), err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	return runWithDependencies(ctx, args, stdout, dependencies{external: externalCommandWithTimeout, now: time.Now})
}

func runWithDependencies(ctx context.Context, args []string, stdout io.Writer, deps dependencies) error {
	if deps.external == nil || deps.now == nil {
		return problem("DEPENDENCIES_INCOMPLETE", "publication-correction dependencies are incomplete")
	}
	configuration, err := parseOptions(args)
	if err != nil {
		return err
	}
	repo, err := gitx.Discover(ctx, configuration.RepoDir)
	if err != nil {
		return wrapProblem("NOT_A_REPOSITORY", err, "discover candidate checkout")
	}
	if err := verifyModuleTarget(repo.Root, configuration.Target); err != nil {
		return err
	}
	verifierRepo, err := gitx.Discover(ctx, configuration.VerifierDir)
	if err != nil {
		return wrapProblem("VERIFIER_INVALID", err, "discover verifier checkout")
	}
	if err := verifyModuleTarget(verifierRepo.Root, configuration.Target); err != nil {
		return problem("VERIFIER_INVALID", "verifier go.mod does not match github.com/%s", configuration.Target)
	}
	verifier, err := collectVerifierEvidence(ctx, verifierRepo)
	if err != nil {
		return err
	}
	if err := validateBranchName(ctx, repo, configuration.CandidateRef); err != nil {
		return err
	}
	roots := []string{repo.Root, verifierRepo.Root}
	outputPath, err := validatedReceiptOutput(roots, configuration.Out)
	if err != nil {
		return err
	}
	manifest, err := loadExpectedPaths(repo, configuration.PathsFile)
	if err != nil {
		return err
	}
	firstReceipt, err := loadFirstReceipt(roots, configuration.FirstReceipt, configuration.Target, configuration.Bootstrap, configuration.OldCandidate)
	if err != nil {
		return err
	}
	gitState, err := collectGitEvidence(ctx, repo, configuration.Bootstrap, configuration.OldCandidate, configuration.Candidate, manifest)
	if err != nil {
		return err
	}
	if firstReceipt.BootstrapTree != gitState.Bootstrap.Tree || firstReceipt.CandidateTree != gitState.Correction.OldCandidate.Tree {
		return problem("FIRST_RECEIPT_MISMATCH", "first receipt trees do not match the local bootstrap and old candidate")
	}
	if err := runSecretScans(ctx, repo.Root, deps.external); err != nil {
		return err
	}
	hosted, err := collectGitHubEvidence(ctx, repo.Root, configuration, gitState.Identity, deps.external)
	if err != nil {
		return err
	}

	result := receipt{
		SchemaVersion: receiptSchemaVersion,
		GeneratedAt:   deps.now().UTC(),
		Target:        configuration.Target,
		Phase:         configuration.Phase,
		Verifier:      verifier,
		Bootstrap:     gitState.Bootstrap,
		FirstReceipt:  firstReceipt,
		Correction:    gitState.Correction,
		Local: localEvidence{
			Clean:                true,
			CompleteHistory:      true,
			ReplacementRefCount:  0,
			ObjectIntegrityCheck: "passed",
			HistorySecretScan:    "passed",
			WorktreeSecretScan:   "passed",
			PathManifestMatched:  true,
			CheckedOutCandidate:  true,
		},
		Identity: identityEvidence{
			HistoryCommitCount:    gitState.Identity.HistoryCommitCount,
			UniqueAuthorNames:     len(gitState.Identity.AuthorNames),
			UniqueAuthorEmails:    len(gitState.Identity.AuthorEmails),
			UniqueCommitterNames:  len(gitState.Identity.CommitterNames),
			UniqueCommitterEmails: len(gitState.Identity.CommitterEmails),
			IdentitySetDigest:     gitState.Identity.Digest,
			PublicEmailMatch:      true,
			PublicNameMatch:       hosted.PublicNameMatch,
		},
		Repository:     hosted.Repository,
		Remote:         hosted.Remote,
		PullRequest:    hosted.PullRequest,
		HostedRuns:     hosted.Runs,
		Ruleset:        hosted.Ruleset,
		RequiredChecks: hosted.RequiredChecks,
	}
	body, digest, err := encodeReceipt(result)
	if err != nil {
		return err
	}
	if err := writeReceipt(outputPath, body); err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"ok":               true,
		"action":           "publication-correction",
		"candidate_commit": result.Correction.NewCandidate.Commit,
		"candidate_tree":   result.Correction.NewCandidate.Tree,
		"phase":            result.Phase,
		"receipt_digest":   digest,
	})
}

func parseOptions(args []string) (options, error) {
	var configuration options
	var rawRuns stringList
	flags := flag.NewFlagSet("publication-correction", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&configuration.RepoDir, "repo-dir", ".", "exact candidate checkout")
	flags.StringVar(&configuration.VerifierDir, "verifier-dir", ".", "clean checkout that contains this verifier")
	flags.StringVar(&configuration.Target, "target", "", "GitHub OWNER/REPO")
	flags.StringVar(&configuration.Phase, "phase", "pre-main", "pre-main or finalized")
	flags.StringVar(&configuration.Bootstrap, "bootstrap", "", "reviewed bootstrap commit")
	flags.StringVar(&configuration.OldCandidate, "old-candidate", "", "candidate bound by the first receipt")
	flags.StringVar(&configuration.Candidate, "candidate", "", "corrected candidate commit")
	flags.StringVar(&configuration.CandidateRef, "candidate-ref", "", "remote candidate branch without refs/heads/")
	flags.StringVar(&configuration.FirstReceipt, "first-receipt", "", "immutable pre-first-push receipt")
	flags.StringVar(&configuration.PathsFile, "paths-file", "", "reviewed bootstrap-delta path manifest")
	flags.StringVar(&configuration.Out, "out", "", "new private receipt outside the checkout")
	flags.IntVar(&configuration.PullRequest, "pull-request", 0, "verification pull request number")
	flags.Int64Var(&configuration.Ruleset, "ruleset", 0, "required main-branch ruleset ID")
	flags.Var(&rawRuns, "run", "LABEL:EVENT:BRANCH:ID; repeat for each required hosted run")
	if err := flags.Parse(args); err != nil {
		return configuration, problem("INVALID_ARGS", "%s", err)
	}
	if flags.NArg() != 0 {
		return configuration, problem("INVALID_ARGS", "unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if !targetPattern.MatchString(configuration.Target) || strings.Contains(configuration.Target, "..") {
		return configuration, problem("INVALID_ARGS", "--target must use OWNER/REPO")
	}
	if !allowedPhaseNames[configuration.Phase] {
		return configuration, problem("INVALID_ARGS", "--phase must be pre-main or finalized")
	}
	for label, value := range map[string]string{
		"--bootstrap":     configuration.Bootstrap,
		"--old-candidate": configuration.OldCandidate,
		"--candidate":     configuration.Candidate,
	} {
		if !objectPattern.MatchString(value) {
			return configuration, problem("INVALID_ARGS", "%s must be a complete Git object ID", label)
		}
	}
	if configuration.Bootstrap == configuration.OldCandidate || configuration.OldCandidate == configuration.Candidate {
		return configuration, problem("INVALID_ARGS", "bootstrap, old candidate, and candidate must identify distinct commits")
	}
	if configuration.CandidateRef == "" || strings.HasPrefix(configuration.CandidateRef, "refs/") {
		return configuration, problem("INVALID_ARGS", "--candidate-ref must be a branch name without refs/heads/")
	}
	if configuration.PullRequest <= 0 || configuration.Ruleset <= 0 {
		return configuration, problem("INVALID_ARGS", "--pull-request and --ruleset must be positive")
	}
	for label, value := range map[string]string{
		"--first-receipt": configuration.FirstReceipt,
		"--paths-file":    configuration.PathsFile,
		"--out":           configuration.Out,
	} {
		if strings.TrimSpace(value) == "" {
			return configuration, problem("INVALID_ARGS", "%s is required", label)
		}
	}
	runs, err := parseRunSpecs(rawRuns)
	if err != nil {
		return configuration, err
	}
	configuration.Runs = runs
	return configuration, nil
}

func parseRunSpecs(values []string) ([]runSpec, error) {
	if len(values) == 0 {
		return nil, problem("INVALID_ARGS", "at least one --run is required")
	}
	seenLabels, seenIDs := map[string]bool{}, map[int64]bool{}
	runs := make([]runSpec, 0, len(values))
	for _, value := range values {
		parts := strings.Split(value, ":")
		if len(parts) != 4 || !runLabelPattern.MatchString(parts[0]) || !runEventPattern.MatchString(parts[1]) || strings.TrimSpace(parts[2]) == "" {
			return nil, problem("INVALID_ARGS", "--run must use LABEL:EVENT:BRANCH:ID")
		}
		id, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil || id <= 0 {
			return nil, problem("INVALID_ARGS", "--run ID must be a positive integer")
		}
		if seenLabels[parts[0]] || seenIDs[id] {
			return nil, problem("INVALID_ARGS", "--run labels and IDs must be unique")
		}
		seenLabels[parts[0]], seenIDs[id] = true, true
		runs = append(runs, runSpec{Label: parts[0], Event: parts[1], Branch: parts[2], ID: id})
	}
	sort.Slice(runs, func(left, right int) bool { return runs[left].Label < runs[right].Label })
	return runs, nil
}

func verifyModuleTarget(root, target string) error {
	body, err := safeio.ReadRegular(filepath.Join(root, "go.mod"), maximumManifestBytes)
	if err != nil {
		return wrapProblem("TARGET_MISMATCH", err, "read go.mod")
	}
	wanted, count := "module github.com/"+target, 0
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "module ") {
			count++
			if strings.TrimSpace(line) != wanted {
				return problem("TARGET_MISMATCH", "go.mod module must equal github.com/%s", target)
			}
		}
	}
	if count != 1 {
		return problem("TARGET_MISMATCH", "go.mod must contain exactly one module declaration")
	}
	return nil
}

func validateBranchName(parent context.Context, repo gitx.Repo, branch string) error {
	ctx, cancel := context.WithTimeout(parent, gitCommandTimeout)
	defer cancel()
	resolved, err := repo.Text(ctx, nil, "check-ref-format", "--branch", branch)
	if err != nil || resolved != branch {
		return gitProblem(ctx, "INVALID_ARGS", err, "--candidate-ref is not a valid Git branch")
	}
	return nil
}

func loadExpectedPaths(repo gitx.Repo, name string) ([]string, error) {
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo.Root, path)
	}
	inside, err := pathWithinRoot(repo.Root, path)
	if err != nil || !inside {
		return nil, problem("PATH_MANIFEST_UNSAFE", "--paths-file must resolve inside the candidate checkout")
	}
	body, err := safeio.ReadRegular(path, maximumManifestBytes)
	if err != nil {
		return nil, wrapProblem("PATH_MANIFEST_INVALID", err, "read path manifest")
	}
	var paths []string
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	if len(paths) == 0 {
		return nil, problem("PATH_MANIFEST_INVALID", "path manifest is empty")
	}
	normalized, err := repo.NormalizePaths(paths)
	if err != nil || !equalStrings(paths, normalized) {
		return nil, problem("PATH_MANIFEST_INVALID", "path manifest must contain unique sorted canonical repository paths")
	}
	return normalized, nil
}

func loadFirstReceipt(roots []string, name, target, bootstrap, oldCandidate string) (firstReceiptEvidence, error) {
	path, err := filepath.Abs(name)
	if err != nil {
		return firstReceiptEvidence{}, wrapProblem("FIRST_RECEIPT_INVALID", err, "resolve first receipt")
	}
	for _, root := range roots {
		inside, containmentErr := pathWithinRoot(root, path)
		if containmentErr != nil || inside {
			return firstReceiptEvidence{}, problem("FIRST_RECEIPT_INVALID", "--first-receipt must resolve outside the candidate and verifier checkouts")
		}
	}
	body, err := safeio.ReadRegular(path, maximumFirstReceiptBytes)
	if err != nil {
		return firstReceiptEvidence{}, wrapProblem("FIRST_RECEIPT_INVALID", err, "read first receipt")
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm()&0o077 != 0 {
			return firstReceiptEvidence{}, problem("FIRST_RECEIPT_INVALID", "first receipt must not grant group or other permissions")
		}
	}
	var document firstReceiptDocument
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&document); err != nil {
		return firstReceiptEvidence{}, wrapProblem("FIRST_RECEIPT_INVALID", err, "decode first receipt")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return firstReceiptEvidence{}, wrapProblem("FIRST_RECEIPT_INVALID", err, "decode first receipt")
	}
	if document.SchemaVersion != "1.0.0" || document.TargetRepository != target || document.TargetVisibility != "public" || document.DefaultBranch != "main" || document.Bootstrap.Commit != bootstrap || document.Candidate.Commit != oldCandidate {
		return firstReceiptEvidence{}, problem("FIRST_RECEIPT_MISMATCH", "first receipt does not bind the target, bootstrap, and old candidate")
	}
	if document.HistoryCommitCount < 2 || len(document.DeltaPaths) == 0 || !document.Local.Clean || document.Local.HistorySecretScan != "passed" || document.Local.WorktreeSecretScan != "passed" || document.Local.ObjectIntegrityCheck != "passed" || !document.Local.ExpectedDeltaMatched || !document.Local.BootstrapIsAncestor || !document.Local.LinearCandidateRange || !document.GitHub.AuthenticatedOwner || document.GitHub.TargetRepositoryExists || !document.GitHub.PublicAuthorIdentityMatch {
		return firstReceiptEvidence{}, problem("FIRST_RECEIPT_INVALID", "first receipt does not contain the required successful pre-first-push evidence")
	}
	if !objectPattern.MatchString(document.Bootstrap.Tree) || !objectPattern.MatchString(document.Candidate.Tree) {
		return firstReceiptEvidence{}, problem("FIRST_RECEIPT_INVALID", "first receipt contains an invalid object ID")
	}
	digest := sha256.Sum256(body)
	return firstReceiptEvidence{
		SchemaVersion: document.SchemaVersion,
		Digest:        "sha256:" + hex.EncodeToString(digest[:]),
		Bootstrap:     bootstrap,
		BootstrapTree: document.Bootstrap.Tree,
		Candidate:     oldCandidate,
		CandidateTree: document.Candidate.Tree,
	}, nil
}

func collectVerifierEvidence(parent context.Context, repo gitx.Repo) (verifierEvidence, error) {
	ctx, cancel := context.WithTimeout(parent, gitCommandTimeout)
	defer cancel()
	var result verifierEvidence
	shallow, err := repo.Text(ctx, nil, "rev-parse", "--is-shallow-repository")
	if err != nil || shallow != "false" {
		return result, verifierProblem(ctx, err, "verifier history must be complete and non-shallow")
	}
	replacements, err := repo.Lines(ctx, nil, "for-each-ref", "--format=%(refname)", "refs/replace")
	if err != nil {
		return result, verifierProblem(ctx, err, "inspect verifier replacement refs")
	}
	if len(replacements) != 0 {
		return result, problem("VERIFIER_INVALID", "verifier repository has replacement refs")
	}
	if _, err := repo.Raw(ctx, nil, "fsck", "--strict", "--no-dangling"); err != nil {
		return result, verifierProblem(ctx, err, "verify verifier object integrity")
	}
	status, err := repo.Raw(ctx, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return result, verifierProblem(ctx, err, "inspect verifier checkout")
	}
	if status != "" {
		return result, problem("VERIFIER_INVALID", "verifier checkout is not clean")
	}
	commit, err := repo.Text(ctx, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !objectPattern.MatchString(commit) {
		return result, verifierProblem(ctx, err, "resolve verifier commit")
	}
	tree, err := objectTree(ctx, repo, commit)
	if err != nil {
		return result, problem("VERIFIER_INVALID", "resolve verifier tree: %v", err)
	}
	commandDigest, err := trackedBlobDigest(ctx, repo, commit, verifierCommandPath)
	if err != nil {
		return result, err
	}
	schemaDigest, err := trackedBlobDigest(ctx, repo, commit, verifierSchemaPath)
	if err != nil {
		return result, err
	}
	return verifierEvidence{
		Commit:               commit,
		Tree:                 tree,
		CommandPath:          verifierCommandPath,
		CommandDigest:        commandDigest,
		SchemaPath:           verifierSchemaPath,
		SchemaDigest:         schemaDigest,
		Clean:                true,
		CompleteHistory:      true,
		ReplacementRefCount:  0,
		ObjectIntegrityCheck: "passed",
	}, nil
}

func trackedBlobDigest(ctx context.Context, repo gitx.Repo, commit, path string) (string, error) {
	object := commit + ":" + path
	typeName, err := repo.Text(ctx, nil, "cat-file", "-t", object)
	if err != nil || typeName != "blob" {
		return "", verifierProblem(ctx, err, "verifier artifact %s is not a tracked file", path)
	}
	sizeText, err := repo.Text(ctx, nil, "cat-file", "-s", object)
	if err != nil {
		return "", verifierProblem(ctx, err, "inspect verifier artifact %s", path)
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size < 1 || size > maximumVerifierFileBytes {
		return "", problem("VERIFIER_INVALID", "verifier artifact %s has an invalid size", path)
	}
	body, err := repo.Raw(ctx, nil, "cat-file", "blob", object)
	if err != nil || int64(len(body)) != size {
		return "", verifierProblem(ctx, err, "read verifier artifact %s", path)
	}
	digest := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func verifierProblem(ctx context.Context, err error, format string, arguments ...any) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return problem("COMMAND_TIMEOUT", "verifier checks exceeded %s", gitCommandTimeout)
	}
	if err == nil {
		return problem("VERIFIER_INVALID", format, arguments...)
	}
	return wrapProblem("VERIFIER_INVALID", err, format, arguments...)
}

func collectGitEvidence(parent context.Context, repo gitx.Repo, bootstrap, oldCandidate, candidate string, expectedPaths []string) (gitEvidence, error) {
	ctx, cancel := context.WithTimeout(parent, gitCommandTimeout)
	defer cancel()
	var result gitEvidence
	if _, err := repo.Text(ctx, nil, "check-ref-format", "--branch", "main"); err != nil {
		return result, gitProblem(ctx, "GIT_CHECK_FAILED", err, "validate Git command execution")
	}
	shallow, err := repo.Text(ctx, nil, "rev-parse", "--is-shallow-repository")
	if err != nil || shallow != "false" {
		return result, gitProblem(ctx, "GIT_CHECK_FAILED", err, "candidate history must be complete and non-shallow")
	}
	replacements, err := repo.Lines(ctx, nil, "for-each-ref", "--format=%(refname)", "refs/replace")
	if err != nil {
		return result, gitProblem(ctx, "GIT_CHECK_FAILED", err, "inspect replacement refs")
	}
	if len(replacements) != 0 {
		return result, problem("GIT_CHECK_FAILED", "candidate repository has replacement refs")
	}
	if _, err := repo.Raw(ctx, nil, "fsck", "--strict", "--no-dangling"); err != nil {
		return result, gitProblem(ctx, "OBJECT_CHECK_FAILED", err, "candidate object integrity check")
	}
	status, err := repo.Raw(ctx, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return result, gitProblem(ctx, "GIT_CHECK_FAILED", err, "inspect candidate checkout")
	}
	if status != "" {
		return result, problem("CHECKOUT_NOT_CLEAN", "candidate checkout is not clean")
	}
	for label, object := range map[string]string{"bootstrap": bootstrap, "old candidate": oldCandidate, "candidate": candidate} {
		resolved, resolveErr := repo.Text(ctx, nil, "rev-parse", "--verify", object+"^{commit}")
		if resolveErr != nil || resolved != object {
			return result, gitProblem(ctx, "CANDIDATE_MISMATCH", resolveErr, "%s does not resolve exactly", label)
		}
	}
	head, err := repo.Text(ctx, nil, "rev-parse", "HEAD")
	if err != nil || head != candidate {
		return result, gitProblem(ctx, "CANDIDATE_MISMATCH", err, "checked-out commit does not equal --candidate")
	}
	ancestorCode, err := repo.Exit(ctx, nil, "merge-base", "--is-ancestor", bootstrap, oldCandidate)
	if err != nil || ancestorCode != 0 {
		return result, gitProblem(ctx, "CORRECTION_NOT_LINEAR", err, "bootstrap is not an ancestor of the old candidate")
	}
	commits, err := collectLinearCorrectionRange(ctx, repo, oldCandidate, candidate)
	if err != nil {
		return result, err
	}
	bootstrapTree, err := objectTree(ctx, repo, bootstrap)
	if err != nil {
		return result, err
	}
	oldTree, err := objectTree(ctx, repo, oldCandidate)
	if err != nil {
		return result, err
	}
	candidateTree, err := objectTree(ctx, repo, candidate)
	if err != nil {
		return result, err
	}
	bootstrapDelta, err := changedPaths(ctx, repo, bootstrap, candidate)
	if err != nil {
		return result, err
	}
	if !equalStrings(bootstrapDelta, expectedPaths) {
		return result, problem("PATH_MANIFEST_MISMATCH", "bootstrap delta does not match the reviewed path manifest")
	}
	correctionDelta, err := changedPaths(ctx, repo, oldCandidate, candidate)
	if err != nil {
		return result, err
	}
	if len(correctionDelta) == 0 {
		return result, problem("CORRECTION_NOT_LINEAR", "correction range does not change a reviewed path")
	}
	identities, err := collectIdentities(ctx, repo, candidate)
	if err != nil {
		return result, err
	}
	result.Bootstrap = objectEvidence{Commit: bootstrap, Tree: bootstrapTree}
	result.Correction = correctionEvidence{
		OldCandidate:        objectEvidence{Commit: oldCandidate, Tree: oldTree},
		NewCandidate:        objectEvidence{Commit: candidate, Tree: candidateTree},
		Commits:             commits,
		BootstrapDelta:      bootstrapDelta,
		CorrectionDelta:     correctionDelta,
		MergeFree:           true,
		OldIsAncestor:       true,
		BootstrapIsAncestor: true,
	}
	result.Identity = identities
	return result, nil
}

func collectLinearCorrectionRange(ctx context.Context, repo gitx.Repo, oldCandidate, candidate string) ([]correctionCommit, error) {
	commits, err := repo.Lines(ctx, nil, "rev-list", "--reverse", "--topo-order", oldCandidate+".."+candidate)
	if err != nil {
		return nil, gitProblem(ctx, "CORRECTION_NOT_LINEAR", err, "inspect correction range")
	}
	if len(commits) == 0 || commits[len(commits)-1] != candidate {
		return nil, problem("CORRECTION_NOT_LINEAR", "old candidate must be an ancestor of a non-empty correction range")
	}
	previous := oldCandidate
	result := make([]correctionCommit, 0, len(commits))
	for _, commit := range commits {
		parents, parentErr := repo.Text(ctx, nil, "rev-list", "--parents", "-n", "1", commit)
		if parentErr != nil {
			return nil, gitProblem(ctx, "CORRECTION_NOT_LINEAR", parentErr, "inspect correction commit")
		}
		fields := strings.Fields(parents)
		if len(fields) != 2 || fields[0] != commit || fields[1] != previous {
			return nil, problem("CORRECTION_NOT_LINEAR", "correction range is not a linear first-parent chain")
		}
		tree, treeErr := objectTree(ctx, repo, commit)
		if treeErr != nil {
			return nil, treeErr
		}
		result = append(result, correctionCommit{Commit: commit, Tree: tree, Parent: previous})
		previous = commit
	}
	return result, nil
}

func objectTree(ctx context.Context, repo gitx.Repo, object string) (string, error) {
	tree, err := repo.Text(ctx, nil, "show", "-s", "--format=%T", object)
	if err != nil || !objectPattern.MatchString(tree) {
		return "", gitProblem(ctx, "OBJECT_CHECK_FAILED", err, "resolve commit tree")
	}
	return tree, nil
}

func changedPaths(ctx context.Context, repo gitx.Repo, old, current string) ([]string, error) {
	paths, err := repo.NULPaths(ctx, nil, "diff", "--no-renames", "--name-only", "-z", old, current)
	if err != nil {
		return nil, gitProblem(ctx, "GIT_CHECK_FAILED", err, "inspect changed paths")
	}
	sort.Strings(paths)
	return paths, nil
}

func collectIdentities(ctx context.Context, repo gitx.Repo, candidate string) (identitySet, error) {
	countText, err := repo.Text(ctx, nil, "rev-list", "--count", candidate)
	if err != nil {
		return identitySet{}, gitProblem(ctx, "IDENTITY_MISMATCH", err, "count history identities")
	}
	count, err := strconv.Atoi(countText)
	if err != nil || count < 1 {
		return identitySet{}, problem("IDENTITY_MISMATCH", "history commit count is invalid")
	}
	output, err := repo.Raw(ctx, nil, "log", "-z", "--format=%H%x00%an%x00%ae%x00%cn%x00%ce%x00", candidate)
	if err != nil {
		return identitySet{}, gitProblem(ctx, "IDENTITY_MISMATCH", err, "read history identities")
	}
	values := nonemptyNULValues(output)
	if len(values) != count*5 {
		return identitySet{}, problem("IDENTITY_MISMATCH", "history identity stream is incomplete")
	}
	authorNames, authorEmails, committerNames, committerEmails := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for index := 0; index < len(values); index += 5 {
		if !objectPattern.MatchString(values[index]) {
			return identitySet{}, problem("IDENTITY_MISMATCH", "history identity stream contains an invalid commit")
		}
		authorNames[values[index+1]], authorEmails[values[index+2]] = true, true
		committerNames[values[index+3]], committerEmails[values[index+4]] = true, true
	}
	result := identitySet{
		HistoryCommitCount: count,
		AuthorNames:        sortedKeys(authorNames),
		AuthorEmails:       sortedKeys(authorEmails),
		CommitterNames:     sortedKeys(committerNames),
		CommitterEmails:    sortedKeys(committerEmails),
	}
	hash := sha256.New()
	for _, group := range []struct {
		label  string
		values []string
	}{
		{label: "author-email", values: result.AuthorEmails},
		{label: "author-name", values: result.AuthorNames},
		{label: "committer-email", values: result.CommitterEmails},
		{label: "committer-name", values: result.CommitterNames},
	} {
		for _, value := range group.values {
			_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", group.label, value)
		}
	}
	result.Digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return result, nil
}

func gitProblem(ctx context.Context, code string, err error, format string, arguments ...any) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return problem("COMMAND_TIMEOUT", "Git checks exceeded %s", gitCommandTimeout)
	}
	message := fmt.Sprintf(format, arguments...)
	if err == nil {
		return problem(code, "%s", message)
	}
	return wrapProblem(code, err, "%s", message)
}

type hostedEvidence struct {
	Repository      repositoryEvidence
	Remote          remoteEvidence
	PullRequest     pullRequestEvidence
	Runs            []runEvidence
	Ruleset         rulesetEvidence
	RequiredChecks  []requiredCheckEvidence
	PublicNameMatch bool
}

type githubUser struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type githubViewer struct {
	Data struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	} `json:"data"`
}

type githubRepository struct {
	FullName      string `json:"full_name"`
	Visibility    string `json:"visibility"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type githubRef struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

type githubPullRequest struct {
	Number      int        `json:"number"`
	State       string     `json:"state"`
	Merged      bool       `json:"merged"`
	MergedAt    *time.Time `json:"merged_at"`
	MergeCommit string     `json:"merge_commit_sha"`
	Head        struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
}

type githubRun struct {
	ID         int64  `json:"id"`
	Event      string `json:"event"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Attempt    int    `json:"run_attempt"`
	Name       string `json:"name"`
}

type githubRuleset struct {
	ID           int64            `json:"id"`
	Name         string           `json:"name"`
	Target       string           `json:"target"`
	Enforcement  string           `json:"enforcement"`
	BypassActors []map[string]any `json:"bypass_actors"`
	Conditions   struct {
		RefName struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"ref_name"`
	} `json:"conditions"`
	Rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredApprovingReviewCount  int      `json:"required_approving_review_count"`
			DismissStaleReviews           bool     `json:"dismiss_stale_reviews_on_push"`
			RequireLastPushApproval       bool     `json:"require_last_push_approval"`
			RequireReviewThreadResolution bool     `json:"required_review_thread_resolution"`
			AllowedMergeMethods           []string `json:"allowed_merge_methods"`
			StrictRequiredChecks          bool     `json:"strict_required_status_checks_policy"`
			DoNotEnforceOnCreate          bool     `json:"do_not_enforce_on_create"`
			RequiredChecks                []struct {
				Context       string `json:"context"`
				IntegrationID int64  `json:"integration_id"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	} `json:"rules"`
}

type githubJobs struct {
	Jobs []struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"jobs"`
}

type githubCheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	App        struct {
		ID int64 `json:"id"`
	} `json:"app"`
}

func collectGitHubEvidence(ctx context.Context, directory string, configuration options, identities identitySet, runExternal externalRunner) (hostedEvidence, error) {
	var result hostedEvidence
	owner := strings.SplitN(configuration.Target, "/", 2)[0]
	authenticated, err := githubAuthenticatedLogin(ctx, directory, runExternal)
	if err != nil {
		return result, err
	}
	if authenticated != owner {
		return result, problem("TARGET_MISMATCH", "authenticated GitHub account does not match the target owner")
	}
	var publicOwner githubUser
	if err := githubAPI(ctx, directory, runExternal, "users/"+owner, &publicOwner); err != nil {
		return result, err
	}
	if publicOwner.Login != owner || strings.TrimSpace(publicOwner.Email) == "" {
		return result, problem("IDENTITY_MISMATCH", "target owner public email is unavailable")
	}
	if !allIdentityValuesMatch(identities.AuthorEmails, publicOwner.Email) || !allIdentityValuesMatch(identities.CommitterEmails, publicOwner.Email) {
		return result, problem("IDENTITY_MISMATCH", "history author or committer email does not match the public owner profile")
	}
	result.PublicNameMatch = allIdentityValuesMatch(identities.AuthorNames, publicOwner.Name) && allIdentityValuesMatch(identities.CommitterNames, publicOwner.Name)
	var repository githubRepository
	if err := githubAPI(ctx, directory, runExternal, "repos/"+configuration.Target, &repository); err != nil {
		return result, err
	}
	if repository.FullName != configuration.Target || repository.Owner.Login != owner || repository.Private || repository.Visibility != "public" || repository.DefaultBranch != "main" {
		return result, problem("TARGET_MISMATCH", "target repository identity, visibility, owner, or default branch differs")
	}
	var candidateRef githubRef
	if err := githubAPI(ctx, directory, runExternal, "repos/"+configuration.Target+"/git/ref/heads/"+configuration.CandidateRef, &candidateRef); err != nil {
		return result, err
	}
	if candidateRef.Ref != "refs/heads/"+configuration.CandidateRef || candidateRef.Object.Type != "commit" || candidateRef.Object.SHA != configuration.Candidate {
		return result, problem("REMOTE_REF_MISMATCH", "remote candidate ref does not equal the corrected candidate")
	}
	var mainRef githubRef
	if err := githubAPI(ctx, directory, runExternal, "repos/"+configuration.Target+"/git/ref/heads/main", &mainRef); err != nil {
		return result, err
	}
	expectedMain := configuration.Bootstrap
	if configuration.Phase == "finalized" {
		expectedMain = configuration.Candidate
	}
	if mainRef.Ref != "refs/heads/main" || mainRef.Object.Type != "commit" || mainRef.Object.SHA != expectedMain {
		return result, problem("REMOTE_REF_MISMATCH", "remote main does not match the required %s phase commit", configuration.Phase)
	}
	var pull githubPullRequest
	if err := githubAPI(ctx, directory, runExternal, fmt.Sprintf("repos/%s/pulls/%d", configuration.Target, configuration.PullRequest), &pull); err != nil {
		return result, err
	}
	expectedPRState := "open"
	if configuration.Phase == "finalized" {
		expectedPRState = "closed"
	}
	if pull.Number != configuration.PullRequest || pull.State != expectedPRState || pull.Merged || pull.MergedAt != nil || pull.Head.Ref != configuration.CandidateRef || pull.Head.SHA != configuration.Candidate || pull.Base.Ref != "main" || pull.Base.SHA != configuration.Bootstrap {
		return result, problem("PULL_REQUEST_MISMATCH", "verification pull request does not match the candidate, phase, base, or unmerged state")
	}
	runs, verificationRun, err := collectRuns(ctx, directory, configuration, runExternal)
	if err != nil {
		return result, err
	}
	ruleset, requirements, err := collectRuleset(ctx, directory, configuration, runExternal)
	if err != nil {
		return result, err
	}
	checks, err := collectRequiredChecks(ctx, directory, configuration.Target, verificationRun, requirements, runExternal)
	if err != nil {
		return result, err
	}
	result.Repository = repositoryEvidence{FullName: configuration.Target, Visibility: "public", DefaultBranch: "main", AuthenticatedOwner: true}
	result.Remote = remoteEvidence{CandidateRef: configuration.CandidateRef, CandidateCommit: configuration.Candidate, MainRef: "main", MainCommit: expectedMain}
	result.PullRequest = pullRequestEvidence{Number: pull.Number, State: pull.State, HeadRef: pull.Head.Ref, HeadCommit: pull.Head.SHA, BaseRef: pull.Base.Ref, BaseCommit: pull.Base.SHA, Merged: false, MergeCommit: pull.MergeCommit}
	result.Runs, result.Ruleset, result.RequiredChecks = runs, ruleset, checks
	return result, nil
}

func githubAuthenticatedLogin(ctx context.Context, directory string, runExternal externalRunner) (string, error) {
	stdout, stderr, code, err := runExternal(ctx, githubCommandTimeout, directory, "gh", "api", "graphql", "-f", "query=query { viewer { login } }")
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", problem("COMMAND_TIMEOUT", "GitHub request exceeded %s", githubCommandTimeout)
		}
		return "", wrapProblem("GITHUB_CHECK_FAILED", err, "query authenticated GitHub viewer")
	}
	if code != 0 {
		return "", problem("GITHUB_CHECK_FAILED", "GitHub viewer request failed: %s", commandFailureOutput("", stderr))
	}
	var viewer githubViewer
	if err := json.Unmarshal([]byte(stdout), &viewer); err != nil {
		return "", wrapProblem("GITHUB_CHECK_FAILED", err, "decode authenticated GitHub viewer")
	}
	if strings.TrimSpace(viewer.Data.Viewer.Login) == "" {
		return "", problem("GITHUB_CHECK_FAILED", "authenticated GitHub viewer has no login")
	}
	return viewer.Data.Viewer.Login, nil
}

func collectRuns(ctx context.Context, directory string, configuration options, runExternal externalRunner) ([]runEvidence, int64, error) {
	runs := make([]runEvidence, 0, len(configuration.Runs))
	verificationRun, pullRuns, candidatePushes, mainPushes := int64(0), 0, 0, 0
	for _, spec := range configuration.Runs {
		var run githubRun
		if err := githubAPI(ctx, directory, runExternal, fmt.Sprintf("repos/%s/actions/runs/%d", configuration.Target, spec.ID), &run); err != nil {
			return nil, 0, err
		}
		if run.ID != spec.ID || run.Event != spec.Event || run.HeadBranch != spec.Branch || run.HeadSHA != configuration.Candidate || run.Status != "completed" || run.Conclusion != "success" || run.Attempt < 1 || strings.TrimSpace(run.Name) == "" {
			return nil, 0, problem("HOSTED_RUN_FAILED", "hosted run %s does not match its reviewed event, branch, candidate, or successful result", spec.Label)
		}
		if run.Event == "pull_request" && run.HeadBranch == configuration.CandidateRef {
			pullRuns++
			verificationRun = run.ID
		}
		if run.Event == "push" && run.HeadBranch == configuration.CandidateRef {
			candidatePushes++
		}
		if run.Event == "push" && run.HeadBranch == "main" {
			mainPushes++
		}
		runs = append(runs, runEvidence{Label: spec.Label, ID: run.ID, Event: run.Event, HeadBranch: run.HeadBranch, HeadCommit: run.HeadSHA, Status: run.Status, Conclusion: run.Conclusion, Attempt: run.Attempt, Workflow: run.Name})
	}
	if pullRuns != 1 || candidatePushes < 1 {
		return nil, 0, problem("HOSTED_RUN_INCOMPLETE", "runs must contain one candidate pull-request run and at least one candidate push run")
	}
	if configuration.Phase == "finalized" && mainPushes < 1 {
		return nil, 0, problem("HOSTED_RUN_INCOMPLETE", "finalized runs must contain a successful main push run")
	}
	return runs, verificationRun, nil
}

type checkRequirement struct {
	Context       string
	IntegrationID int64
}

func collectRuleset(ctx context.Context, directory string, configuration options, runExternal externalRunner) (rulesetEvidence, []checkRequirement, error) {
	var ruleset githubRuleset
	if err := githubAPI(ctx, directory, runExternal, fmt.Sprintf("repos/%s/rulesets/%d", configuration.Target, configuration.Ruleset), &ruleset); err != nil {
		return rulesetEvidence{}, nil, err
	}
	if ruleset.ID != configuration.Ruleset || strings.TrimSpace(ruleset.Name) == "" || ruleset.Target != "branch" || ruleset.Enforcement != "active" || len(ruleset.BypassActors) != 0 || !equalStrings(ruleset.Conditions.RefName.Include, []string{"~DEFAULT_BRANCH"}) || len(ruleset.Conditions.RefName.Exclude) != 0 {
		return rulesetEvidence{}, nil, problem("RULESET_MISMATCH", "ruleset must have a name, no bypass actor, and one active default-branch target")
	}
	ruleCounts := map[string]int{}
	var pullParameters struct {
		RequiredApprovingReviewCount  int
		DismissStaleReviews           bool
		RequireLastPushApproval       bool
		RequireReviewThreadResolution bool
		AllowedMergeMethods           []string
	}
	strict, requiredChecksOnCreate := false, false
	var requirements []checkRequirement
	for _, rule := range ruleset.Rules {
		ruleCounts[rule.Type]++
		switch rule.Type {
		case "pull_request":
			pullParameters.RequiredApprovingReviewCount = rule.Parameters.RequiredApprovingReviewCount
			pullParameters.DismissStaleReviews = rule.Parameters.DismissStaleReviews
			pullParameters.RequireLastPushApproval = rule.Parameters.RequireLastPushApproval
			pullParameters.RequireReviewThreadResolution = rule.Parameters.RequireReviewThreadResolution
			pullParameters.AllowedMergeMethods = append([]string(nil), rule.Parameters.AllowedMergeMethods...)
		case "required_status_checks":
			strict = rule.Parameters.StrictRequiredChecks
			requiredChecksOnCreate = !rule.Parameters.DoNotEnforceOnCreate
			for _, check := range rule.Parameters.RequiredChecks {
				requirements = append(requirements, checkRequirement{Context: check.Context, IntegrationID: check.IntegrationID})
			}
		}
	}
	for _, requiredRule := range []string{"deletion", "non_fast_forward", "required_linear_history", "pull_request", "required_status_checks"} {
		if ruleCounts[requiredRule] != 1 {
			return rulesetEvidence{}, nil, problem("RULESET_MISMATCH", "ruleset must contain exactly one %s rule", requiredRule)
		}
	}
	if pullParameters.RequiredApprovingReviewCount < 1 || !pullParameters.DismissStaleReviews || !pullParameters.RequireLastPushApproval || !pullParameters.RequireReviewThreadResolution || !equalStrings(pullParameters.AllowedMergeMethods, []string{"rebase"}) {
		return rulesetEvidence{}, nil, problem("RULESET_MISMATCH", "pull-request rule must require review, stale-review dismissal, last-push approval, resolved threads, and rebase-only merges")
	}
	if !strict || !requiredChecksOnCreate || len(requirements) == 0 {
		return rulesetEvidence{}, nil, problem("RULESET_MISMATCH", "status checks must be strict, required on branch creation, and non-empty")
	}
	sort.Slice(requirements, func(left, right int) bool { return requirements[left].Context < requirements[right].Context })
	seen := map[string]bool{}
	for _, requirement := range requirements {
		if strings.TrimSpace(requirement.Context) == "" || requirement.IntegrationID <= 0 || seen[requirement.Context] {
			return rulesetEvidence{}, nil, problem("RULESET_MISMATCH", "ruleset contains an invalid or duplicate required check")
		}
		seen[requirement.Context] = true
	}
	return rulesetEvidence{
		ID:                           ruleset.ID,
		Name:                         ruleset.Name,
		Enforcement:                  ruleset.Enforcement,
		NoBypassActors:               true,
		DeletionBlocked:              true,
		NonFastForwardBlocked:        true,
		LinearHistoryRequired:        true,
		PullRequestRequired:          true,
		RequiredApprovingReviewCount: pullParameters.RequiredApprovingReviewCount,
		DismissStaleReviews:          true,
		LastPushApprovalRequired:     true,
		ReviewThreadsResolved:        true,
		AllowedMergeMethods:          []string{"rebase"},
		StrictRequiredChecks:         true,
		RequiredChecksOnCreate:       true,
	}, requirements, nil
}

func collectRequiredChecks(ctx context.Context, directory, target string, verificationRun int64, requirements []checkRequirement, runExternal externalRunner) ([]requiredCheckEvidence, error) {
	var jobs githubJobs
	endpoint := fmt.Sprintf("repos/%s/actions/runs/%d/jobs?filter=latest&per_page=100", target, verificationRun)
	if err := githubAPI(ctx, directory, runExternal, endpoint, &jobs); err != nil {
		return nil, err
	}
	result := make([]requiredCheckEvidence, 0, len(requirements))
	for _, requirement := range requirements {
		var jobID int64
		for _, job := range jobs.Jobs {
			if job.Name == requirement.Context && job.Status == "completed" && job.Conclusion == "success" && job.ID > jobID {
				jobID = job.ID
			}
		}
		if jobID == 0 {
			return nil, problem("REQUIRED_CHECK_FAILED", "required check %q has no successful job in the verification run", requirement.Context)
		}
		var check githubCheckRun
		if err := githubAPI(ctx, directory, runExternal, fmt.Sprintf("repos/%s/check-runs/%d", target, jobID), &check); err != nil {
			return nil, err
		}
		if check.ID != jobID || check.Name != requirement.Context || check.App.ID != requirement.IntegrationID || check.Status != "completed" || check.Conclusion != "success" {
			return nil, problem("REQUIRED_CHECK_FAILED", "required check %q does not match its integration or successful result", requirement.Context)
		}
		result = append(result, requiredCheckEvidence{Context: check.Name, IntegrationID: check.App.ID, CheckRunID: check.ID, Status: check.Status, Conclusion: check.Conclusion})
	}
	return result, nil
}

func githubAPI(ctx context.Context, directory string, runExternal externalRunner, endpoint string, target any) error {
	stdout, stderr, code, err := runExternal(ctx, githubCommandTimeout, directory, "gh", "api", endpoint)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return problem("COMMAND_TIMEOUT", "GitHub request exceeded %s", githubCommandTimeout)
		}
		return wrapProblem("GITHUB_CHECK_FAILED", err, "query GitHub API")
	}
	if code != 0 {
		return problem("GITHUB_CHECK_FAILED", "GitHub API request failed: %s", commandFailureOutput("", stderr))
	}
	if err := json.Unmarshal([]byte(stdout), target); err != nil {
		return wrapProblem("GITHUB_CHECK_FAILED", err, "decode GitHub API response")
	}
	return nil
}

func allIdentityValuesMatch(values []string, expected string) bool {
	if len(values) == 0 || strings.TrimSpace(expected) == "" {
		return false
	}
	for _, value := range values {
		if !strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(expected)) {
			return false
		}
	}
	return true
}

func runSecretScans(ctx context.Context, directory string, runExternal externalRunner) error {
	for _, arguments := range [][]string{{"git", "--redact", "--exit-code", "1", "."}, {"dir", "--redact", "--exit-code", "1", "."}} {
		stdout, stderr, code, err := runExternal(ctx, secretScanTimeout, directory, "gitleaks", arguments...)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return problem("COMMAND_TIMEOUT", "secret scan exceeded %s", secretScanTimeout)
			}
			return wrapProblem("SECRET_SCAN_FAILED", err, "run gitleaks %s", arguments[0])
		}
		if code != 0 {
			return problem("SECRET_SCAN_FAILED", "gitleaks %s failed: %s", arguments[0], commandFailureOutput(stdout, stderr))
		}
	}
	return nil
}

func externalCommandWithTimeout(ctx context.Context, timeout time.Duration, directory, name string, arguments ...string) (string, string, int, error) {
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr, code, err := externalCommand(commandContext, directory, name, arguments...)
	if commandContext.Err() != nil {
		return stdout, stderr, -1, commandContext.Err()
	}
	return stdout, stderr, code, err
}

func externalCommand(ctx context.Context, directory, name string, arguments ...string) (string, string, int, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	command.Env = gitx.Environment(nil)
	stdout, stderr := newBoundedOutput(maximumCommandOutputBytes), newBoundedOutput(maximumCommandOutputBytes)
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
	return stdout.String(), stderr.String(), -1, err
}

type boundedOutput struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newBoundedOutput(maximum int) boundedOutput { return boundedOutput{remaining: maximum} }

func (output *boundedOutput) Write(value []byte) (int, error) {
	length, writeLength := len(value), min(output.remaining, len(value))
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

func commandFailureOutput(stdout, stderr string) string {
	var parts []string
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

func encodeReceipt(value receipt) ([]byte, string, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, "", wrapProblem("RECEIPT_ENCODE_FAILED", err, "encode correction receipt")
	}
	body = append(body, '\n')
	digest := sha256.Sum256(body)
	return body, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func writeReceipt(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return problem("OUTPUT_EXISTS", "receipt output already exists")
	}
	if err != nil {
		return wrapProblem("OUTPUT_WRITE_FAILED", err, "create correction receipt")
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if written, writeErr := file.Write(body); writeErr != nil || written != len(body) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return wrapProblem("OUTPUT_WRITE_FAILED", writeErr, "write correction receipt")
	}
	if err := file.Sync(); err != nil {
		return wrapProblem("OUTPUT_WRITE_FAILED", err, "sync correction receipt")
	}
	if err := file.Close(); err != nil {
		return wrapProblem("OUTPUT_WRITE_FAILED", err, "close correction receipt")
	}
	complete = true
	return nil
}

func validatedReceiptOutput(roots []string, name string) (string, error) {
	out, err := filepath.Abs(name)
	if err != nil {
		return "", wrapProblem("OUTPUT_UNSAFE", err, "resolve receipt output")
	}
	for _, root := range roots {
		inside, containmentErr := pathWithinRoot(root, out)
		if containmentErr != nil || inside {
			return "", problem("OUTPUT_UNSAFE", "--out must resolve outside the candidate and verifier checkouts")
		}
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(out))
	if err != nil {
		return "", wrapProblem("OUTPUT_UNSAFE", err, "resolve receipt directory")
	}
	info, err := os.Stat(resolvedParent)
	if err != nil || !info.IsDir() {
		return "", problem("OUTPUT_UNSAFE", "receipt parent must be an existing directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", problem("OUTPUT_UNSAFE", "receipt directory must not grant group or other permissions")
	}
	resolvedOut := filepath.Join(resolvedParent, filepath.Base(out))
	if _, err := os.Lstat(resolvedOut); err == nil {
		return "", problem("OUTPUT_EXISTS", "receipt output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", wrapProblem("OUTPUT_UNSAFE", err, "inspect receipt output")
	}
	return resolvedOut, nil
}

func pathWithinRoot(root, name string) (bool, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, err
	}
	var resolvedName string
	if existing, resolveErr := filepath.EvalSymlinks(name); resolveErr == nil {
		resolvedName = existing
	} else {
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(name))
		if parentErr != nil {
			return false, parentErr
		}
		resolvedName = filepath.Join(parent, filepath.Base(name))
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedName)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("document contains more than one JSON value")
		}
		return err
	}
	return nil
}

func nonemptyNULValues(output string) []string {
	var values []string
	for _, value := range strings.Split(output, "\x00") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		if strings.TrimSpace(value) != "" {
			keys = append(keys, value)
		}
	}
	sort.Strings(keys)
	return keys
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
