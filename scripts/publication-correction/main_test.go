package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/wip-commit/internal/gitx"
)

const (
	testTarget       = "owner/project"
	testOwner        = "owner"
	testPublicName   = "Public Author"
	testPublicEmail  = "public@example.invalid"
	testCandidateRef = "candidate/beta"
	testRulesetID    = int64(77)
	testIntegration  = int64(15368)
	testCandidateRun = int64(101)
	testPullRun      = int64(102)
	testMainRun      = int64(103)
	testCheckRun     = int64(201)
)

type correctionFixture struct {
	repo              gitx.Repo
	verifier          gitx.Repo
	bootstrap, old    string
	candidate         string
	correctionCommits []string
	pathsFile         string
	firstReceipt      string
	out               string
	privateDirectory  string
	hosted            *hostedFixture
}

type hostedRunFixture struct {
	ID         int64
	Event      string
	HeadBranch string
	HeadSHA    string
	Status     string
	Conclusion string
	Attempt    int
	Name       string
}

type hostedFixture struct {
	bootstrap, candidate, candidateRef string
	mainCommit                         string
	publicName, publicEmail            string
	repositoryPrivate                  bool
	repositoryVisibility               string
	remoteCandidate                    string
	pullState                          string
	pullHead                           string
	pullMerged                         bool
	pullMergedAt                       any
	runs                               map[int64]hostedRunFixture
	rulesetEnforcement                 string
	rulesetStrict                      bool
	rulesetBypass                      bool
	rulesetDeletion                    bool
	rulesetNonFastForward              bool
	rulesetLinearHistory               bool
	rulesetPullRequest                 bool
	rulesetReviewCount                 int
	rulesetDismissStale                bool
	rulesetLastPushApproval            bool
	rulesetReviewThreads               bool
	rulesetAllowedMergeMethods         []string
	rulesetDoNotEnforceOnCreate        bool
	checkIntegration                   int64
	checkConclusion                    string
	secretFailure                      bool
	timeoutCommand                     string
}

func TestRunFinalizedCorrectionSuccess(t *testing.T) {
	fixture := newCorrectionFixture(t, 1)
	stdout := runFixture(t, fixture, "finalized")
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result["ok"] != true || result["phase"] != "finalized" || result["candidate_commit"] != fixture.candidate {
		t.Fatalf("result = %#v", result)
	}
	var receiptValue receipt
	readJSONFile(t, fixture.out, &receiptValue)
	if receiptValue.SchemaVersion != receiptSchemaVersion || receiptValue.Correction.NewCandidate.Commit != fixture.candidate || receiptValue.Correction.NewCandidate.Tree == "" {
		t.Fatalf("receipt identity = %#v", receiptValue)
	}
	if len(receiptValue.Correction.Commits) != 1 || len(receiptValue.RequiredChecks) != 1 || len(receiptValue.HostedRuns) != 3 {
		t.Fatalf("receipt evidence counts = correction %d, checks %d, runs %d", len(receiptValue.Correction.Commits), len(receiptValue.RequiredChecks), len(receiptValue.HostedRuns))
	}
	if receiptValue.Remote.MainCommit != fixture.candidate || receiptValue.PullRequest.State != "closed" || receiptValue.PullRequest.Merged {
		t.Fatalf("finalized evidence = %#v %#v", receiptValue.Remote, receiptValue.PullRequest)
	}
	if !sha256Pattern.MatchString(receiptValue.FirstReceipt.Digest) || !sha256Pattern.MatchString(receiptValue.Identity.IdentitySetDigest) {
		t.Fatalf("receipt digests = %#v %#v", receiptValue.FirstReceipt, receiptValue.Identity)
	}
	if !sha256Pattern.MatchString(receiptValue.Verifier.CommandDigest) || !sha256Pattern.MatchString(receiptValue.Verifier.SchemaDigest) || !receiptValue.Ruleset.NoBypassActors || !receiptValue.Ruleset.LastPushApprovalRequired {
		t.Fatalf("verifier or ruleset evidence = %#v %#v", receiptValue.Verifier, receiptValue.Ruleset)
	}
	info, err := os.Stat(fixture.out)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %v", info.Mode())
	}
	body, err := os.ReadFile(fixture.out)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{fixture.repo.Root, fixture.firstReceipt, testPublicName, testPublicEmail, "token-value", "raw secret finding"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("receipt contains forbidden private value %q", forbidden)
		}
	}
}

func TestRunPreMainCorrectionSuccess(t *testing.T) {
	fixture := newCorrectionFixture(t, 1)
	fixture.hosted.mainCommit = fixture.bootstrap
	fixture.hosted.pullState = "open"
	delete(fixture.hosted.runs, testMainRun)
	stdout := runFixture(t, fixture, "pre-main")
	if !strings.Contains(stdout, `"phase":"pre-main"`) {
		t.Fatalf("stdout = %s", stdout)
	}
	var got receipt
	readJSONFile(t, fixture.out, &got)
	if got.Remote.MainCommit != fixture.bootstrap || got.PullRequest.State != "open" || len(got.HostedRuns) != 2 {
		t.Fatalf("pre-main receipt = %#v", got)
	}
}

func TestRunRecordsDisplayNameDifferenceWithoutWeakeningEmailIdentity(t *testing.T) {
	fixture := newCorrectionFixture(t, 1)
	fixture.hosted.publicName = "Different Display Name"
	runFixture(t, fixture, "finalized")
	var got receipt
	readJSONFile(t, fixture.out, &got)
	if !got.Identity.PublicEmailMatch || got.Identity.PublicNameMatch {
		t.Fatalf("identity result = %#v", got.Identity)
	}
}

func TestRunRecordsMultipleLinearCorrectionCommits(t *testing.T) {
	fixture := newCorrectionFixture(t, 3)
	runFixture(t, fixture, "finalized")
	var got receipt
	readJSONFile(t, fixture.out, &got)
	if len(got.Correction.Commits) != 3 {
		t.Fatalf("correction commits = %#v", got.Correction.Commits)
	}
	previous := fixture.old
	for index, commit := range got.Correction.Commits {
		if commit.Parent != previous || commit.Commit != fixture.correctionCommits[index] || commit.Tree == "" {
			t.Fatalf("commit %d = %#v", index, commit)
		}
		previous = commit.Commit
	}
}

func TestRunRejectsHostedEvidenceMismatchesWithoutReceipt(t *testing.T) {
	tests := []struct {
		name string
		code string
		edit func(*correctionFixture)
	}{
		{name: "moved candidate ref", code: "REMOTE_REF_MISMATCH", edit: func(f *correctionFixture) { f.hosted.remoteCandidate = f.old }},
		{name: "failed run", code: "HOSTED_RUN_FAILED", edit: func(f *correctionFixture) {
			run := f.hosted.runs[testCandidateRun]
			run.Conclusion = "failure"
			f.hosted.runs[testCandidateRun] = run
		}},
		{name: "pending run", code: "HOSTED_RUN_FAILED", edit: func(f *correctionFixture) {
			run := f.hosted.runs[testCandidateRun]
			run.Status, run.Conclusion = "in_progress", ""
			f.hosted.runs[testCandidateRun] = run
		}},
		{name: "run for another head", code: "HOSTED_RUN_FAILED", edit: func(f *correctionFixture) {
			run := f.hosted.runs[testPullRun]
			run.HeadSHA = f.old
			f.hosted.runs[testPullRun] = run
		}},
		{name: "pull request head moved", code: "PULL_REQUEST_MISMATCH", edit: func(f *correctionFixture) { f.hosted.pullHead = f.old }},
		{name: "pull request merged", code: "PULL_REQUEST_MISMATCH", edit: func(f *correctionFixture) {
			f.hosted.pullMerged = true
			f.hosted.pullMergedAt = "2026-08-17T00:00:00Z"
		}},
		{name: "private repository", code: "TARGET_MISMATCH", edit: func(f *correctionFixture) {
			f.hosted.repositoryPrivate, f.hosted.repositoryVisibility = true, "private"
		}},
		{name: "public identity changed", code: "IDENTITY_MISMATCH", edit: func(f *correctionFixture) { f.hosted.publicEmail = "different@example.invalid" }},
		{name: "failed required check", code: "REQUIRED_CHECK_FAILED", edit: func(f *correctionFixture) { f.hosted.checkConclusion = "failure" }},
		{name: "different check integration", code: "REQUIRED_CHECK_FAILED", edit: func(f *correctionFixture) { f.hosted.checkIntegration = 999 }},
		{name: "inactive ruleset", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetEnforcement = "disabled" }},
		{name: "non-strict ruleset", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetStrict = false }},
		{name: "ruleset bypass", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetBypass = true }},
		{name: "deletion allowed", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetDeletion = false }},
		{name: "force push allowed", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetNonFastForward = false }},
		{name: "nonlinear history", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetLinearHistory = false }},
		{name: "pull requests optional", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetPullRequest = false }},
		{name: "reviews optional", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetReviewCount = 0 }},
		{name: "stale reviews retained", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetDismissStale = false }},
		{name: "last push approval optional", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetLastPushApproval = false }},
		{name: "review threads unresolved", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetReviewThreads = false }},
		{name: "merge commits allowed", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetAllowedMergeMethods = []string{"merge", "rebase"} }},
		{name: "checks skipped on create", code: "RULESET_MISMATCH", edit: func(f *correctionFixture) { f.hosted.rulesetDoNotEnforceOnCreate = true }},
		{name: "secret scan failure", code: "SECRET_SCAN_FAILED", edit: func(f *correctionFixture) { f.hosted.secretFailure = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCorrectionFixture(t, 1)
			test.edit(fixture)
			expectFixtureFailure(t, fixture, "finalized", test.code)
		})
	}
}

func TestRunRejectsLocalEvidenceMismatchesWithoutReceipt(t *testing.T) {
	t.Run("changed path manifest", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 1)
		writeCorrectionFile(t, fixture.repo.Root, fixture.pathsFile, "core.txt\ndocs/paths.txt\n")
		correctionGit(t, fixture.repo.Root, "add", fixture.pathsFile)
		correctionGit(t, fixture.repo.Root, "commit", "-m", "test: change reviewed manifest")
		fixture.updateCandidate(correctionGit(t, fixture.repo.Root, "rev-parse", "HEAD"))
		expectFixtureFailure(t, fixture, "finalized", "PATH_MANIFEST_MISMATCH")
	})

	t.Run("merge commit", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 1)
		correctionGit(t, fixture.repo.Root, "branch", "side", fixture.old)
		correctionGit(t, fixture.repo.Root, "switch", "side")
		writeCorrectionFile(t, fixture.repo.Root, "side.txt", "side\n")
		correctionGit(t, fixture.repo.Root, "add", "side.txt")
		correctionGit(t, fixture.repo.Root, "commit", "-m", "test: create side")
		correctionGit(t, fixture.repo.Root, "switch", "main")
		correctionGit(t, fixture.repo.Root, "merge", "--no-ff", "side", "-m", "test: merge side")
		fixture.updateCandidate(correctionGit(t, fixture.repo.Root, "rev-parse", "HEAD"))
		expectFixtureFailure(t, fixture, "finalized", "CORRECTION_NOT_LINEAR")
	})

	t.Run("checked out commit differs", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 2)
		arguments := fixture.arguments("finalized")
		replaceFlag(arguments, "--candidate", fixture.correctionCommits[0])
		err := runWithDependencies(context.Background(), arguments, io.Discard, fixture.dependencies())
		assertTypedError(t, err, "CANDIDATE_MISMATCH")
		assertNoFile(t, fixture.out)
	})

	t.Run("different target repository", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 1)
		arguments := fixture.arguments("finalized")
		replaceFlag(arguments, "--target", "owner/different")
		err := runWithDependencies(context.Background(), arguments, io.Discard, fixture.dependencies())
		assertTypedError(t, err, "TARGET_MISMATCH")
		assertNoFile(t, fixture.out)
	})

	t.Run("first receipt trees differ", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 1)
		var first map[string]any
		readJSONFile(t, fixture.firstReceipt, &first)
		first["candidate"].(map[string]any)["tree"] = fixture.bootstrap
		writeJSONFile(t, fixture.firstReceipt, first, 0o600)
		expectFixtureFailure(t, fixture, "finalized", "FIRST_RECEIPT_MISMATCH")
	})

	t.Run("corrupt object database", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 1)
		object := fixture.candidate
		path := filepath.Join(fixture.repo.GitDir, "objects", object[:2], object[2:])
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		expectFixtureFailure(t, fixture, "finalized", "OBJECT_CHECK_FAILED")
	})
}

func TestRunRejectsInvalidVerifierWithoutReceipt(t *testing.T) {
	t.Run("dirty verifier", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 1)
		writeCorrectionFile(t, fixture.verifier.Root, "untracked.txt", "dirty\n")
		expectFixtureFailure(t, fixture, "finalized", "VERIFIER_INVALID")
	})

	t.Run("wrong verifier module", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 1)
		writeCorrectionFile(t, fixture.verifier.Root, "go.mod", "module github.com/owner/different\n\ngo 1.25.12\n")
		correctionGit(t, fixture.verifier.Root, "add", "go.mod")
		correctionGit(t, fixture.verifier.Root, "commit", "-m", "test: change verifier module")
		expectFixtureFailure(t, fixture, "finalized", "VERIFIER_INVALID")
	})

	t.Run("missing verifier schema", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 1)
		correctionGit(t, fixture.verifier.Root, "rm", verifierSchemaPath)
		correctionGit(t, fixture.verifier.Root, "commit", "-m", "test: remove verifier schema")
		expectFixtureFailure(t, fixture, "finalized", "VERIFIER_INVALID")
	})
}

func TestRunRejectsUnsafeOrExistingOutput(t *testing.T) {
	t.Run("existing output", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 1)
		if err := os.WriteFile(fixture.out, []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := runWithDependencies(context.Background(), fixture.arguments("finalized"), io.Discard, fixture.dependencies())
		assertTypedError(t, err, "OUTPUT_EXISTS")
		body, err := os.ReadFile(fixture.out)
		if err != nil || string(body) != "keep\n" {
			t.Fatalf("existing output changed: %q, %v", body, err)
		}
	})

	t.Run("output inside checkout", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 1)
		fixture.out = filepath.Join(fixture.repo.Root, "receipt.json")
		expectFixtureFailure(t, fixture, "finalized", "OUTPUT_UNSAFE")
	})

	t.Run("output inside verifier checkout", func(t *testing.T) {
		fixture := newCorrectionFixture(t, 1)
		fixture.out = filepath.Join(fixture.verifier.Root, "receipt.json")
		expectFixtureFailure(t, fixture, "finalized", "OUTPUT_UNSAFE")
	})

	if runtime.GOOS != "windows" {
		t.Run("open receipt directory", func(t *testing.T) {
			fixture := newCorrectionFixture(t, 1)
			if err := os.Chmod(fixture.privateDirectory, 0o755); err != nil {
				t.Fatal(err)
			}
			expectFixtureFailure(t, fixture, "finalized", "OUTPUT_UNSAFE")
		})
	}
}

func TestRunRejectsTimeoutAndIncompleteDependencies(t *testing.T) {
	fixture := newCorrectionFixture(t, 1)
	fixture.hosted.timeoutCommand = "gitleaks"
	expectFixtureFailure(t, fixture, "finalized", "COMMAND_TIMEOUT")
	if err := runWithDependencies(context.Background(), nil, io.Discard, dependencies{}); errorCode(err) != "DEPENDENCIES_INCOMPLETE" {
		t.Fatalf("incomplete dependencies error = %v (%s)", err, errorCode(err))
	}
}

func TestReceiptEncodingIsDeterministicAndRedacted(t *testing.T) {
	first := newCorrectionFixture(t, 2)
	runFixture(t, first, "finalized")
	firstBody, err := os.ReadFile(first.out)
	if err != nil {
		t.Fatal(err)
	}
	// Commit object IDs differ across fixture repositories. Compare two encodings
	// of the same validated receipt instead of two repository histories.
	var value receipt
	if err := json.Unmarshal(firstBody, &value); err != nil {
		t.Fatal(err)
	}
	bodyOne, digestOne, err := encodeReceipt(value)
	if err != nil {
		t.Fatal(err)
	}
	bodyTwo, digestTwo, err := encodeReceipt(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(bodyOne) != string(bodyTwo) || digestOne != digestTwo {
		t.Fatal("receipt encoding or digest is not deterministic")
	}
}

func TestReceiptSchemaMatchesGeneratedTopLevelContract(t *testing.T) {
	fixture := newCorrectionFixture(t, 1)
	runFixture(t, fixture, "finalized")
	var document map[string]any
	readJSONFile(t, fixture.out, &document)
	var schema struct {
		Schema               string         `json:"$schema"`
		AdditionalProperties bool           `json:"additionalProperties"`
		Required             []string       `json:"required"`
		Properties           map[string]any `json:"properties"`
	}
	readJSONFile(t, filepath.Join("..", "..", "docs", "PUBLICATION-CORRECTION.schema.json"), &schema)
	if schema.Schema != "https://json-schema.org/draft/2020-12/schema" || schema.AdditionalProperties {
		t.Fatalf("schema identity = %#v", schema)
	}
	required := map[string]bool{}
	for _, name := range schema.Required {
		if required[name] {
			t.Fatalf("duplicate required property %q", name)
		}
		required[name] = true
	}
	if len(required) != len(document) || len(schema.Properties) != len(document) {
		t.Fatalf("schema keys = required %d, properties %d, receipt %d", len(required), len(schema.Properties), len(document))
	}
	for name := range document {
		if !required[name] || schema.Properties[name] == nil {
			t.Errorf("generated property %q is not required by the schema", name)
		}
	}
}

func TestExternalCommandWithTimeout(t *testing.T) {
	t.Setenv("GO_WANT_CORRECTION_HELPER", "1")
	arguments := func(mode string) []string {
		return []string{"-test.run=^TestCorrectionExternalHelperProcess$", "--", mode}
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

func TestCorrectionExternalHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CORRECTION_HELPER") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
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

func newCorrectionFixture(t *testing.T, correctionCount int) *correctionFixture {
	t.Helper()
	if correctionCount < 1 {
		t.Fatal("correctionCount must be positive")
	}
	directory := t.TempDir()
	correctionGit(t, directory, "init", "-b", "main")
	correctionGit(t, directory, "config", "user.name", testPublicName)
	correctionGit(t, directory, "config", "user.email", testPublicEmail)
	writeCorrectionFile(t, directory, "README.md", "base\n")
	writeCorrectionFile(t, directory, "go.mod", "module github.com/"+testTarget+"\n\ngo 1.25.12\n")
	correctionGit(t, directory, "add", "README.md", "go.mod")
	correctionGit(t, directory, "commit", "-m", "test: create bootstrap")
	bootstrap := correctionGit(t, directory, "rev-parse", "HEAD")

	paths := []string{"core.txt", "docs/paths.txt"}
	for index := 1; index <= correctionCount; index++ {
		paths = append(paths, fmt.Sprintf("fix-%d.txt", index))
	}
	writeCorrectionFile(t, directory, "core.txt", "old candidate\n")
	writeCorrectionFile(t, directory, "docs/paths.txt", strings.Join(paths, "\n")+"\n")
	correctionGit(t, directory, "add", "core.txt", "docs/paths.txt")
	correctionGit(t, directory, "commit", "-m", "test: create old candidate")
	oldCandidate := correctionGit(t, directory, "rev-parse", "HEAD")

	var corrections []string
	for index := 1; index <= correctionCount; index++ {
		name := fmt.Sprintf("fix-%d.txt", index)
		writeCorrectionFile(t, directory, name, fmt.Sprintf("correction %d\n", index))
		correctionGit(t, directory, "add", name)
		correctionGit(t, directory, "commit", "-m", fmt.Sprintf("fix: correction %d", index))
		corrections = append(corrections, correctionGit(t, directory, "rev-parse", "HEAD"))
	}
	candidate := corrections[len(corrections)-1]
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	verifier := newVerifierFixture(t)
	privateDirectory := t.TempDir()
	if err := os.Chmod(privateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	firstReceipt := filepath.Join(privateDirectory, "first.json")
	first := map[string]any{
		"schema_version":    "1.0.0",
		"target_repository": testTarget,
		"target_visibility": "public",
		"default_branch":    "main",
		"bootstrap": map[string]any{
			"commit": bootstrap,
			"tree":   correctionGit(t, directory, "show", "-s", "--format=%T", bootstrap),
		},
		"candidate": map[string]any{
			"commit": oldCandidate,
			"tree":   correctionGit(t, directory, "show", "-s", "--format=%T", oldCandidate),
		},
		"history_commit_count": 2,
		"delta_paths":          []string{"core.txt", "docs/paths.txt"},
		"local": map[string]any{
			"clean": true, "history_secret_scan": "passed", "worktree_secret_scan": "passed",
			"object_integrity_check": "passed", "expected_delta_matched": true,
			"bootstrap_is_ancestor": true, "linear_candidate_range": true,
		},
		"github": map[string]any{
			"authenticated_owner": true, "target_repository_exists": false, "public_author_identity_match": true,
		},
	}
	writeJSONFile(t, firstReceipt, first, 0o600)
	hosted := newHostedFixture(bootstrap, candidate)
	return &correctionFixture{
		repo:              repo,
		verifier:          verifier,
		bootstrap:         bootstrap,
		old:               oldCandidate,
		candidate:         candidate,
		correctionCommits: corrections,
		pathsFile:         "docs/paths.txt",
		firstReceipt:      firstReceipt,
		out:               filepath.Join(privateDirectory, "correction.json"),
		privateDirectory:  privateDirectory,
		hosted:            hosted,
	}
}

func newVerifierFixture(t *testing.T) gitx.Repo {
	t.Helper()
	directory := t.TempDir()
	correctionGit(t, directory, "init", "-b", "main")
	correctionGit(t, directory, "config", "user.name", testPublicName)
	correctionGit(t, directory, "config", "user.email", testPublicEmail)
	writeCorrectionFile(t, directory, "go.mod", "module github.com/"+testTarget+"\n\ngo 1.25.12\n")
	writeCorrectionFile(t, directory, verifierCommandPath, "package main\n")
	writeCorrectionFile(t, directory, verifierSchemaPath, "{}\n")
	correctionGit(t, directory, "add", "go.mod", verifierCommandPath, verifierSchemaPath)
	correctionGit(t, directory, "commit", "-m", "test: create verifier")
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func newHostedFixture(bootstrap, candidate string) *hostedFixture {
	return &hostedFixture{
		bootstrap:                  bootstrap,
		candidate:                  candidate,
		candidateRef:               testCandidateRef,
		mainCommit:                 candidate,
		publicName:                 testPublicName,
		publicEmail:                testPublicEmail,
		repositoryVisibility:       "public",
		remoteCandidate:            candidate,
		pullState:                  "closed",
		pullHead:                   candidate,
		rulesetEnforcement:         "active",
		rulesetStrict:              true,
		rulesetDeletion:            true,
		rulesetNonFastForward:      true,
		rulesetLinearHistory:       true,
		rulesetPullRequest:         true,
		rulesetReviewCount:         1,
		rulesetDismissStale:        true,
		rulesetLastPushApproval:    true,
		rulesetReviewThreads:       true,
		rulesetAllowedMergeMethods: []string{"rebase"},
		checkIntegration:           testIntegration,
		checkConclusion:            "success",
		runs: map[int64]hostedRunFixture{
			testCandidateRun: {ID: testCandidateRun, Event: "push", HeadBranch: testCandidateRef, HeadSHA: candidate, Status: "completed", Conclusion: "success", Attempt: 1, Name: "ci"},
			testPullRun:      {ID: testPullRun, Event: "pull_request", HeadBranch: testCandidateRef, HeadSHA: candidate, Status: "completed", Conclusion: "success", Attempt: 1, Name: "ci"},
			testMainRun:      {ID: testMainRun, Event: "push", HeadBranch: "main", HeadSHA: candidate, Status: "completed", Conclusion: "success", Attempt: 1, Name: "ci"},
		},
	}
}

func (fixture *correctionFixture) updateCandidate(candidate string) {
	fixture.candidate = candidate
	fixture.hosted.candidate = candidate
	fixture.hosted.mainCommit = candidate
	fixture.hosted.remoteCandidate = candidate
	fixture.hosted.pullHead = candidate
	for id, run := range fixture.hosted.runs {
		run.HeadSHA = candidate
		fixture.hosted.runs[id] = run
	}
}

func (fixture *correctionFixture) arguments(phase string) []string {
	arguments := []string{
		"--repo-dir", fixture.repo.Root,
		"--verifier-dir", fixture.verifier.Root,
		"--target", testTarget,
		"--phase", phase,
		"--bootstrap", fixture.bootstrap,
		"--old-candidate", fixture.old,
		"--candidate", fixture.candidate,
		"--candidate-ref", testCandidateRef,
		"--first-receipt", fixture.firstReceipt,
		"--paths-file", fixture.pathsFile,
		"--pull-request", "1",
		"--ruleset", strconv.FormatInt(testRulesetID, 10),
		"--out", fixture.out,
		"--run", fmt.Sprintf("candidate-push:push:%s:%d", testCandidateRef, testCandidateRun),
		"--run", fmt.Sprintf("verification-pr:pull_request:%s:%d", testCandidateRef, testPullRun),
	}
	if phase == "finalized" {
		arguments = append(arguments, "--run", fmt.Sprintf("main-push:push:main:%d", testMainRun))
	}
	return arguments
}

func (fixture *correctionFixture) dependencies() dependencies {
	return dependencies{
		external: fixture.hosted.run,
		now:      func() time.Time { return time.Date(2026, time.August, 17, 12, 0, 0, 123456789, time.UTC) },
	}
}

func (fixture *hostedFixture) run(_ context.Context, _ time.Duration, _ string, name string, arguments ...string) (string, string, int, error) {
	if fixture.timeoutCommand == name {
		return "", "", -1, context.DeadlineExceeded
	}
	if name == "gitleaks" {
		if fixture.secretFailure {
			return "raw secret finding", "", 1, nil
		}
		return "", "", 0, nil
	}
	if name == "gh" && len(arguments) == 4 && arguments[0] == "api" && arguments[1] == "graphql" && arguments[2] == "-f" {
		body, err := json.Marshal(map[string]any{"data": map[string]any{"viewer": map[string]any{"login": testOwner}}})
		if err != nil {
			return "", "", -1, err
		}
		return string(body), "", 0, nil
	}
	if name != "gh" || len(arguments) != 2 || arguments[0] != "api" {
		return "", "", -1, fmt.Errorf("unexpected external command %q %q", name, arguments)
	}
	endpoint := arguments[1]
	var value any
	switch endpoint {
	case "users/" + testOwner:
		value = map[string]any{"login": testOwner, "name": fixture.publicName, "email": fixture.publicEmail}
	case "repos/" + testTarget:
		value = map[string]any{
			"full_name": testTarget, "visibility": fixture.repositoryVisibility, "private": fixture.repositoryPrivate,
			"default_branch": "main", "owner": map[string]any{"login": testOwner},
		}
	case "repos/" + testTarget + "/git/ref/heads/" + fixture.candidateRef:
		value = map[string]any{"ref": "refs/heads/" + fixture.candidateRef, "object": map[string]any{"sha": fixture.remoteCandidate, "type": "commit"}}
	case "repos/" + testTarget + "/git/ref/heads/main":
		value = map[string]any{"ref": "refs/heads/main", "object": map[string]any{"sha": fixture.mainCommit, "type": "commit"}}
	case "repos/" + testTarget + "/pulls/1":
		value = map[string]any{
			"number": 1, "state": fixture.pullState, "merged": fixture.pullMerged, "merged_at": fixture.pullMergedAt,
			"merge_commit_sha": fixture.candidate,
			"head":             map[string]any{"ref": fixture.candidateRef, "sha": fixture.pullHead},
			"base":             map[string]any{"ref": "main", "sha": fixture.bootstrap},
		}
	case fmt.Sprintf("repos/%s/rulesets/%d", testTarget, testRulesetID):
		var bypassActors any
		if fixture.rulesetBypass {
			bypassActors = []any{map[string]any{"actor_id": 1, "actor_type": "RepositoryRole", "bypass_mode": "always"}}
		}
		var rules []any
		if fixture.rulesetDeletion {
			rules = append(rules, map[string]any{"type": "deletion"})
		}
		if fixture.rulesetNonFastForward {
			rules = append(rules, map[string]any{"type": "non_fast_forward"})
		}
		if fixture.rulesetLinearHistory {
			rules = append(rules, map[string]any{"type": "required_linear_history"})
		}
		if fixture.rulesetPullRequest {
			rules = append(rules, map[string]any{"type": "pull_request", "parameters": map[string]any{
				"required_approving_review_count":   fixture.rulesetReviewCount,
				"dismiss_stale_reviews_on_push":     fixture.rulesetDismissStale,
				"require_last_push_approval":        fixture.rulesetLastPushApproval,
				"required_review_thread_resolution": fixture.rulesetReviewThreads,
				"allowed_merge_methods":             fixture.rulesetAllowedMergeMethods,
			}})
		}
		rules = append(rules, map[string]any{"type": "required_status_checks", "parameters": map[string]any{
			"strict_required_status_checks_policy": fixture.rulesetStrict,
			"do_not_enforce_on_create":             fixture.rulesetDoNotEnforceOnCreate,
			"required_status_checks":               []any{map[string]any{"context": "test", "integration_id": testIntegration}},
		}})
		value = map[string]any{
			"id": testRulesetID, "name": "Protect main", "target": "branch", "enforcement": fixture.rulesetEnforcement,
			"bypass_actors": bypassActors,
			"conditions":    map[string]any{"ref_name": map[string]any{"include": []string{"~DEFAULT_BRANCH"}, "exclude": []string{}}},
			"rules":         rules,
		}
	case fmt.Sprintf("repos/%s/actions/runs/%d/jobs?filter=latest&per_page=100", testTarget, testPullRun):
		value = map[string]any{"jobs": []any{map[string]any{"id": testCheckRun, "name": "test", "status": "completed", "conclusion": "success"}}}
	case fmt.Sprintf("repos/%s/check-runs/%d", testTarget, testCheckRun):
		value = map[string]any{"id": testCheckRun, "name": "test", "status": "completed", "conclusion": fixture.checkConclusion, "app": map[string]any{"id": fixture.checkIntegration}}
	default:
		prefix := "repos/" + testTarget + "/actions/runs/"
		if strings.HasPrefix(endpoint, prefix) {
			id, err := strconv.ParseInt(strings.TrimPrefix(endpoint, prefix), 10, 64)
			run, ok := fixture.runs[id]
			if err != nil || !ok {
				return "", "not found", 1, nil
			}
			value = map[string]any{
				"id": run.ID, "event": run.Event, "head_branch": run.HeadBranch, "head_sha": run.HeadSHA,
				"status": run.Status, "conclusion": run.Conclusion, "run_attempt": run.Attempt, "name": run.Name,
			}
		} else {
			return "", "", -1, fmt.Errorf("unexpected GitHub endpoint %q", endpoint)
		}
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "", "", -1, err
	}
	return string(body), "", 0, nil
}

func runFixture(t *testing.T, fixture *correctionFixture, phase string) string {
	t.Helper()
	var stdout strings.Builder
	if err := runWithDependencies(context.Background(), fixture.arguments(phase), &stdout, fixture.dependencies()); err != nil {
		t.Fatalf("run: %s: %v", errorCode(err), err)
	}
	return stdout.String()
}

func expectFixtureFailure(t *testing.T, fixture *correctionFixture, phase, code string) {
	t.Helper()
	err := runWithDependencies(context.Background(), fixture.arguments(phase), io.Discard, fixture.dependencies())
	assertTypedError(t, err, code)
	assertNoFile(t, fixture.out)
}

func assertTypedError(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || errorCode(err) != code {
		t.Fatalf("error = %v (%s), want %s", err, errorCode(err), code)
	}
}

func assertNoFile(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected output %s: %v", name, err)
	}
}

func replaceFlag(arguments []string, name, value string) {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == name {
			arguments[index+1] = value
			return
		}
	}
}

func writeCorrectionFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeJSONFile(t *testing.T, name string, value any, mode os.FileMode) {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, append(body, '\n'), mode); err != nil {
		t.Fatal(err)
	}
}

func readJSONFile(t *testing.T, name string, target any) {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatal(err)
	}
}

func correctionGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = gitx.Environment(nil)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}
