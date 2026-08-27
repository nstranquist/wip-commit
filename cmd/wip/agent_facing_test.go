package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nstranquist/wip-commit/internal/engine"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/store"
)

func TestLaneJSONAlwaysSerializesLeaseExpiry(t *testing.T) {
	for name, value := range map[string]any{
		"status":  operationalStatus{},
		"context": laneContext{},
	} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"lease_expires_at":null`) {
			t.Fatalf("%s omitted lease expiry: %s", name, body)
		}
	}
}

func TestHandoffReportsNotReadyForUnmovedLane(t *testing.T) {
	directory := cliTestRepo(t)
	initializeCLI(t, directory, "empty-handoff", "core.txt")

	result := runCLI(t, []string{"--json", "--repo-dir", directory, "handoff", "--lane", "empty-handoff"}, "")
	if result.code == 0 || result.envelope.Error == nil || result.envelope.Error.Code != "HANDOFF_NOT_READY" {
		t.Fatalf("empty handoff = %#v stderr=%s", result.envelope, result.stderr)
	}
}

func TestReviewedPlanCaptureProducesBoundPrivateHandoff(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	writeCLI(t, directory, "docs.txt", "new docs\n")
	cliGit(t, directory, "add", "core.txt", "docs.txt")
	head := cliGit(t, directory, "rev-parse", "HEAD")
	index := cliGitRaw(t, directory, "ls-files", "-s", "-z")
	initializeCLI(t, directory, "guided-capture", "core.txt", "docs.txt")

	planPath := filepath.Join(t.TempDir(), "capture-plan.json")
	drafted := runCLI(t, []string{"--json", "--repo-dir", directory, "plan", "--lane", "guided-capture", "--out", planPath}, "")
	if drafted.code != 0 || !drafted.envelope.OK {
		t.Fatalf("draft plan: %#v stderr=%s", drafted.envelope, drafted.stderr)
	}
	var plan planProposal
	decodeData(t, drafted.envelope.Data, &plan)
	if plan.State != "draft" || plan.SchemaVersion != reviewPlanSchemaVersion || len(plan.Groups) != 2 {
		t.Fatalf("draft = %#v", plan)
	}
	plan.State = "reviewed"
	for index := range plan.Groups {
		if strings.Contains(plan.Groups[index].SuggestedScope, "core") {
			plan.Groups[index].Message = "fix(core): preserve reviewed capture"
		} else {
			plan.Groups[index].Message = "docs(guide): document reviewed capture"
		}
	}
	body, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	reviewDigest, err := reviewPlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}

	captured := runCLI(t, []string{"--json", "--repo-dir", directory, "capture", "--lane", "guided-capture", "--plan", planPath, "--review-plan-digest", reviewDigest}, "")
	if captured.code != 0 || !captured.envelope.OK {
		t.Fatalf("capture: %#v stderr=%s", captured.envelope, captured.stderr)
	}
	var result guidedCaptureResult
	decodeData(t, captured.envelope.Data, &result)
	if len(result.Handoff.Segments) != 1 {
		t.Fatalf("handoff segments = %#v", result.Handoff.Segments)
	}
	segment := result.Handoff.Segments[0]
	if !result.Handoff.CaptureOnly || result.Handoff.LandingAuthorized || !result.Handoff.ReviewEligible || result.Handoff.HandoffDigest == "" || !segment.MessagePolicyBound || segment.ReviewedPlanDigest != reviewDigest || segment.IntentState != "complete" {
		t.Fatalf("handoff = %#v", result.Handoff)
	}
	if result.Handoff.LaneTip != result.Capture.FinalCommit || result.Handoff.FinalTree != result.Capture.FinalTree || len(segment.Commits) != 2 {
		t.Fatalf("capture/handoff mismatch = %#v", result)
	}
	if result.Capture.SourceDigest != plan.SourceDigest {
		t.Fatalf("kernel source digest = %q, reviewed digest = %q", result.Capture.SourceDigest, plan.SourceDigest)
	}
	encoded, _ := json.Marshal(result.Handoff)
	for _, forbidden := range []string{"\"agent\"", "\"session\"", "\"worktree\"", "remote", "new core"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("handoff leaked %q: %s", forbidden, encoded)
		}
	}
	if got := cliGit(t, directory, "rev-parse", "HEAD"); got != head {
		t.Fatalf("source HEAD moved: %s", got)
	}
	if got := cliGitRaw(t, directory, "ls-files", "-s", "-z"); got != index {
		t.Fatal("source index changed")
	}

	printed := runCLI(t, []string{"--json", "--repo-dir", directory, "handoff", "--lane", "guided-capture"}, "")
	if printed.code != 0 || !printed.envelope.OK {
		t.Fatalf("handoff command: %#v stderr=%s", printed.envelope, printed.stderr)
	}
	if printed.envelope.Context == nil || !printed.envelope.Context.LeaseRenewed || printed.envelope.Context.LeaseExpiresAt == nil {
		t.Fatalf("active handoff did not renew the lease: %#v", printed.envelope.Context)
	}
	var receipt captureHandoff
	decodeData(t, printed.envelope.Data, &receipt)
	if receipt.HandoffDigest != result.Handoff.HandoffDigest || receipt.TipCaptureIntentDigest != result.Handoff.TipCaptureIntentDigest || receipt.LaneTip != result.Handoff.LaneTip {
		t.Fatalf("handoff changed = %#v", receipt)
	}
}

func TestCaptureReviewDigestRejectsSameCountPlanSubstitutionBeforeMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX Git hook; Windows runtime coverage is a hosted gate")
	}
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	cliGit(t, directory, "add", "core.txt")
	initializeCLI(t, directory, "review-digest", "core.txt")

	planPath := filepath.Join(t.TempDir(), "reviewed-plan.json")
	drafted := runCLI(t, []string{"--json", "--repo-dir", directory, "plan", "--lane", "review-digest", "--out", planPath}, "")
	if drafted.code != 0 {
		t.Fatalf("draft plan = %#v stderr=%s", drafted.envelope, drafted.stderr)
	}
	var plan planProposal
	decodeData(t, drafted.envelope.Data, &plan)
	plan.State = "reviewed"
	plan.Groups[0].Message = "fix(core): bind the exact reviewed plan"
	plan.Groups[0].Verify = []engine.VerifyCommand{{Argv: []string{"git", "--version"}}}
	reviewDigest, err := reviewPlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}

	verifyMarker := filepath.Join(t.TempDir(), "verify-ran")
	hookMarker := filepath.Join(t.TempDir(), "hook-ran")
	t.Setenv("WIP_CAPTURE_VERIFY_MARKER", verifyMarker)
	plan.Groups[0].Verify = []engine.VerifyCommand{{Argv: []string{os.Args[0], "-test.run=^TestCapturePlanDigestVerifyMarkerHelper$", "-test.count=1"}}}
	body, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	hook := cliGit(t, directory, "rev-parse", "--git-path", "hooks/pre-commit")
	if !filepath.IsAbs(hook) {
		hook = filepath.Join(directory, hook)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n: > "+quoteCommandArg(hookMarker)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore := inspectCLIStore(t, directory)
	statusBefore, err := laneStore.Status("review-digest")
	if err != nil {
		t.Fatal(err)
	}
	statePaths := []string{
		filepath.Join(repo.CommonDir, "wip-coordination.lock"),
		filepath.Join(laneStore.Root, "locks", "lane-review-digest.lock"),
		filepath.Join(laneStore.Root, "locks", "leases.lock"),
		filepath.Join(laneStore.Root, "lanes", "review-digest.json"),
	}
	for _, lease := range statusBefore.Leases {
		statePaths = append(statePaths, filepath.Join(laneStore.Root, "leases", lease.ID+".json"))
	}
	stateBefore := make(map[string][]byte, len(statePaths))
	for _, path := range statePaths {
		stateBefore[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatalf("read preflight state %s: %v", path, err)
		}
	}
	refBefore := cliGit(t, directory, "rev-parse", "refs/heads/wip/agent/review-digest")
	indexBefore := cliGitRaw(t, directory, "ls-files", "-s", "-z")

	rejected := runCLI(t, []string{
		"--json", "--repo-dir", directory, "capture", "--lane", "review-digest", "--plan", planPath,
		"--review-plan-digest", reviewDigest,
	}, "")
	if rejected.code == 0 || rejected.envelope.Error == nil || rejected.envelope.Error.Code != "REVIEW_PLAN_DIGEST_MISMATCH" {
		t.Fatalf("substituted plan result = %#v stderr=%s", rejected.envelope, rejected.stderr)
	}
	statusAfter, err := laneStore.Status("review-digest")
	if err != nil || !reflect.DeepEqual(statusBefore, statusAfter) {
		t.Fatalf("digest refusal changed lane or lease timestamps:\nbefore=%#v\nafter=%#v\nerr=%v", statusBefore, statusAfter, err)
	}
	for path, before := range stateBefore {
		after, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(before, after) {
			t.Fatalf("digest refusal changed %s: before=%q after=%q err=%v", path, before, after, readErr)
		}
	}
	for _, marker := range []string{hookMarker, verifyMarker} {
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			t.Fatalf("digest refusal executed marker %s: %v", marker, err)
		}
	}
	if got := cliGit(t, directory, "rev-parse", "refs/heads/wip/agent/review-digest"); got != refBefore {
		t.Fatalf("digest refusal moved lane ref: before=%s after=%s", refBefore, got)
	}
	if got := cliGitRaw(t, directory, "ls-files", "-s", "-z"); got != indexBefore {
		t.Fatal("digest refusal changed the caller index")
	}
}

func TestCommitDryRunIsNoExecAndBytePure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses POSIX hooks; hosted Windows tests cover the no-exec verify path")
	}
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	cliGit(t, directory, "add", "core.txt")
	initializeCLI(t, directory, "dry-run-no-exec", "core.txt")

	planPath := filepath.Join(t.TempDir(), "dry-run-plan.json")
	drafted := runCLI(t, []string{"--json", "--repo-dir", directory, "plan", "--lane", "dry-run-no-exec", "--out", planPath}, "")
	if drafted.code != 0 || !drafted.envelope.OK {
		t.Fatalf("draft plan = %#v stderr=%s", drafted.envelope, drafted.stderr)
	}
	var plan planProposal
	decodeData(t, drafted.envelope.Data, &plan)
	plan.State = "reviewed"
	plan.Groups[0].Message = "fix(core): prove byte-pure no-exec preview"
	verifyMarker := filepath.Join(t.TempDir(), "verify-ran")
	preHookMarker := filepath.Join(t.TempDir(), "pre-commit-ran")
	postHookMarker := filepath.Join(t.TempDir(), "post-commit-ran")
	t.Setenv("WIP_CAPTURE_VERIFY_MARKER", verifyMarker)
	t.Setenv("WIP_DRY_RUN_PRE_HOOK_MARKER", preHookMarker)
	t.Setenv("WIP_DRY_RUN_POST_HOOK_MARKER", postHookMarker)
	plan.Groups[0].Verify = []engine.VerifyCommand{{Argv: []string{os.Args[0], "-test.run=^TestCapturePlanDigestVerifyMarkerHelper$", "-test.count=1"}}}
	body, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	reviewDigest, err := reviewPlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}

	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	hookBodies := map[string][]byte{
		"pre-commit":  []byte("#!/bin/sh\n: > \"$WIP_DRY_RUN_PRE_HOOK_MARKER\"\n"),
		"post-commit": []byte("#!/bin/sh\n: > \"$WIP_DRY_RUN_POST_HOOK_MARKER\"\n"),
	}
	for name, hookBody := range hookBodies {
		hook := cliGit(t, directory, "rev-parse", "--git-path", "hooks/"+name)
		if !filepath.IsAbs(hook) {
			hook = filepath.Join(directory, hook)
		}
		if err := os.WriteFile(hook, hookBody, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	laneStore := inspectCLIStore(t, directory)
	statusBefore, err := laneStore.Status("dry-run-no-exec")
	if err != nil {
		t.Fatal(err)
	}
	repositoryBefore := commandTreeDigest(t, directory)
	commonBefore := commandTreeDigest(t, repo.CommonDir)
	headBefore := cliGit(t, directory, "rev-parse", "HEAD")
	refBefore := cliGit(t, directory, "rev-parse", "refs/heads/wip/agent/dry-run-no-exec")
	indexBefore := cliGitRaw(t, directory, "ls-files", "-s", "-z")
	configPath := filepath.Join(repo.CommonDir, "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	args := []string{"--repo-dir", directory, "commit", "--lane", "dry-run-no-exec", "--dry-run", "--plan", planPath}
	preview := runCLI(t, append([]string{"--json"}, args...), "")
	if preview.code != 0 || !preview.envelope.OK {
		t.Fatalf("dry-run = %#v stderr=%s", preview.envelope, preview.stderr)
	}
	var result engine.Result
	decodeData(t, preview.envelope.Data, &result)
	wantCount := engine.ExecutionCount{Planned: 1, Executed: 0, Skipped: 1}
	if result.SchemaVersion != engine.ResultSchemaVersion || !result.DryRun || result.GateOutcome != "skipped" || result.RefUpdated || result.FinalCommit != "" || result.IntentState != "" {
		t.Fatalf("dry-run result = %#v", result)
	}
	if result.Execution == nil || result.Execution.Policy != "no-exec" || result.Execution.PreCommitHooks != wantCount || result.Execution.PostCommitHooks != wantCount || result.Execution.VerifyCommands != wantCount {
		t.Fatalf("dry-run execution report = %#v", result.Execution)
	}
	if preview.envelope.Context == nil || preview.envelope.Context.LeaseRenewed {
		t.Fatalf("dry-run claimed lease renewal = %#v", preview.envelope.Context)
	}
	human := runCLI(t, args, "")
	if human.code != 0 || strings.Contains(human.stdout, "passed") || !strings.Contains(human.stdout, "skipped 2 repository hook invocation(s) and 1 verify command(s)") {
		t.Fatalf("dry-run human output = %q stderr=%q", human.stdout, human.stderr)
	}

	rejected := runCLI(t, []string{
		"--json", "--repo-dir", directory, "capture", "--lane", "dry-run-no-exec", "--dry-run", "--plan", planPath,
		"--review-plan-digest", reviewDigest,
	}, "")
	if rejected.code == 0 || rejected.envelope.Error == nil || rejected.envelope.Error.Code != "REVIEWED_PLAN_REQUIRED" || rejected.envelope.Context != nil || rejected.envelope.Data != nil {
		t.Fatalf("guided dry-run refusal = %#v stderr=%s", rejected.envelope, rejected.stderr)
	}

	statusAfter, err := laneStore.Status("dry-run-no-exec")
	if err != nil || !reflect.DeepEqual(statusBefore, statusAfter) {
		t.Fatalf("dry-run changed lane or lease timestamps:\nbefore=%#v\nafter=%#v\nerr=%v", statusBefore, statusAfter, err)
	}
	for _, marker := range []string{preHookMarker, postHookMarker, verifyMarker} {
		if _, statErr := os.Lstat(marker); !os.IsNotExist(statErr) {
			t.Fatalf("dry-run executed marker %s: %v", marker, statErr)
		}
	}
	if got := commandTreeDigest(t, directory); got != repositoryBefore {
		t.Fatalf("dry-run changed repository bytes: before=%s after=%s", repositoryBefore, got)
	}
	if got := commandTreeDigest(t, repo.CommonDir); got != commonBefore {
		t.Fatalf("dry-run changed common-directory bytes: before=%s after=%s", commonBefore, got)
	}
	if got := cliGit(t, directory, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("dry-run moved HEAD: before=%s after=%s", headBefore, got)
	}
	if got := cliGit(t, directory, "rev-parse", "refs/heads/wip/agent/dry-run-no-exec"); got != refBefore {
		t.Fatalf("dry-run moved lane ref: before=%s after=%s", refBefore, got)
	}
	if got := cliGitRaw(t, directory, "ls-files", "-s", "-z"); got != indexBefore {
		t.Fatal("dry-run changed the caller index")
	}
	if got, readErr := os.ReadFile(configPath); readErr != nil || !bytes.Equal(got, configBefore) {
		t.Fatalf("dry-run changed repository config: before=%q after=%q err=%v", configBefore, got, readErr)
	}
	for name, want := range hookBodies {
		hook := cliGit(t, directory, "rev-parse", "--git-path", "hooks/"+name)
		if !filepath.IsAbs(hook) {
			hook = filepath.Join(directory, hook)
		}
		if got, readErr := os.ReadFile(hook); readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("dry-run changed %s hook bytes: before=%q after=%q err=%v", name, want, got, readErr)
		}
	}
}

func TestCapturePlanDigestVerifyMarkerHelper(t *testing.T) {
	marker := os.Getenv("WIP_CAPTURE_VERIFY_MARKER")
	if marker == "" {
		return
	}
	if err := os.WriteFile(marker, []byte("verify ran\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReviewedWorktreeSourceCapturesOwnedUnstagedAndUntrackedPathsWithoutCallerIndexMutation(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "work/core.txt", "base work\n")
	cliGit(t, directory, "add", "work/core.txt")
	cliGit(t, directory, "commit", "-m", "test: add work fixture")

	writeCLI(t, directory, "work/core.txt", "owned tracked\n")
	writeCLI(t, directory, "work/new.txt", "owned untracked\n")
	writeCLI(t, directory, "foreign.txt", "foreign staged\n")
	cliGit(t, directory, "add", "foreign.txt")
	head := cliGit(t, directory, "rev-parse", "HEAD")
	indexBefore := cliGitRaw(t, directory, "ls-files", "-s", "-z")
	initializeCLI(t, directory, "worktree-source", "work")

	planPath := filepath.Join(t.TempDir(), "worktree-plan.json")
	drafted := runCLI(t, []string{
		"--json", "--repo-dir", directory, "plan", "--lane", "worktree-source",
		"--source", "worktree_snapshot", "--untracked", "include", "--path", "work", "--out", planPath,
	}, "")
	if drafted.code != 0 || !drafted.envelope.OK {
		t.Fatalf("draft worktree plan: %#v stderr=%s", drafted.envelope, drafted.stderr)
	}
	var plan planProposal
	decodeData(t, drafted.envelope.Data, &plan)
	if plan.SourceMode != "worktree_snapshot" || plan.UntrackedPolicy != "include" || !samePlanStrings(plan.SourcePaths, []string{"work/core.txt", "work/new.txt"}) {
		t.Fatalf("worktree plan = %#v", plan)
	}
	plan.State = "reviewed"
	plan.Groups[0].Message = "fix(work): capture private worktree source"
	body, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	captured := runCLI(t, []string{"--json", "--repo-dir", directory, "capture", "--lane", "worktree-source", "--plan", planPath}, "")
	if captured.code != 0 || !captured.envelope.OK {
		t.Fatalf("worktree capture: %#v stderr=%s", captured.envelope, captured.stderr)
	}
	var result guidedCaptureResult
	decodeData(t, captured.envelope.Data, &result)
	if result.Capture.SourceMode != "worktree_snapshot" || result.Capture.UntrackedPolicy != "include" || result.Capture.SourceDigest != plan.SourceDigest || !result.Handoff.ReviewEligible {
		t.Fatalf("worktree capture result = %#v", result)
	}
	if got := cliGitRaw(t, directory, "ls-files", "-s", "-z"); got != indexBefore {
		t.Fatalf("caller index changed\nbefore=%q\nafter=%q", indexBefore, got)
	}
	if got := cliGit(t, directory, "rev-parse", "HEAD"); got != head {
		t.Fatalf("source HEAD moved: %s", got)
	}
	assertCLIFileAtCommit(t, directory, result.Handoff.LaneTip, "work/core.txt", "owned tracked")
	assertCLIFileAtCommit(t, directory, result.Handoff.LaneTip, "work/new.txt", "owned untracked")
	assertCLIFileAtCommit(t, directory, result.Handoff.LaneTip, "foreign.txt", "base foreign")
}

func TestCumulativeHandoffCoversEveryReviewedCaptureFromLaneBaseToTip(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "first capture\n")
	cliGit(t, directory, "add", "core.txt")
	base := cliGit(t, directory, "rev-parse", "HEAD")
	initializeCLI(t, directory, "cumulative-handoff", "core.txt", "docs.txt")

	firstPlan := reviewedPlanFile(t, directory, "cumulative-handoff", "fix(core): capture first segment")
	firstCapture := runCLI(t, []string{"--json", "--repo-dir", directory, "capture", "--lane", "cumulative-handoff", "--plan", firstPlan}, "")
	if firstCapture.code != 0 {
		t.Fatalf("first capture = %#v stderr=%s", firstCapture.envelope, firstCapture.stderr)
	}
	var first guidedCaptureResult
	decodeData(t, firstCapture.envelope.Data, &first)

	writeCLI(t, directory, "docs.txt", "second capture\n")
	cliGit(t, directory, "add", "docs.txt")
	secondPlan := reviewedPlanFile(t, directory, "cumulative-handoff", "docs(guide): capture second segment")
	secondCapture := runCLI(t, []string{"--json", "--repo-dir", directory, "capture", "--lane", "cumulative-handoff", "--plan", secondPlan}, "")
	if secondCapture.code != 0 {
		t.Fatalf("second capture = %#v stderr=%s", secondCapture.envelope, secondCapture.stderr)
	}
	var second guidedCaptureResult
	decodeData(t, secondCapture.envelope.Data, &second)
	if !second.Handoff.ReviewEligible || second.Handoff.LaneBaseCommit != base || len(second.Handoff.Segments) != 2 {
		t.Fatalf("cumulative handoff = %#v", second.Handoff)
	}
	firstSegment, secondSegment := second.Handoff.Segments[0], second.Handoff.Segments[1]
	if firstSegment.ParentCommit != base || firstSegment.FinalCommit != secondSegment.ParentCommit || secondSegment.FinalCommit != second.Handoff.LaneTip || second.Handoff.TipPlanID != secondSegment.PlanID || second.Handoff.TipCaptureIntentDigest != secondSegment.CaptureIntentDigest {
		t.Fatalf("segment chain is not contiguous: %#v", second.Handoff)
	}
	if first.Handoff.TipPlanID != firstSegment.PlanID || first.Handoff.TipCaptureIntentDigest != firstSegment.CaptureIntentDigest {
		t.Fatalf("first handoff was not retained in cumulative chain")
	}

	printed := runCLI(t, []string{"--json", "--repo-dir", directory, "handoff", "--lane", "cumulative-handoff"}, "")
	if printed.code != 0 {
		t.Fatalf("print cumulative handoff = %#v stderr=%s", printed.envelope, printed.stderr)
	}
	var receipt captureHandoff
	decodeData(t, printed.envelope.Data, &receipt)
	if receipt.HandoffDigest != second.Handoff.HandoffDigest {
		t.Fatalf("handoff digest changed: capture=%s printed=%s", second.Handoff.HandoffDigest, receipt.HandoffDigest)
	}

	staleTip := runCLI(t, []string{
		"--json", "--repo-dir", directory, "handoff", "--lane", "cumulative-handoff",
		"--plan-id", firstSegment.PlanID, "--plan-digest", firstSegment.CaptureIntentDigest,
	}, "")
	if staleTip.code == 0 || staleTip.envelope.Error == nil || staleTip.envelope.Error.Code != "HANDOFF_TIP_MISMATCH" {
		t.Fatalf("older segment was accepted as lane tip: %#v", staleTip.envelope)
	}
}

func TestCaptureRejectsDraftAndStaleReviewedPlans(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "first change\n")
	cliGit(t, directory, "add", "core.txt")
	initializeCLI(t, directory, "stale-plan", "core.txt")
	planPath := filepath.Join(t.TempDir(), "plan.json")
	drafted := runCLI(t, []string{"--json", "--repo-dir", directory, "plan", "--lane", "stale-plan", "--out", planPath}, "")
	if drafted.code != 0 {
		t.Fatalf("draft: %#v", drafted)
	}
	draftCapture := runCLI(t, []string{"--json", "--repo-dir", directory, "capture", "--lane", "stale-plan", "--plan", planPath}, "")
	if draftCapture.code == 0 || draftCapture.envelope.Error == nil || draftCapture.envelope.Error.Code != "PLAN_NOT_REVIEWED" {
		t.Fatalf("draft capture = %#v", draftCapture)
	}
	var plan planProposal
	decodeData(t, drafted.envelope.Data, &plan)
	plan.State = "reviewed"
	plan.Groups[0].Message = "fix(core): capture first behavior"
	body, _ := json.MarshalIndent(plan, "", "  ")
	if err := os.WriteFile(planPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	writeCLI(t, directory, "core.txt", "second change\n")
	cliGit(t, directory, "add", "core.txt")
	stale := runCLI(t, []string{"--json", "--repo-dir", directory, "capture", "--lane", "stale-plan", "--plan", planPath}, "")
	if stale.code == 0 || stale.envelope.Error == nil || stale.envelope.Error.Code != "STALE_PLAN" {
		t.Fatalf("stale capture = %#v", stale)
	}
}

func TestStatusClassifiesCaptureStateAndForeignIndexEntries(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	writeCLI(t, directory, "foreign.txt", "foreign staged\n")
	cliGit(t, directory, "add", "core.txt", "foreign.txt")
	initializeCLI(t, directory, "operational-status", "core.txt", "docs.txt")
	status := runCLI(t, []string{"--json", "--repo-dir", directory, "status", "--lane", "operational-status"}, "")
	if status.code != 0 || status.envelope.Context == nil || !status.envelope.Context.LeaseRenewed {
		t.Fatalf("status = %#v", status)
	}
	var before operationalStatus
	decodeData(t, status.envelope.Data, &before)
	if !samePlanStrings(before.CaptureCandidates, []string{"core.txt"}) || !samePlanStrings(before.ForeignStaged, []string{"foreign.txt"}) || !samePlanStrings(before.LeasedButNotStaged, []string{"docs.txt"}) {
		t.Fatalf("operational sets = %#v", before)
	}
	captured := runCLI(t, []string{"--json", "--repo-dir", directory, "commit", "--lane", "operational-status", "--single", "--message", "fix(core): capture status behavior", "--path", "core.txt"}, "")
	if captured.code != 0 {
		t.Fatalf("capture: %#v", captured)
	}
	afterResult := runCLI(t, []string{"--json", "--repo-dir", directory, "status", "--lane", "operational-status", "--observe"}, "")
	var after operationalStatus
	decodeData(t, afterResult.envelope.Data, &after)
	if len(after.CaptureCandidates) != 0 || !samePlanStrings(after.CapturedButStillStaged, []string{"core.txt"}) || after.Renewal.Attempted || after.LastIntent == nil || after.LastIntent.State != "complete" {
		t.Fatalf("captured status = %#v", after)
	}
}

func TestMessagePolicyIsBoundOutsideCaptureKernel(t *testing.T) {
	directory := cliTestRepo(t)
	config := `{"schema_version":"1.0.0","message_policy":"safety-only-v1"}`
	if err := os.WriteFile(filepath.Join(directory, ".wip-commit.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCLI(t, directory, "core.txt", "new core\n")
	cliGit(t, directory, "add", "core.txt")
	initialized := runCLI(t, []string{"--json", "--repo-dir", directory, "init", "--mode", "shared", "--lane", "policy-lane", "--agent", "agent", "--session", "session", "--path", "core.txt", "--non-interactive", "--no-install", "--no-install-skill"}, "")
	if initialized.code != 0 {
		t.Fatalf("init: %#v stderr=%s", initialized.envelope, initialized.stderr)
	}
	plain := runCLI(t, []string{"--json", "--repo-dir", directory, "commit", "--lane", "policy-lane", "--single", "--message", "plain local checkpoint", "--path", "core.txt"}, "")
	if plain.code != 0 || !plain.envelope.OK {
		t.Fatalf("safety-only capture: %#v stderr=%s", plain.envelope, plain.stderr)
	}
}

func TestGuidedCaptureFinalReleasesAfterHandoffValidation(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	cliGit(t, directory, "add", "core.txt")
	initializeCLI(t, directory, "final-capture", "core.txt")
	planPath := filepath.Join(t.TempDir(), "plan.json")
	drafted := runCLI(t, []string{"--json", "--repo-dir", directory, "plan", "--lane", "final-capture", "--out", planPath}, "")
	var plan planProposal
	decodeData(t, drafted.envelope.Data, &plan)
	plan.State = "reviewed"
	plan.Groups[0].Message = "fix(core): finalize reviewed capture"
	body, _ := json.MarshalIndent(plan, "", "  ")
	if err := os.WriteFile(planPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	captured := runCLI(t, []string{"--json", "--repo-dir", directory, "capture", "--lane", "final-capture", "--plan", planPath, "--final"}, "")
	if captured.code != 0 || !captured.envelope.OK {
		t.Fatalf("final capture: %#v stderr=%s", captured.envelope, captured.stderr)
	}
	var result guidedCaptureResult
	decodeData(t, captured.envelope.Data, &result)
	if !result.LaneReleased || len(result.Handoff.Segments) != 1 || result.Handoff.Segments[0].IntentState != "complete" {
		t.Fatalf("final result = %#v", result)
	}
	laneStore := inspectCLIStore(t, directory)
	lockPath := filepath.Join(laneStore.Root, "locks", "lane-final-capture.lock")
	lockBefore, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	printed := runCLI(t, []string{"--json", "--repo-dir", directory, "handoff", "--lane", "final-capture"}, "")
	if printed.code != 0 || !printed.envelope.OK {
		t.Fatalf("released handoff: %#v stderr=%s", printed.envelope, printed.stderr)
	}
	lockAfter, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(lockAfter, lockBefore) {
		t.Fatalf("released handoff changed the lane lock: before=%q after=%q", lockBefore, lockAfter)
	}
}

func TestGuidedCaptureKeepsLaneLockThroughFinalRelease(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	cliGit(t, directory, "add", "core.txt")
	initializeCLI(t, directory, "locked-final", "core.txt")
	planPath := reviewedPlanFile(t, directory, "locked-final", "fix(core): finalize under one lane lock")

	entered := make(chan struct{})
	proceed := make(chan struct{})
	var stdout, stderr bytes.Buffer
	application := app{
		stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr,
		beforeFinalRelease: func() {
			close(entered)
			<-proceed
		},
	}
	done := make(chan int, 1)
	go func() {
		done <- application.run(context.Background(), []string{"--json", "--repo-dir", directory, "capture", "--lane", "locked-final", "--plan", planPath, "--final"})
	}()
	waitForBarrier(t, entered, "final capture validation")
	unblocked := false
	defer func() {
		if !unblocked {
			close(proceed)
		}
	}()

	laneStore := inspectCLIStore(t, directory)
	lock, err := laneStore.LaneLock("locked-final", 100*time.Millisecond)
	if err == nil {
		_ = lock.Release()
		t.Fatal("another lane transaction acquired the lock before final release")
	}
	if fail.Code(err) != "LOCK_TIMEOUT" {
		t.Fatalf("competing lane lock error = %v (%s)", err, fail.Code(err))
	}
	lane, err := laneStore.Load("locked-final")
	if err != nil || lane.State != "active" {
		t.Fatalf("lane before final release = %#v err=%v", lane, err)
	}
	close(proceed)
	unblocked = true

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("final capture code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("final capture did not finish")
	}
	lane, err = laneStore.Load("locked-final")
	if err != nil || lane.State != "released" {
		t.Fatalf("lane after final release = %#v err=%v", lane, err)
	}
}

func TestHandoffKeepsLaneLockThroughValidationAndRenewal(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	cliGit(t, directory, "add", "core.txt")
	initializeCLI(t, directory, "locked-handoff", "core.txt")
	planPath := reviewedPlanFile(t, directory, "locked-handoff", "fix(core): validate handoff under one lock")
	captured := runCLI(t, []string{"--json", "--repo-dir", directory, "capture", "--lane", "locked-handoff", "--plan", planPath}, "")
	if captured.code != 0 {
		t.Fatalf("capture = %#v stderr=%s", captured.envelope, captured.stderr)
	}

	entered := make(chan struct{})
	proceed := make(chan struct{})
	var stdout, stderr bytes.Buffer
	application := app{
		stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr,
		afterHandoffValidation: func() {
			close(entered)
			<-proceed
		},
	}
	done := make(chan int, 1)
	go func() {
		done <- application.run(context.Background(), []string{"--json", "--repo-dir", directory, "handoff", "--lane", "locked-handoff"})
	}()
	waitForBarrier(t, entered, "handoff validation")
	unblocked := false
	defer func() {
		if !unblocked {
			close(proceed)
		}
	}()

	laneStore := inspectCLIStore(t, directory)
	lock, err := laneStore.LaneLock("locked-handoff", 100*time.Millisecond)
	if err == nil {
		_ = lock.Release()
		t.Fatal("another lane transaction acquired the lock after handoff validation")
	}
	if fail.Code(err) != "LOCK_TIMEOUT" {
		t.Fatalf("competing lane lock error = %v (%s)", err, fail.Code(err))
	}
	close(proceed)
	unblocked = true

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("handoff code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("handoff did not finish")
	}
}

func reviewedPlanFile(t *testing.T, directory, lane, message string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "reviewed-plan.json")
	drafted := runCLI(t, []string{"--json", "--repo-dir", directory, "plan", "--lane", lane, "--out", path}, "")
	if drafted.code != 0 {
		t.Fatalf("draft plan = %#v stderr=%s", drafted.envelope, drafted.stderr)
	}
	var plan planProposal
	decodeData(t, drafted.envelope.Data, &plan)
	plan.State = "reviewed"
	for index := range plan.Groups {
		plan.Groups[index].Message = message
	}
	body, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func inspectCLIStore(t *testing.T, directory string) store.Store {
	t.Helper()
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, initialized, err := store.Inspect(repo)
	if err != nil || !initialized {
		t.Fatalf("inspect initialized=%v err=%v", initialized, err)
	}
	return laneStore
}

func waitForBarrier(t *testing.T, barrier <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-barrier:
	case <-time.After(20 * time.Second):
		t.Fatalf("%s did not reach its test barrier", label)
	}
}

func TestPlanOutputDoesNotOverwriteAndLeaseChangesMakeReviewStale(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	writeCLI(t, directory, "docs.txt", "new docs\n")
	cliGit(t, directory, "add", "core.txt", "docs.txt")
	initializeCLI(t, directory, "plan-binding", "core.txt")

	existingPath := filepath.Join(t.TempDir(), "existing.json")
	if err := os.WriteFile(existingPath, []byte("operator-owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused := runCLI(t, []string{"--json", "--repo-dir", directory, "plan", "--lane", "plan-binding", "--out", existingPath}, "")
	if refused.code == 0 || refused.envelope.Error == nil || refused.envelope.Error.Code != "PLAN_EXISTS" {
		t.Fatalf("no-overwrite plan = %#v", refused)
	}
	if body, err := os.ReadFile(existingPath); err != nil || string(body) != "operator-owned\n" {
		t.Fatalf("existing output changed: %q err=%v", body, err)
	}

	planPath := filepath.Join(t.TempDir(), "review.json")
	drafted := runCLI(t, []string{"--json", "--repo-dir", directory, "plan", "--lane", "plan-binding", "--out", planPath}, "")
	if drafted.code != 0 {
		t.Fatalf("draft = %#v", drafted)
	}
	var plan planProposal
	decodeData(t, drafted.envelope.Data, &plan)
	plan.State = "reviewed"
	plan.Groups[0].Message = "fix(plan): bind the active lease set"
	body, _ := json.MarshalIndent(plan, "", "  ")
	if err := os.WriteFile(planPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	claimed := runCLI(t, []string{"--json", "--repo-dir", directory, "claim", "--lane", "plan-binding", "--path", "docs.txt"}, "")
	if claimed.code != 0 {
		t.Fatalf("claim = %#v", claimed)
	}
	stale := runCLI(t, []string{"--json", "--repo-dir", directory, "capture", "--lane", "plan-binding", "--plan", planPath}, "")
	if stale.code == 0 || stale.envelope.Error == nil || stale.envelope.Error.Code != "STALE_PLAN" {
		t.Fatalf("lease-stale capture = %#v", stale)
	}
}

func TestConventionalPolicyRejectsBeforeRepositoryHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX hook")
	}
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	cliGit(t, directory, "add", "core.txt")
	initializeCLI(t, directory, "policy-before-hook", "core.txt")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := cliGit(t, directory, "rev-parse", "--git-path", "hooks/pre-commit")
	if !filepath.IsAbs(hook) {
		hook = filepath.Join(directory, hook)
	}
	script := "#!/bin/sh\n: > " + quoteCommandArg(marker) + "\n"
	if err := os.WriteFile(hook, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	result := runCLI(t, []string{"--json", "--repo-dir", directory, "commit", "--lane", "policy-before-hook", "--single", "--message", "plain local checkpoint", "--path", "core.txt"}, "")
	if result.code == 0 || result.envelope.Error == nil || result.envelope.Error.Code != "MESSAGE_POLICY_REJECTED" {
		t.Fatalf("policy result = %#v stderr=%s", result.envelope, result.stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("policy failure ran the repository hook: %v", err)
	}
}

func TestLegacyProfileReadPreservesBytesAndInfersDefaultPolicy(t *testing.T) {
	directory := cliTestRepo(t)
	initializeCLI(t, directory, "legacy-profile", "core.txt")
	gitDir := cliGit(t, directory, "rev-parse", "--git-common-dir")
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(directory, gitDir)
	}
	path := filepath.Join(gitDir, "wip", "v1", "profiles", "legacy-profile.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	record["schema_version"] = float64(legacyProfileSchemaVersion)
	delete(record, "message_policy")
	legacy, _ := json.MarshalIndent(record, "", "  ")
	legacy = append(legacy, '\n')
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	result := runCLI(t, []string{"--json", "--repo-dir", directory, "status", "--lane", "legacy-profile", "--observe"}, "")
	if result.code != 0 || result.envelope.Context == nil || result.envelope.Context.MessagePolicy.ID != "conventional-v1" {
		t.Fatalf("legacy profile status = %#v stderr=%s", result.envelope, result.stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(legacy) {
		t.Fatalf("legacy profile read changed bytes: err=%v", err)
	}
}

func TestLegacyInitIntentReadPreservesHistoricalDigestAndBytes(t *testing.T) {
	directory := cliTestRepo(t)
	initialized := runCLI(t, []string{"--json", "--repo-dir", directory, "init", "--mode", "shared", "--lane", "legacy-init", "--agent", "agent", "--session", "session", "--path", "core.txt", "--non-interactive", "--no-install", "--no-install-skill"}, "")
	if initialized.code != 0 {
		t.Fatalf("init = %#v stderr=%s", initialized.envelope, initialized.stderr)
	}
	var setup initResult
	decodeData(t, initialized.envelope.Data, &setup)
	intent, err := loadInitIntent(setup.IntentPath)
	if err != nil {
		t.Fatal(err)
	}
	intent.SchemaVersion = legacyInitIntentSchemaVersion
	intent.MessagePolicy = nil
	intent.ID, intent.Digest = "", ""
	digest, err := digestInitIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	intent.Digest = digest
	intent.ID = "init-" + strings.TrimPrefix(digest, "sha256:")[:24]
	legacy, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	legacy = append(legacy, '\n')
	legacyPath := filepath.Join(filepath.Dir(setup.IntentPath), intent.ID+".json")
	if legacyPath != setup.IntentPath {
		if err := os.Rename(setup.IntentPath, legacyPath); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadInitIntent(legacyPath)
	if err != nil || loaded.SchemaVersion != legacyInitIntentSchemaVersion || loaded.Digest != digest || loaded.MessagePolicy != nil {
		t.Fatalf("legacy intent = %#v err=%v", loaded, err)
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil || string(after) != string(legacy) {
		t.Fatalf("legacy intent read changed bytes: err=%v", err)
	}
}
