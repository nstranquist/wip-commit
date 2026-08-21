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
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	benchmarkName          = "wip-commit-portable-behavior"
	benchmarkVersion       = "1.0.0"
	reportSchemaVersion    = "1.0.0"
	defaultCommandTimeout  = 45 * time.Second
	maxCommandOutputBytes  = 256 * 1024
	maxJSONArtifactBytes   = 1 << 20
	maxCandidateBytes      = 256 << 20
	benchmarkAgent         = "bench"
	benchmarkSession       = "portable"
	weakenedCandidateLabel = "deliberately-weakened"
)

var requiredCaseIDs = []string{
	"setup-shared-linked",
	"disjoint-parallel-lanes",
	"reviewed-split-default",
	"explicit-single-opt-out",
	"foreign-index-preservation",
	"dry-run-no-publication",
	"failed-verification-no-publication",
	"reconcile-interrupted-publication",
}

type baseline struct {
	SchemaVersion            string   `json:"schema_version"`
	Benchmark                string   `json:"benchmark"`
	BenchmarkVersion         string   `json:"benchmark_version"`
	ReviewStatus             string   `json:"review_status"`
	MinimumScore             float64  `json:"minimum_score"`
	RequiredPositiveChecks   int      `json:"required_positive_checks"`
	RequiredNegativeControls int      `json:"required_negative_controls"`
	ZeroAIRequired           bool     `json:"zero_ai_required"`
	RequiredCases            []string `json:"required_cases"`
}

type assertion struct {
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type caseResult struct {
	ID              string    `json:"id"`
	Passed          bool      `json:"passed"`
	Positive        assertion `json:"positive"`
	NegativeControl assertion `json:"negative_control"`
	ElapsedMS       int64     `json:"elapsed_ms"`
}

type overallResult struct {
	TotalChecks            int     `json:"total_checks"`
	PassedChecks           int     `json:"passed_checks"`
	FailedChecks           int     `json:"failed_checks"`
	PassedPositiveChecks   int     `json:"passed_positive_checks"`
	PassedNegativeControls int     `json:"passed_negative_controls"`
	Score                  float64 `json:"score"`
	MeetsReviewedBaseline  bool    `json:"meets_reviewed_baseline"`
}

type candidateInfo struct {
	Kind        string `json:"kind"`
	SHA256      string `json:"sha256"`
	Version     string `json:"version,omitempty"`
	Revision    string `json:"revision,omitempty"`
	SourceState string `json:"source_state,omitempty"`
}

type aiUsage struct {
	Calls  int `json:"calls"`
	Tokens int `json:"tokens"`
}

type report struct {
	SchemaVersion    string        `json:"schema_version"`
	Benchmark        string        `json:"benchmark"`
	BenchmarkVersion string        `json:"benchmark_version"`
	Platform         string        `json:"platform"`
	Candidate        candidateInfo `json:"candidate"`
	BaselineSHA256   string        `json:"baseline_sha256"`
	Cases            []caseResult  `json:"cases"`
	Overall          overallResult `json:"overall"`
	ElapsedMS        int64         `json:"elapsed_ms"`
	AIUsage          aiUsage       `json:"ai_usage"`
	ZeroAI           bool          `json:"zero_ai"`
}

type proposal struct {
	SchemaVersion    string   `json:"schema_version"`
	Benchmark        string   `json:"benchmark"`
	BenchmarkVersion string   `json:"benchmark_version"`
	BaselineScore    float64  `json:"baseline_score"`
	CandidateScore   float64  `json:"candidate_score"`
	FailedCaseIDs    []string `json:"failed_case_ids"`
	Action           string   `json:"action"`
}

type envelopeError struct {
	Code string `json:"code"`
}

type envelope struct {
	OK     bool            `json:"ok"`
	Action string          `json:"action"`
	Data   json.RawMessage `json:"data"`
	Error  *envelopeError  `json:"error"`
}

type rawCommand struct {
	ExitCode int
	Stdout   string
	Stderr   string
	TimedOut bool
	Err      error
}

type wipRun struct {
	Raw      rawCommand
	Envelope envelope
	Decoded  bool
}

func (result wipRun) succeeded() bool {
	return result.Raw.ExitCode == 0 && result.Decoded && result.Envelope.OK
}

func (result wipRun) failedWith(code string) bool {
	return result.Raw.ExitCode != 0 && result.Decoded && !result.Envelope.OK && result.Envelope.Error != nil && result.Envelope.Error.Code == code
}

type fixture struct {
	repo string
	home string
}

type harness struct {
	candidate string
	root      string
}

type directCapture struct {
	FinalCommit string `json:"final_commit"`
	RefUpdated  bool   `json:"ref_updated"`
	DryRun      bool   `json:"dry_run"`
	PlanID      string `json:"plan_id"`
	PlanDigest  string `json:"plan_digest"`
	IntentState string `json:"intent_state"`
	GateOutcome string `json:"gate_outcome"`
	Commits     []struct {
		Commit string `json:"commit"`
	} `json:"commits"`
}

type guidedCapture struct {
	Capture directCapture `json:"capture"`
	Handoff struct {
		CaptureOnly       bool `json:"capture_only"`
		LandingAuthorized bool `json:"landing_authorized"`
	} `json:"handoff"`
}

type reconcileData struct {
	IntentState  string `json:"intent_state"`
	AlreadyClean bool   `json:"already_clean"`
}

func main() {
	candidateFlag := flag.String("candidate", "", "evaluate an existing candidate executable")
	outDirFlag := flag.String("out-dir", "", "directory for mutable benchmark artifacts")
	proveWeakened := flag.Bool("prove-weakened", false, "prove that the reviewed baseline rejects the deliberately weakened candidate")
	flag.Parse()
	if flag.NArg() != 0 || (*proveWeakened && *candidateFlag != "") {
		fmt.Fprintln(os.Stderr, "invalid benchmark arguments")
		os.Exit(3)
	}

	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve repository root: %v\n", err)
		os.Exit(3)
	}
	benchmarkDir := filepath.Join(root, "skills", "wip-commit", "benchmark")
	baselinePath := filepath.Join(benchmarkDir, "baseline.json")
	reviewed, baselineBody, err := loadBaseline(baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load reviewed baseline: %v\n", err)
		os.Exit(3)
	}
	baselineDigest := sha256Text(baselineBody)
	artifactDir, err := resolveArtifactDir(*outDirFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve artifact directory: %v\n", err)
		os.Exit(3)
	}
	if err := prepareArtifactDir(artifactDir); err != nil {
		fmt.Fprintf(os.Stderr, "prepare artifact directory: %v\n", err)
		os.Exit(3)
	}

	workspace, err := os.MkdirTemp("", "wip-portable-benchmark-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create disposable benchmark workspace: %v\n", err)
		os.Exit(3)
	}
	defer func() { _ = os.RemoveAll(workspace) }()

	candidate := *candidateFlag
	candidateKind := "repository-build"
	if *proveWeakened {
		candidateKind = weakenedCandidateLabel
		candidate = filepath.Join(workspace, executableName("wip-weakened"))
		source := filepath.Join(benchmarkDir, "testdata", "weakened", "main.go")
		if err := buildGoCandidate(root, candidate, source); err != nil {
			fmt.Fprintf(os.Stderr, "build weakened candidate: %v\n", err)
			os.Exit(3)
		}
	} else if candidate == "" {
		candidate = filepath.Join(workspace, executableName("wip-candidate"))
		if err := buildGoCandidate(root, candidate, "./cmd/wip"); err != nil {
			fmt.Fprintf(os.Stderr, "build repository candidate without network access: %v\n", err)
			os.Exit(3)
		}
	} else {
		candidateKind = "external-binary"
		candidate, err = filepath.Abs(candidate)
		if err != nil {
			fmt.Fprintf(os.Stderr, "resolve candidate: %v\n", err)
			os.Exit(3)
		}
	}
	candidate, candidateDigest, err := freezeCandidate(candidate, workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "freeze candidate: %v\n", err)
		os.Exit(3)
	}

	candidateMetadata, err := inspectCandidate(candidate, candidateKind, candidateDigest, workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inspect candidate: %v\n", err)
		os.Exit(3)
	}
	started := time.Now()
	results := runCases(harness{candidate: candidate, root: workspace})
	report := assembleReport(results, candidateMetadata, baselineDigest, elapsedMilliseconds(time.Since(started)))
	report.Overall.MeetsReviewedBaseline = meetsBaseline(report, reviewed)

	if *proveWeakened {
		if err := validateReport(report, reviewed); err != nil {
			fmt.Fprintf(os.Stderr, "validate weakened report: %v\n", err)
			os.Exit(3)
		}
		if err := writeBoundedJSON(filepath.Join(artifactDir, "weakened-report.json"), report); err != nil {
			fmt.Fprintf(os.Stderr, "write weakened report: %v\n", err)
			os.Exit(3)
		}
		_, afterBody, err := loadBaseline(baselinePath)
		if err != nil || sha256Text(afterBody) != baselineDigest {
			fmt.Fprintln(os.Stderr, "reviewed baseline changed during weakened-candidate proof")
			os.Exit(1)
		}
		if report.Overall.MeetsReviewedBaseline {
			fmt.Fprintln(os.Stderr, "deliberately weakened candidate unexpectedly met the reviewed baseline")
			os.Exit(1)
		}
		fmt.Printf("wip-commit weakened candidate rejected: %d/%d checks passed (score %.3f)\n", report.Overall.PassedChecks, report.Overall.TotalChecks, report.Overall.Score)
		return
	}

	if err := writeRunArtifacts(artifactDir, report, reviewed); err != nil {
		fmt.Fprintf(os.Stderr, "write benchmark artifacts: %v\n", err)
		os.Exit(3)
	}
	fmt.Printf("wip-commit portable benchmark: %d/%d checks passed (score %.3f, baseline %.3f, zero AI)\n", report.Overall.PassedChecks, report.Overall.TotalChecks, report.Overall.Score, reviewed.MinimumScore)
	if !report.Overall.MeetsReviewedBaseline {
		os.Exit(1)
	}
}

func runCases(h harness) []caseResult {
	cases := []func(harness) caseResult{
		caseSetupSharedLinked,
		caseDisjointParallelLanes,
		caseReviewedSplitDefault,
		caseExplicitSingleOptOut,
		caseForeignIndexPreservation,
		caseDryRunNoPublication,
		caseFailedVerificationNoPublication,
		caseReconcileInterruptedPublication,
	}
	results := make([]caseResult, 0, len(cases))
	for _, run := range cases {
		results = append(results, run(h))
	}
	return results
}

func caseSetupSharedLinked(h harness) caseResult {
	started := time.Now()
	shared, sharedErr := newFixture(h.root, "setup-shared", map[string]string{"core.txt": "base\n"})
	linked, linkedErr := newFixture(h.root, "setup-linked", map[string]string{"core.txt": "base\n"})
	anchor, anchorErr := newFixture(h.root, "setup-anchor-negative", map[string]string{"core.txt": "base\n"})
	if sharedErr != nil || linkedErr != nil || anchorErr != nil {
		return failedFixtureCase("setup-shared-linked", started)
	}
	sharedRun := h.initLane(shared, "shared", "shared-setup", "", "core.txt")
	linkedWorktree := filepath.Join(h.root, "setup-linked-worktree")
	linkedRun := h.initLane(linked, "worktree", "linked-setup", linkedWorktree, "core.txt")
	inside, insideErr := runGitAt(linked, linkedWorktree, "rev-parse", "--is-inside-work-tree")
	negative := h.initLane(anchor, "worktree", "anchor-refused", "", "core.txt")
	positivePassed := sharedRun.succeeded() && linkedRun.succeeded() && insideErr == nil && inside == "true"
	negativePassed := negative.failedWith("WORKTREE_REQUIRED")
	return resultCase(
		"setup-shared-linked", started,
		positivePassed, "shared and linked-worktree initialization used shipped entrypoints", "shared or linked-worktree initialization contract was not observed",
		negativePassed, "anchor worktree mode without a destination was refused", "expected WORKTREE_REQUIRED refusal was not observed",
	)
}

func caseDisjointParallelLanes(h harness) caseResult {
	started := time.Now()
	fix, err := newFixture(h.root, "parallel-lanes", map[string]string{"a.txt": "base a\n", "b.txt": "base b\n"})
	if err != nil {
		return failedFixtureCase("disjoint-parallel-lanes", started)
	}
	if writeAndStage(fix, "a.txt", "changed a\n") != nil || writeAndStage(fix, "b.txt", "changed b\n") != nil {
		return failedFixtureCase("disjoint-parallel-lanes", started)
	}
	indexBefore, beforeErr := runGit(fix, "write-tree")
	leftInit := h.initLane(fix, "shared", "parallel-left", "", "a.txt")
	rightInit := h.initLane(fix, "shared", "parallel-right", "", "b.txt")
	overlap := h.initLane(fix, "shared", "parallel-overlap", "", "a.txt")

	var leftCapture, rightCapture wipRun
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		leftCapture = h.commitSingle(fix, "parallel-left", "fix(left): capture disjoint path", false, "a.txt")
	}()
	go func() {
		defer group.Done()
		rightCapture = h.commitSingle(fix, "parallel-right", "fix(right): capture disjoint path", false, "b.txt")
	}()
	group.Wait()
	indexAfter, afterErr := runGit(fix, "write-tree")
	leftBody, leftErr := runGit(fix, "show", "refs/heads/wip/bench/parallel-left:a.txt")
	rightBody, rightErr := runGit(fix, "show", "refs/heads/wip/bench/parallel-right:b.txt")
	positivePassed := beforeErr == nil && afterErr == nil && indexBefore == indexAfter && leftInit.succeeded() && rightInit.succeeded() && leftCapture.succeeded() && rightCapture.succeeded() && leftErr == nil && rightErr == nil && leftBody == "changed a" && rightBody == "changed b"
	negativePassed := overlap.failedWith("PATH_LEASE_CONFLICT")
	return resultCase(
		"disjoint-parallel-lanes", started,
		positivePassed, "two disjoint shared lanes captured concurrently and preserved the source index", "parallel disjoint capture or source-index preservation failed",
		negativePassed, "an overlapping third lease was refused", "expected PATH_LEASE_CONFLICT refusal was not observed",
	)
}

func caseReviewedSplitDefault(h harness) caseResult {
	started := time.Now()
	fix, err := newFixture(h.root, "reviewed-split", map[string]string{"cmd/tool.txt": "base tool\n", "docs/guide.md": "base guide\n"})
	if err != nil {
		return failedFixtureCase("reviewed-split-default", started)
	}
	if writeAndStage(fix, "cmd/tool.txt", "changed tool\n") != nil || writeAndStage(fix, "docs/guide.md", "changed guide\n") != nil {
		return failedFixtureCase("reviewed-split-default", started)
	}
	initialized := h.initLane(fix, "shared", "reviewed-split", "", "cmd/tool.txt", "docs/guide.md")
	planPath := filepath.Join(h.root, "reviewed-split-plan.json")
	planned := h.runWIP(fix, append([]string{"plan"}, append(identityArgs("reviewed-split"), "--out", planPath)...)...)
	state, groupCount, inspectErr := inspectPlan(planPath)
	draftCapture := h.runWIP(fix, append([]string{"capture"}, append(identityArgs("reviewed-split"), "--plan", planPath)...)...)
	reviewErr := reviewPlan(planPath, nil)
	captured := h.runWIP(fix, append([]string{"capture"}, append(identityArgs("reviewed-split"), "--plan", planPath)...)...)
	var data guidedCapture
	decodeErr := decodeEnvelopeData(captured.Envelope, &data)
	positivePassed := initialized.succeeded() && planned.succeeded() && inspectErr == nil && state == "draft" && groupCount >= 2 && reviewErr == nil && captured.succeeded() && decodeErr == nil && data.Capture.RefUpdated && data.Capture.GateOutcome == "passed" && len(data.Capture.Commits) == groupCount && data.Handoff.CaptureOnly && !data.Handoff.LandingAuthorized
	negativePassed := draftCapture.failedWith("PLAN_NOT_REVIEWED")
	return resultCase(
		"reviewed-split-default", started,
		positivePassed, "the draft grouped multiple components and the reviewed plan emitted a capture-only handoff", "reviewed split-plan behavior was not observed",
		negativePassed, "capture refused the unreviewed draft", "expected PLAN_NOT_REVIEWED refusal was not observed",
	)
}

func caseExplicitSingleOptOut(h harness) caseResult {
	started := time.Now()
	fix, err := newFixture(h.root, "single-opt-out", map[string]string{"core.txt": "base\n"})
	if err != nil || writeAndStage(fix, "core.txt", "changed\n") != nil {
		return failedFixtureCase("explicit-single-opt-out", started)
	}
	initialized := h.initLane(fix, "shared", "single-opt-out", "", "core.txt")
	withoutSingle := h.runWIP(fix, append([]string{"commit"}, append(identityArgs("single-opt-out"), "--message", "fix(core): capture one path", "--path", "core.txt", "--non-interactive")...)...)
	withSingle := h.commitSingle(fix, "single-opt-out", "fix(core): capture one path", false, "core.txt")
	var captured directCapture
	decodeErr := decodeEnvelopeData(withSingle.Envelope, &captured)
	positivePassed := initialized.succeeded() && withSingle.succeeded() && decodeErr == nil && captured.RefUpdated && len(captured.Commits) == 1
	negativePassed := withoutSingle.failedWith("SPLIT_PLAN_REQUIRED")
	return resultCase(
		"explicit-single-opt-out", started,
		positivePassed, "--single explicitly selected one compatibility commit", "explicit single-commit capture failed",
		negativePassed, "a message and path without --single were refused", "expected SPLIT_PLAN_REQUIRED refusal was not observed",
	)
}

func caseForeignIndexPreservation(h harness) caseResult {
	started := time.Now()
	fix, err := newFixture(h.root, "foreign-index", map[string]string{"owned.txt": "base owned\n", "foreign.txt": "base foreign\n"})
	if err != nil || writeAndStage(fix, "owned.txt", "changed owned\n") != nil || writeAndStage(fix, "foreign.txt", "changed foreign\n") != nil {
		return failedFixtureCase("foreign-index-preservation", started)
	}
	initialized := h.initLane(fix, "shared", "foreign-index", "", "owned.txt")
	indexBefore, indexBeforeErr := runGit(fix, "write-tree")
	headBefore, headBeforeErr := runGit(fix, "rev-parse", "HEAD")
	foreignBefore, foreignBeforeErr := runGit(fix, "ls-files", "--stage", "--", "foreign.txt")
	foreignAttempt := h.commitSingle(fix, "foreign-index", "fix(foreign): forbidden scope", false, "foreign.txt")
	ownedCapture := h.commitSingle(fix, "foreign-index", "fix(owned): capture exact scope", false, "owned.txt")
	indexAfter, indexAfterErr := runGit(fix, "write-tree")
	headAfter, headAfterErr := runGit(fix, "rev-parse", "HEAD")
	foreignAfter, foreignAfterErr := runGit(fix, "ls-files", "--stage", "--", "foreign.txt")
	ownedBody, ownedErr := runGit(fix, "show", "refs/heads/wip/bench/foreign-index:owned.txt")
	positivePassed := initialized.succeeded() && ownedCapture.succeeded() && indexBeforeErr == nil && indexAfterErr == nil && indexBefore == indexAfter && headBeforeErr == nil && headAfterErr == nil && headBefore == headAfter && foreignBeforeErr == nil && foreignAfterErr == nil && foreignBefore == foreignAfter && ownedErr == nil && ownedBody == "changed owned"
	negativePassed := foreignAttempt.failedWith("LEASE_REQUIRED")
	return resultCase(
		"foreign-index-preservation", started,
		positivePassed, "owned capture preserved source HEAD, the complete index tree, and the foreign stage entry", "source HEAD or index preservation failed",
		negativePassed, "capture of an unleased foreign path was refused", "expected LEASE_REQUIRED refusal was not observed",
	)
}

func caseDryRunNoPublication(h harness) caseResult {
	started := time.Now()
	fix, err := newFixture(h.root, "dry-run", map[string]string{"core.txt": "base\n"})
	if err != nil || writeAndStage(fix, "core.txt", "changed\n") != nil {
		return failedFixtureCase("dry-run-no-publication", started)
	}
	initialized := h.initLane(fix, "shared", "dry-run", "", "core.txt")
	refName := "refs/heads/wip/bench/dry-run"
	refBefore, beforeErr := runGit(fix, "rev-parse", refName)
	dry := h.commitSingle(fix, "dry-run", "fix(core): prove dry run", true, "core.txt")
	refAfterDry, dryRefErr := runGit(fix, "rev-parse", refName)
	var dryData directCapture
	dryDecodeErr := decodeEnvelopeData(dry.Envelope, &dryData)
	actual := h.commitSingle(fix, "dry-run", "fix(core): prove dry run", false, "core.txt")
	refAfterActual, actualRefErr := runGit(fix, "rev-parse", refName)
	var actualData directCapture
	actualDecodeErr := decodeEnvelopeData(actual.Envelope, &actualData)
	positivePassed := initialized.succeeded() && beforeErr == nil && dry.succeeded() && dryDecodeErr == nil && dryData.DryRun && !dryData.RefUpdated && dryRefErr == nil && refBefore == refAfterDry
	negativePassed := actual.succeeded() && actualDecodeErr == nil && actualData.RefUpdated && actualRefErr == nil && refAfterActual != refBefore
	return resultCase(
		"dry-run-no-publication", started,
		positivePassed, "dry-run passed gates without moving the lane ref", "dry-run moved the ref or did not report non-publication",
		negativePassed, "the same plan without --dry-run moved the ref, proving detector sensitivity", "the non-dry sensitivity control did not move the ref",
	)
}

func caseFailedVerificationNoPublication(h harness) caseResult {
	started := time.Now()
	fix, err := newFixture(h.root, "failed-verification", map[string]string{"core.txt": "base\n"})
	if err != nil || writeAndStage(fix, "core.txt", "changed\n") != nil {
		return failedFixtureCase("failed-verification-no-publication", started)
	}
	initialized := h.initLane(fix, "shared", "verify-gate", "", "core.txt")
	planPath := filepath.Join(h.root, "failed-verification-plan.json")
	planned := h.runWIP(fix, append([]string{"plan"}, append(identityArgs("verify-gate"), "--out", planPath)...)...)
	failedPlanErr := reviewPlan(planPath, []string{"git", "--definitely-not-a-real-option"})
	refName := "refs/heads/wip/bench/verify-gate"
	refBefore, beforeErr := runGit(fix, "rev-parse", refName)
	failed := h.runWIP(fix, append([]string{"capture"}, append(identityArgs("verify-gate"), "--plan", planPath)...)...)
	refAfterFailure, failedRefErr := runGit(fix, "rev-parse", refName)
	passingPlanErr := reviewPlan(planPath, []string{"git", "--version"})
	passed := h.runWIP(fix, append([]string{"capture"}, append(identityArgs("verify-gate"), "--plan", planPath)...)...)
	refAfterPass, passedRefErr := runGit(fix, "rev-parse", refName)
	var passedData guidedCapture
	passedDecodeErr := decodeEnvelopeData(passed.Envelope, &passedData)
	positivePassed := initialized.succeeded() && planned.succeeded() && failedPlanErr == nil && beforeErr == nil && failed.failedWith("VERIFY_FAILED") && failedRefErr == nil && refAfterFailure == refBefore
	negativePassed := passingPlanErr == nil && passed.succeeded() && passedDecodeErr == nil && passedData.Capture.RefUpdated && passedRefErr == nil && refAfterPass != refBefore
	return resultCase(
		"failed-verification-no-publication", started,
		positivePassed, "a failing typed verify command left the lane ref unchanged", "failed verification published or did not return VERIFY_FAILED",
		negativePassed, "a passing verify command published, proving gate-outcome sensitivity", "the passing verification sensitivity control did not publish",
	)
}

func caseReconcileInterruptedPublication(h harness) caseResult {
	started := time.Now()
	fix, err := newFixture(h.root, "reconcile", map[string]string{"core.txt": "base\n"})
	if err != nil || writeAndStage(fix, "core.txt", "changed\n") != nil {
		return failedFixtureCase("reconcile-interrupted-publication", started)
	}
	base, baseErr := runGit(fix, "rev-parse", "HEAD")
	initialized := h.initLane(fix, "shared", "reconcile", "", "core.txt")
	captured := h.commitSingle(fix, "reconcile", "fix(core): create recoverable receipt", false, "core.txt")
	var capture directCapture
	decodeErr := decodeEnvelopeData(captured.Envelope, &capture)
	fixtureErr := errors.New("capture receipt unavailable")
	if baseErr == nil && initialized.succeeded() && captured.succeeded() && decodeErr == nil && capture.PlanID != "" && capture.PlanDigest != "" && capture.FinalCommit != "" {
		fixtureErr = reproduceAppliedIntentBoundary(fix, "reconcile", capture.PlanID, base)
	}
	wrongDigest := "sha256:" + strings.Repeat("0", 64)
	wrong := h.runWIP(fix, append([]string{"reconcile"}, append(identityArgs("reconcile"), "--plan-id", capture.PlanID, "--plan-digest", wrongDigest)...)...)
	cursorAfterWrong, cursorWrongErr := readLaneCursor(fix, "reconcile")
	correct := h.runWIP(fix, append([]string{"reconcile"}, append(identityArgs("reconcile"), "--plan-id", capture.PlanID, "--plan-digest", capture.PlanDigest)...)...)
	var first reconcileData
	firstDecodeErr := decodeEnvelopeData(correct.Envelope, &first)
	second := h.runWIP(fix, append([]string{"reconcile"}, append(identityArgs("reconcile"), "--plan-id", capture.PlanID, "--plan-digest", capture.PlanDigest)...)...)
	var repeated reconcileData
	secondDecodeErr := decodeEnvelopeData(second.Envelope, &repeated)
	finalCursor, finalCursorErr := readLaneCursor(fix, "reconcile")
	positivePassed := fixtureErr == nil && correct.succeeded() && firstDecodeErr == nil && first.IntentState == "complete" && !first.AlreadyClean && second.succeeded() && secondDecodeErr == nil && repeated.AlreadyClean && finalCursorErr == nil && finalCursor == capture.FinalCommit
	negativePassed := fixtureErr == nil && wrong.failedWith("INTENT_DIGEST_MISMATCH") && cursorWrongErr == nil && cursorAfterWrong == base
	return resultCase(
		"reconcile-interrupted-publication", started,
		positivePassed, "reconcile completed a reproduced applied-intent boundary and an exact retry was idempotent", "reconciliation or its idempotent retry failed",
		negativePassed, "a wrong immutable receipt digest was refused without advancing the lane cursor", "expected INTENT_DIGEST_MISMATCH refusal was not observed",
	)
}

func (h harness) initLane(f fixture, mode, lane, worktree string, paths ...string) wipRun {
	args := []string{"init", "--mode", mode, "--lane", lane, "--agent", benchmarkAgent, "--session", benchmarkSession, "--non-interactive", "--no-install", "--no-install-skill"}
	if worktree != "" {
		args = append(args, "--create-worktree", worktree)
	}
	for _, path := range paths {
		args = append(args, "--path", path)
	}
	return h.runWIP(f, args...)
}

func (h harness) commitSingle(f fixture, lane, message string, dryRun bool, paths ...string) wipRun {
	args := append([]string{"commit"}, identityArgs(lane)...)
	args = append(args, "--single", "--message", message, "--non-interactive")
	if dryRun {
		args = append(args, "--dry-run")
	}
	for _, path := range paths {
		args = append(args, "--path", path)
	}
	return h.runWIP(f, args...)
}

func (h harness) runWIP(f fixture, args ...string) wipRun {
	all := append([]string{"--json", "--repo-dir", f.repo}, args...)
	raw := runCommand(defaultCommandTimeout, f.repo, isolatedEnvironment(f.home), h.candidate, all...)
	result := wipRun{Raw: raw}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw.Stdout)), &result.Envelope); err == nil {
		result.Decoded = true
	}
	return result
}

func identityArgs(lane string) []string {
	return []string{"--lane", lane, "--agent", benchmarkAgent, "--session", benchmarkSession}
}

func resultCase(id string, started time.Time, positive bool, positivePass, positiveFail string, negative bool, negativePass, negativeFail string) caseResult {
	positiveDetail := positiveFail
	if positive {
		positiveDetail = positivePass
	}
	negativeDetail := negativeFail
	if negative {
		negativeDetail = negativePass
	}
	return caseResult{
		ID: id, Passed: positive && negative,
		Positive:        assertion{Passed: positive, Detail: positiveDetail},
		NegativeControl: assertion{Passed: negative, Detail: negativeDetail},
		ElapsedMS:       elapsedMilliseconds(time.Since(started)),
	}
}

func failedFixtureCase(id string, started time.Time) caseResult {
	return resultCase(id, started, false, "", "disposable fixture setup failed", false, "", "negative control could not run because fixture setup failed")
}

func newFixture(root, id string, files map[string]string) (fixture, error) {
	base := filepath.Join(root, id)
	repo := filepath.Join(base, "repo")
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return fixture{}, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fixture{}, err
	}
	fix := fixture{repo: repo, home: home}
	if _, err := runGit(fix, "init", "-b", "main"); err != nil {
		return fixture{}, err
	}
	for _, config := range [][2]string{
		{"user.name", "WIP Benchmark"},
		{"user.email", "wip-benchmark@example.invalid"},
		{"commit.gpgsign", "false"},
		{"core.autocrlf", "false"},
	} {
		if _, err := runGit(fix, "config", config[0], config[1]); err != nil {
			return fixture{}, err
		}
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := writeFixtureFile(fix.repo, path, files[path]); err != nil {
			return fixture{}, err
		}
	}
	if _, err := runGit(fix, "add", "--", "."); err != nil {
		return fixture{}, err
	}
	if _, err := runGit(fix, "commit", "--no-gpg-sign", "-m", "test: create disposable benchmark fixture"); err != nil {
		return fixture{}, err
	}
	return fix, nil
}

func writeAndStage(f fixture, path, body string) error {
	if err := writeFixtureFile(f.repo, path, body); err != nil {
		return err
	}
	_, err := runGit(f, "add", "--", filepath.ToSlash(path))
	return err
}

func writeFixtureFile(root, name, body string) error {
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o600)
}

func runGit(f fixture, args ...string) (string, error) {
	return runGitAt(f, f.repo, args...)
}

func runGitAt(f fixture, directory string, args ...string) (string, error) {
	all := append([]string{"-C", directory}, args...)
	result := runCommand(defaultCommandTimeout, directory, isolatedEnvironment(f.home), "git", all...)
	if result.ExitCode != 0 || result.TimedOut || result.Err != nil {
		return "", fmt.Errorf("git command failed")
	}
	return strings.TrimSpace(result.Stdout), nil
}

func inspectPlan(path string) (string, int, error) {
	var plan map[string]any
	if err := readBoundedJSON(path, &plan); err != nil {
		return "", 0, err
	}
	state, _ := plan["state"].(string)
	groups, ok := plan["groups"].([]any)
	if !ok || len(groups) == 0 {
		return state, 0, errors.New("plan groups missing")
	}
	return state, len(groups), nil
}

func reviewPlan(path string, verifyArgv []string) error {
	var plan map[string]any
	if err := readBoundedJSON(path, &plan); err != nil {
		return err
	}
	groups, ok := plan["groups"].([]any)
	if !ok || len(groups) == 0 {
		return errors.New("plan groups missing")
	}
	for index, raw := range groups {
		group, ok := raw.(map[string]any)
		if !ok {
			return errors.New("plan group is invalid")
		}
		group["message"] = fmt.Sprintf("chore(benchmark): capture reviewed group %d", index+1)
		delete(group, "verify")
	}
	if len(verifyArgv) > 0 {
		first := groups[0].(map[string]any)
		first["verify"] = []any{map[string]any{"argv": verifyArgv, "timeout_ms": float64(30_000)}}
	}
	plan["state"] = "reviewed"
	return writeBoundedJSON(path, plan)
}

func reproduceAppliedIntentBoundary(f fixture, lane, planID, base string) error {
	common, err := runGit(f, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(f.repo, common)
	}
	stateRoot := filepath.Join(filepath.Clean(common), "wip", "v1")
	lanePath := filepath.Join(stateRoot, "lanes", lane+".json")
	intentPath := filepath.Join(stateRoot, "intents", planID+".json")
	if err := mutateJSONObject(lanePath, func(value map[string]any) error {
		value["current_sha"] = base
		delete(value, "last_commit")
		return nil
	}); err != nil {
		return err
	}
	return mutateJSONObject(intentPath, func(value map[string]any) error {
		value["state"] = "applied"
		return nil
	})
}

func readLaneCursor(f fixture, lane string) (string, error) {
	common, err := runGit(f, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(f.repo, common)
	}
	var value map[string]any
	if err := readBoundedJSON(filepath.Join(filepath.Clean(common), "wip", "v1", "lanes", lane+".json"), &value); err != nil {
		return "", err
	}
	cursor, ok := value["current_sha"].(string)
	if !ok || cursor == "" {
		return "", errors.New("lane cursor missing")
	}
	return cursor, nil
}

func mutateJSONObject(path string, mutate func(map[string]any) error) error {
	var value map[string]any
	if err := readBoundedJSON(path, &value); err != nil {
		return err
	}
	if err := mutate(value); err != nil {
		return err
	}
	return writeBoundedJSON(path, value)
}

func decodeEnvelopeData(value envelope, target any) error {
	if len(value.Data) == 0 {
		return errors.New("envelope data missing")
	}
	return json.Unmarshal(value.Data, target)
}

func assembleReport(cases []caseResult, candidate candidateInfo, baselineDigest string, elapsedMS int64) report {
	positivePassed, negativePassed := 0, 0
	for _, result := range cases {
		if result.Positive.Passed {
			positivePassed++
		}
		if result.NegativeControl.Passed {
			negativePassed++
		}
	}
	passed := positivePassed + negativePassed
	total := len(cases) * 2
	score := 0.0
	if total > 0 {
		score = float64(passed) / float64(total)
	}
	return report{
		SchemaVersion: reportSchemaVersion, Benchmark: benchmarkName, BenchmarkVersion: benchmarkVersion,
		Platform: runtime.GOOS + "/" + runtime.GOARCH, Candidate: candidate, BaselineSHA256: baselineDigest,
		Cases:     cases,
		Overall:   overallResult{TotalChecks: total, PassedChecks: passed, FailedChecks: total - passed, PassedPositiveChecks: positivePassed, PassedNegativeControls: negativePassed, Score: score},
		ElapsedMS: elapsedMS, AIUsage: aiUsage{}, ZeroAI: true,
	}
}

func meetsBaseline(current report, reviewed baseline) bool {
	if current.SchemaVersion != reportSchemaVersion || current.Benchmark != reviewed.Benchmark || current.BenchmarkVersion != reviewed.BenchmarkVersion || reviewed.ReviewStatus != "reviewed" {
		return false
	}
	if reviewed.ZeroAIRequired && (!current.ZeroAI || current.AIUsage.Calls != 0 || current.AIUsage.Tokens != 0) {
		return false
	}
	if current.Overall.Score < reviewed.MinimumScore || current.Overall.PassedPositiveChecks < reviewed.RequiredPositiveChecks || current.Overall.PassedNegativeControls < reviewed.RequiredNegativeControls {
		return false
	}
	byID := make(map[string]caseResult, len(current.Cases))
	for _, result := range current.Cases {
		if _, exists := byID[result.ID]; exists {
			return false
		}
		byID[result.ID] = result
	}
	for _, id := range reviewed.RequiredCases {
		result, exists := byID[id]
		if !exists || !result.Passed || !result.Positive.Passed || !result.NegativeControl.Passed {
			return false
		}
	}
	return true
}

func loadBaseline(path string) (baseline, []byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return baseline{}, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return baseline{}, nil, errors.New("baseline is not a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return baseline{}, nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return baseline{}, nil, errors.New("baseline changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxJSONArtifactBytes+1))
	if err != nil {
		return baseline{}, nil, err
	}
	if len(body) == 0 || len(body) > maxJSONArtifactBytes {
		return baseline{}, nil, errors.New("baseline size is invalid")
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, current) {
		return baseline{}, nil, errors.New("baseline changed while reading")
	}
	var value baseline
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return baseline{}, nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return baseline{}, nil, errors.New("baseline contains trailing JSON data")
	}
	if value.SchemaVersion != reportSchemaVersion || value.Benchmark != benchmarkName || value.BenchmarkVersion != benchmarkVersion || value.ReviewStatus != "reviewed" || value.MinimumScore != 1 || !value.ZeroAIRequired {
		return baseline{}, nil, errors.New("baseline identity or review policy is invalid")
	}
	if value.RequiredPositiveChecks != len(requiredCaseIDs) || value.RequiredNegativeControls != len(requiredCaseIDs) || !sameStrings(value.RequiredCases, requiredCaseIDs) {
		return baseline{}, nil, errors.New("baseline case coverage is invalid")
	}
	return value, body, nil
}

func sameStrings(left, right []string) bool {
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

func writeRunArtifacts(directory string, current report, reviewed baseline) error {
	if err := validateReport(current, reviewed); err != nil {
		return err
	}
	if err := writeBoundedJSON(filepath.Join(directory, "latest-report.json"), current); err != nil {
		return err
	}
	proposalPath := filepath.Join(directory, "proposal.json")
	if current.Overall.MeetsReviewedBaseline {
		if err := os.Remove(proposalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	failed := make([]string, 0)
	for _, result := range current.Cases {
		if !result.Passed {
			failed = append(failed, result.ID)
		}
	}
	return writeBoundedJSON(proposalPath, proposal{
		SchemaVersion: reportSchemaVersion, Benchmark: benchmarkName, BenchmarkVersion: benchmarkVersion,
		BaselineScore: reviewed.MinimumScore, CandidateScore: current.Overall.Score, FailedCaseIDs: failed,
		Action: "Repair the candidate and rerun every case. Do not change the reviewed baseline to admit a regression.",
	})
}

func validateReport(current report, reviewed baseline) error {
	if current.SchemaVersion != reportSchemaVersion || current.Benchmark != benchmarkName || current.BenchmarkVersion != benchmarkVersion {
		return errors.New("report identity is invalid")
	}
	if current.Candidate.Kind == "" || !validSHA256(current.Candidate.SHA256) || !validSHA256(current.BaselineSHA256) {
		return errors.New("report candidate or baseline identity is invalid")
	}
	if current.ElapsedMS < 1 || current.AIUsage.Calls != 0 || current.AIUsage.Tokens != 0 || !current.ZeroAI {
		return errors.New("report elapsed-time or zero-AI contract is invalid")
	}
	if len(current.Cases) != len(requiredCaseIDs) || current.Overall.TotalChecks != len(requiredCaseIDs)*2 {
		return errors.New("report case coverage is invalid")
	}
	positivePassed, negativePassed := 0, 0
	for index, result := range current.Cases {
		if result.ID != requiredCaseIDs[index] || result.ElapsedMS < 1 {
			return errors.New("report case identity or elapsed time is invalid")
		}
		if result.Passed != (result.Positive.Passed && result.NegativeControl.Passed) {
			return errors.New("report case outcome is inconsistent")
		}
		if result.Positive.Passed {
			positivePassed++
		}
		if result.NegativeControl.Passed {
			negativePassed++
		}
		for _, detail := range []string{result.Positive.Detail, result.NegativeControl.Detail} {
			if detail == "" || len(detail) > 512 || strings.ContainsAny(detail, "\r\n/\\") {
				return errors.New("report detail is empty, oversized, or path-like")
			}
		}
	}
	passed := positivePassed + negativePassed
	total := len(requiredCaseIDs) * 2
	expectedScore := float64(passed) / float64(total)
	if current.Overall.PassedChecks != passed || current.Overall.FailedChecks != total-passed || current.Overall.PassedPositiveChecks != positivePassed || current.Overall.PassedNegativeControls != negativePassed || current.Overall.Score != expectedScore {
		return errors.New("report aggregate outcome is inconsistent")
	}
	if current.Overall.MeetsReviewedBaseline != meetsBaseline(current, reviewed) {
		return errors.New("report baseline outcome is inconsistent")
	}
	body, err := json.Marshal(current)
	if err != nil {
		return err
	}
	if len(body) > maxJSONArtifactBytes {
		return errors.New("report exceeds benchmark limit")
	}
	return nil
}

func validSHA256(value string) bool {
	encoded, found := strings.CutPrefix(value, "sha256:")
	if !found || len(encoded) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func resolveArtifactDir(flagValue string) (string, error) {
	if strings.TrimSpace(flagValue) != "" {
		return filepath.Abs(flagValue)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".nicos-dev", "benchmarks", "wip-commit"), nil
}

func prepareArtifactDir(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("artifact path is not a regular directory")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.MkdirAll(path, 0o700)
}

func readBoundedJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, maxJSONArtifactBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxJSONArtifactBytes {
		return errors.New("JSON input exceeds benchmark limit")
	}
	return json.Unmarshal(body, target)
}

func writeBoundedJSON(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > maxJSONArtifactBytes {
		return errors.New("JSON artifact exceeds benchmark limit")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".wip-benchmark-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func repositoryRoot() (string, error) {
	result := runCommand(10*time.Second, "", os.Environ(), "git", "rev-parse", "--show-toplevel")
	if result.ExitCode != 0 || result.TimedOut || result.Err != nil {
		return "", errors.New("current directory is not a Git repository")
	}
	root := strings.TrimSpace(result.Stdout)
	if root == "" {
		return "", errors.New("git returned an empty repository root")
	}
	return filepath.Abs(root)
}

func buildGoCandidate(root, output, source string) error {
	environment := withoutEnvironment(os.Environ(), "GOFLAGS", "GONOPROXY", "GONOSUMDB", "GOPRIVATE", "GOPROXY", "GOSUMDB", "GOTOOLCHAIN", "GOVCS", "GOWORK")
	environment = append(environment, "GOFLAGS=", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off")
	result := runCommand(2*time.Minute, root, environment, "go", "build", "-trimpath", "-ldflags=-buildid=", "-o", output, source)
	if result.ExitCode != 0 || result.TimedOut || result.Err != nil {
		return errors.New("go build failed; prefetch modules before running the offline benchmark")
	}
	return nil
}

func freezeCandidate(source, workspace string) (string, string, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("candidate is not a regular non-symlink file")
	}
	if info.Size() <= 0 || info.Size() > maxCandidateBytes {
		return "", "", errors.New("candidate size is outside the benchmark limit")
	}

	input, err := os.Open(source)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = input.Close() }()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return "", "", errors.New("candidate changed while opening")
	}

	destinationPath := filepath.Join(workspace, executableName("wip-evaluated"))
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return "", "", err
	}
	keep := false
	defer func() {
		_ = destination.Close()
		if !keep {
			_ = os.Remove(destinationPath)
		}
	}()

	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(destination, digest), io.LimitReader(input, maxCandidateBytes+1))
	if err != nil {
		return "", "", err
	}
	if written <= 0 || written > maxCandidateBytes {
		return "", "", errors.New("candidate size is outside the benchmark limit")
	}
	if err := destination.Sync(); err != nil {
		return "", "", err
	}
	if err := destination.Close(); err != nil {
		return "", "", err
	}
	if err := os.Chmod(destinationPath, 0o700); err != nil {
		return "", "", err
	}
	keep = true
	return destinationPath, "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func inspectCandidate(path, kind, digest, workspace string) (candidateInfo, error) {
	metadata := candidateInfo{Kind: kind, SHA256: digest}
	home := filepath.Join(workspace, "candidate-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return candidateInfo{}, err
	}
	raw := runCommand(10*time.Second, workspace, isolatedEnvironment(home), path, "--json", "version")
	if raw.ExitCode != 0 {
		return metadata, nil
	}
	var value envelope
	if json.Unmarshal([]byte(strings.TrimSpace(raw.Stdout)), &value) != nil || !value.OK {
		return metadata, nil
	}
	var version struct {
		Version     string `json:"version"`
		Revision    string `json:"revision"`
		SourceState string `json:"source_state"`
	}
	if json.Unmarshal(value.Data, &version) == nil {
		metadata.Version = safeMetadata(version.Version)
		metadata.Revision = safeMetadata(version.Revision)
		metadata.SourceState = safeMetadata(version.SourceState)
	}
	return metadata, nil
}

func safeMetadata(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 || strings.ContainsAny(value, "\r\n/\\") {
		return ""
	}
	return value
}

func isolatedEnvironment(home string) []string {
	allowed := map[string]bool{
		"COMSPEC":    true,
		"PATH":       true,
		"PATHEXT":    true,
		"SYSTEMROOT": true,
		"WINDIR":     true,
	}
	filtered := make([]string, 0, len(allowed)+11)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if allowed[upper] {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered,
		"HOME="+home,
		"USERPROFILE="+home,
		"TEMP="+home,
		"TMP="+home,
		"TMPDIR="+home,
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func withoutEnvironment(environment []string, names ...string) []string {
	blocked := make(map[string]bool, len(names))
	for _, name := range names {
		blocked[strings.ToUpper(name)] = true
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if !blocked[strings.ToUpper(name)] {
			result = append(result, entry)
		}
	}
	return result
}

type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (buffer *cappedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = buffer.buffer.Write(value)
	}
	return original, nil
}

func (buffer *cappedBuffer) String() string { return buffer.buffer.String() }

func runCommand(timeout time.Duration, directory string, environment []string, executable string, args ...string) rawCommand {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, args...)
	if directory != "" {
		command.Dir = directory
	}
	command.Env = environment
	stdout := &cappedBuffer{limit: maxCommandOutputBytes}
	stderr := &cappedBuffer{limit: maxCommandOutputBytes}
	command.Stdout, command.Stderr = stdout, stderr
	err := command.Run()
	result := rawCommand{ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String(), TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded), Err: err}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		result.Err = nil
		return result
	}
	result.ExitCode = -1
	return result
}

func elapsedMilliseconds(duration time.Duration) int64 {
	value := duration.Milliseconds()
	if value < 1 {
		return 1
	}
	return value
}

func executableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func sha256Text(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}
