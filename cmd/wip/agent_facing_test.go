package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

	captured := runCLI(t, []string{"--json", "--repo-dir", directory, "capture", "--lane", "guided-capture", "--plan", planPath}, "")
	if captured.code != 0 || !captured.envelope.OK {
		t.Fatalf("capture: %#v stderr=%s", captured.envelope, captured.stderr)
	}
	var result guidedCaptureResult
	decodeData(t, captured.envelope.Data, &result)
	if !result.Handoff.CaptureOnly || result.Handoff.LandingAuthorized || !result.Handoff.MessagePolicyBound || result.Handoff.ReviewedPlanDigest == "" || result.Handoff.IntentState != "complete" {
		t.Fatalf("handoff = %#v", result.Handoff)
	}
	if result.Handoff.LaneTip != result.Capture.FinalCommit || result.Handoff.FinalTree != result.Capture.FinalTree || len(result.Handoff.Commits) != 2 {
		t.Fatalf("capture/handoff mismatch = %#v", result)
	}
	if result.Capture.SourceIndexDigest != plan.SourceIndexDigest {
		t.Fatalf("kernel source digest = %q, reviewed digest = %q", result.Capture.SourceIndexDigest, plan.SourceIndexDigest)
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
	if receipt.CaptureIntentDigest != result.Handoff.CaptureIntentDigest || receipt.LaneTip != result.Handoff.LaneTip {
		t.Fatalf("handoff changed = %#v", receipt)
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
	if !result.LaneReleased || result.Handoff.IntentState != "complete" {
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
