package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nstranquist/wip-commit/internal/engine"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/store"
)

func TestNonInteractiveInitIsIdempotentAndSingleCaptureIsExact(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	writeCLI(t, directory, "docs.txt", "new docs\n")
	writeCLI(t, directory, "foreign.txt", "foreign staged\n")
	cliGit(t, directory, "add", "core.txt", "docs.txt", "foreign.txt")
	head := cliGit(t, directory, "rev-parse", "HEAD")
	index := cliGitRaw(t, directory, "ls-files", "-s", "-z")
	initArgs := []string{"--json", "--repo-dir", directory, "init", "--mode", "shared", "--lane", "exact-capture", "--agent", "agent", "--session", "session", "--path", "core.txt", "--path", "docs.txt", "--non-interactive", "--no-install"}
	first := runCLI(t, initArgs, "")
	if first.code != 0 || !first.envelope.OK {
		t.Fatalf("first init: %#v stderr=%s", first.envelope, first.stderr)
	}
	second := runCLI(t, initArgs, "")
	if second.code != 0 || !second.envelope.OK {
		t.Fatalf("second init: %#v stderr=%s", second.envelope, second.stderr)
	}
	repo, _ := gitx.Discover(context.Background(), directory)
	laneStore, _ := store.Open(repo)
	status, err := laneStore.Status("exact-capture")
	if err != nil || len(status.Leases) != 1 {
		t.Fatalf("leases = %#v, err=%v", status.Leases, err)
	}
	commit := runCLI(t, []string{"--json", "--repo-dir", directory, "commit", "--lane", "exact-capture", "--single", "--message", "fix(core): capture staged core change", "--path", "core.txt"}, "")
	if commit.code != 0 || !commit.envelope.OK {
		t.Fatalf("commit: %#v stderr=%s", commit.envelope, commit.stderr)
	}
	var result engine.Result
	decodeData(t, commit.envelope.Data, &result)
	if result.IntentState != "complete" || !result.RefUpdated {
		t.Fatalf("commit result = %#v", result)
	}
	if got := cliGit(t, directory, "rev-parse", "HEAD"); got != head {
		t.Fatalf("source HEAD moved: %s", got)
	}
	if got := cliGitRaw(t, directory, "ls-files", "-s", "-z"); got != index {
		t.Fatal("source index changed")
	}
	if got := cliGit(t, directory, "show", result.FinalCommit+":foreign.txt"); got != "base foreign" {
		t.Fatalf("foreign staged content leaked: %q", got)
	}
	if got := cliGit(t, directory, "show", result.FinalCommit+":docs.txt"); got != "base docs" {
		t.Fatalf("unselected leased docs content leaked: %q", got)
	}
}

func TestConcurrentSharedLanesCaptureDisjointPaths(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	writeCLI(t, directory, "docs.txt", "new docs\n")
	writeCLI(t, directory, "foreign.txt", "foreign staged\n")
	cliGit(t, directory, "add", "core.txt", "docs.txt", "foreign.txt")
	head := cliGit(t, directory, "rev-parse", "HEAD")
	index := cliGitRaw(t, directory, "ls-files", "-s", "-z")
	initializeCLI(t, directory, "core-lane", "core.txt")
	initializeCLI(t, directory, "docs-lane", "docs.txt")

	type concurrentResult struct {
		lane   string
		code   int
		stdout string
		stderr string
	}
	start := make(chan struct{})
	results := make(chan concurrentResult, 2)
	runLane := func(lane, message, path string) {
		<-start
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{
			"--json", "--repo-dir", directory, "commit", "--lane", lane,
			"--single", "--message", message, "--path", path,
		}, strings.NewReader(""), &stdout, &stderr)
		results <- concurrentResult{lane: lane, code: code, stdout: stdout.String(), stderr: stderr.String()}
	}
	go runLane("core-lane", "fix(core): capture core behavior", "core.txt")
	go runLane("docs-lane", "docs(guide): capture docs behavior", "docs.txt")
	close(start)

	commits := map[string]string{}
	for range 2 {
		captured := <-results
		var output envelope
		if err := json.Unmarshal([]byte(captured.stdout), &output); err != nil {
			t.Fatalf("decode %s output %q: %v; stderr=%s", captured.lane, captured.stdout, err, captured.stderr)
		}
		if captured.code != 0 || !output.OK {
			t.Fatalf("%s capture: code=%d output=%#v stderr=%s", captured.lane, captured.code, output, captured.stderr)
		}
		var result engine.Result
		decodeData(t, output.Data, &result)
		if !result.RefUpdated || result.IntentState != "complete" || len(result.Commits) != 1 {
			t.Fatalf("%s result = %#v", captured.lane, result)
		}
		commits[captured.lane] = result.FinalCommit
	}

	if got := cliGit(t, directory, "rev-parse", "HEAD"); got != head {
		t.Fatalf("source HEAD moved: %s", got)
	}
	if got := cliGitRaw(t, directory, "ls-files", "-s", "-z"); got != index {
		t.Fatal("source index changed")
	}
	assertCLIFileAtCommit(t, directory, commits["core-lane"], "core.txt", "new core")
	assertCLIFileAtCommit(t, directory, commits["core-lane"], "docs.txt", "base docs")
	assertCLIFileAtCommit(t, directory, commits["core-lane"], "foreign.txt", "base foreign")
	assertCLIFileAtCommit(t, directory, commits["docs-lane"], "core.txt", "base core")
	assertCLIFileAtCommit(t, directory, commits["docs-lane"], "docs.txt", "new docs")
	assertCLIFileAtCommit(t, directory, commits["docs-lane"], "foreign.txt", "base foreign")
}

func TestCommitRequiresSplitPlanUnlessSingleIsExplicit(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	writeCLI(t, directory, "docs.txt", "new docs\n")
	cliGit(t, directory, "add", "core.txt", "docs.txt")
	initializeCLI(t, directory, "split-required", "core.txt", "docs.txt")
	failed := runCLI(t, []string{"--json", "--repo-dir", directory, "commit", "--lane", "split-required", "--non-interactive", "--message", "fix(core): capture staged changes"}, "")
	if failed.code == 0 || failed.envelope.Error == nil || failed.envelope.Error.Code != "SPLIT_PLAN_REQUIRED" {
		t.Fatalf("unexpected split-default result: %#v", failed)
	}
	planPath := filepath.Join(t.TempDir(), "plan.json")
	plan := `[
  {"message":"fix(core): capture core behavior","files":["core.txt"]},
  {"message":"docs(guide): capture docs behavior","files":["docs.txt"]}
]`
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	passed := runCLI(t, []string{"--json", "--repo-dir", directory, "commit", "--lane", "split-required", "--plan", planPath}, "")
	if passed.code != 0 || !passed.envelope.OK {
		t.Fatalf("plan commit: %#v stderr=%s", passed.envelope, passed.stderr)
	}
	var result engine.Result
	decodeData(t, passed.envelope.Data, &result)
	if len(result.Commits) != 2 || result.Commits[1].Parent != result.Commits[0].Commit {
		t.Fatalf("split chain = %#v", result.Commits)
	}
}

func TestInteractiveInitAndSplitPlannerKeepJSONClean(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	writeCLI(t, directory, "docs.txt", "new docs\n")
	cliGit(t, directory, "add", "core.txt", "docs.txt")
	input := "shared\n\n\ninteractive-lane\nyes\nyes\nyes\n"
	initialized := runCLI(t, []string{"--json", "--repo-dir", directory, "init", "--no-install"}, input)
	if initialized.code != 0 || !initialized.envelope.OK {
		t.Fatalf("interactive init: %#v stderr=%s stdout=%s", initialized.envelope, initialized.stderr, initialized.stdout)
	}
	if strings.Contains(initialized.stdout, "Lane mode") {
		t.Fatalf("prompt corrupted JSON stdout: %q", initialized.stdout)
	}
	commitInput := "\nfix(core): capture core behavior\ndocs(guide): capture docs behavior\n"
	committed := runCLI(t, []string{"--json", "--repo-dir", directory, "commit", "--lane", "interactive-lane"}, commitInput)
	if committed.code != 0 || !committed.envelope.OK {
		t.Fatalf("interactive commit: %#v stderr=%s stdout=%s", committed.envelope, committed.stderr, committed.stdout)
	}
	var result engine.Result
	decodeData(t, committed.envelope.Data, &result)
	if len(result.Commits) != 2 {
		t.Fatalf("default planner made %d commits, want 2", len(result.Commits))
	}
}

func TestInitCanCreateAndCaptureLinkedWorktreeWithoutMovingCheckouts(t *testing.T) {
	directory := cliTestRepo(t)
	head := cliGit(t, directory, "rev-parse", "HEAD")
	linked := filepath.Join(t.TempDir(), "agent-worktree")
	result := runCLI(t, []string{"--json", "--repo-dir", directory, "init", "--mode", "worktree", "--create-worktree", linked, "--lane", "linked-lane", "--agent", "agent", "--session", "session", "--path", "core.txt", "--non-interactive", "--no-install"}, "")
	if result.code != 0 || !result.envelope.OK {
		t.Fatalf("worktree init: %#v stderr=%s", result.envelope, result.stderr)
	}
	var initialized initResult
	decodeData(t, result.envelope.Data, &initialized)
	if !initialized.CreatedWorktree || initialized.Lane == nil || initialized.Lane.Mode != store.ModeWorktree {
		t.Fatalf("worktree result = %#v", initialized)
	}
	if got := cliGit(t, directory, "rev-parse", "HEAD"); got != head {
		t.Fatalf("anchor HEAD moved: %s", got)
	}
	if got := cliGit(t, linked, "rev-parse", "HEAD"); got != head {
		t.Fatalf("linked HEAD = %s, want %s", got, head)
	}
	writeCLI(t, linked, "core.txt", "linked core\n")
	cliGit(t, linked, "add", "core.txt")
	anchorIndex := cliGitRaw(t, directory, "ls-files", "-s", "-z")
	linkedIndex := cliGitRaw(t, linked, "ls-files", "-s", "-z")
	committed := runCLI(t, []string{
		"--json", "--repo-dir", linked, "commit", "--lane", "linked-lane",
		"--single", "--message", "fix(core): capture linked worktree change", "--path", "core.txt",
	}, "")
	if committed.code != 0 || !committed.envelope.OK {
		t.Fatalf("worktree capture: %#v stderr=%s", committed.envelope, committed.stderr)
	}
	var capture engine.Result
	decodeData(t, committed.envelope.Data, &capture)
	if !capture.RefUpdated || capture.IntentState != "complete" {
		t.Fatalf("worktree capture result = %#v", capture)
	}
	if got := cliGit(t, directory, "rev-parse", "HEAD"); got != head {
		t.Fatalf("anchor HEAD moved after capture: %s", got)
	}
	if got := cliGit(t, linked, "rev-parse", "HEAD"); got != head {
		t.Fatalf("linked HEAD moved after capture: %s", got)
	}
	if got := cliGitRaw(t, directory, "ls-files", "-s", "-z"); got != anchorIndex {
		t.Fatal("anchor index changed")
	}
	if got := cliGitRaw(t, linked, "ls-files", "-s", "-z"); got != linkedIndex {
		t.Fatal("linked index changed")
	}
	assertCLIFileAtCommit(t, directory, capture.FinalCommit, "core.txt", "linked core")
}

func TestInitDryRunAndInstallerAreNonDestructive(t *testing.T) {
	directory := cliTestRepo(t)
	linked := filepath.Join(t.TempDir(), "not-created")
	result := runCLI(t, []string{"--json", "--repo-dir", directory, "init", "--mode", "worktree", "--create-worktree", linked, "--lane", "dry-run", "--agent", "agent", "--session", "session", "--path", "core.txt", "--non-interactive", "--no-install", "--dry-run"}, "")
	if result.code != 0 || !result.envelope.OK {
		t.Fatalf("dry-run: %#v stderr=%s", result.envelope, result.stderr)
	}
	if _, err := os.Lstat(linked); !os.IsNotExist(err) {
		t.Fatalf("dry-run created worktree: %v", err)
	}
	gitDir := cliGit(t, directory, "rev-parse", "--absolute-git-dir")
	if _, err := os.Lstat(filepath.Join(gitDir, "wip")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created WIP store: %v", err)
	}
	installDir := t.TempDir()
	path, installed, valid, err := installSelf(installDir)
	if err != nil || !installed || valid {
		t.Fatalf("first install path=%s installed=%v valid=%v err=%v", path, installed, valid, err)
	}
	_, installed, valid, err = installSelf(installDir)
	if err != nil || installed || !valid {
		t.Fatalf("second install installed=%v valid=%v err=%v", installed, valid, err)
	}
	if err := os.WriteFile(path, []byte("different binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := installSelf(installDir); err == nil {
		t.Fatal("installer overwrote or accepted a different binary")
	}
}

func TestReconcileFinishesInterruptedRefPublicationIdempotently(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	cliGit(t, directory, "add", "core.txt")
	initializeCLI(t, directory, "recover-lane", "core.txt")
	repo, _ := gitx.Discover(context.Background(), directory)
	laneStore, _ := store.Open(repo)
	lane, _ := laneStore.Load("recover-lane")
	allowed, _ := laneStore.ActivePaths(lane.ID)
	result, err := engine.Run(context.Background(), engine.Options{
		Repo: repo, TargetRef: lane.Ref, ExpectedRef: lane.CurrentSHA, ExpectedSourceHead: lane.BaseSHA,
		AllowedPaths: allowed, Groups: []engine.Group{{Message: "fix(recovery): publish recoverable receipt", Files: []string{"core.txt"}}},
		BeforePublish: func() error { return laneStore.ValidateCapture(context.Background(), lane, allowed) },
	})
	if err != nil || result.IntentState != "applied" {
		t.Fatalf("interrupted publish setup: %#v err=%v", result, err)
	}
	if current, _ := laneStore.Load(lane.ID); current.CurrentSHA != lane.CurrentSHA {
		t.Fatal("test setup unexpectedly advanced lane metadata")
	}
	args := []string{"--json", "--repo-dir", directory, "reconcile", "--lane", lane.ID, "--plan-id", result.PlanID, "--plan-digest", result.PlanDigest}
	first := runCLI(t, args, "")
	if first.code != 0 || !first.envelope.OK {
		t.Fatalf("first reconcile: %#v stderr=%s", first.envelope, first.stderr)
	}
	current, _ := laneStore.Load(lane.ID)
	if current.CurrentSHA != result.FinalCommit {
		t.Fatalf("lane cursor = %s, want %s", current.CurrentSHA, result.FinalCommit)
	}
	intent, _, err := engine.LoadIntent(repo, result.PlanID, result.PlanDigest)
	if err != nil || intent.State != "complete" {
		t.Fatalf("intent = %#v err=%v", intent, err)
	}
	second := runCLI(t, args, "")
	if second.code != 0 || !second.envelope.OK {
		t.Fatalf("second reconcile: %#v stderr=%s", second.envelope, second.stderr)
	}
	var reconciled engine.ReconcileResult
	decodeData(t, second.envelope.Data, &reconciled)
	if !reconciled.AlreadyClean {
		t.Fatalf("second reconcile was not idempotent: %#v", reconciled)
	}
}

func TestPlanDecoderRejectsAmbiguousOrUnboundedJSON(t *testing.T) {
	cases := []string{
		`[{"message":"fix(core): capture behavior","message":"fix(core): shadow behavior","files":["core.txt"]}]`,
		`[{"message":"fix(core): capture behavior","files":["core.txt"],"unknown":true}]`,
		`[{"message":"fix(core): capture behavior","files":["core.txt"]}] true`,
	}
	for _, body := range cases {
		if _, err := decodePlan(strings.NewReader(body)); err == nil {
			t.Errorf("ambiguous plan unexpectedly passed: %s", body)
		}
	}
	oversized := strings.Repeat(" ", maxPlanBytes+1)
	if _, err := decodePlan(strings.NewReader(oversized)); err == nil {
		t.Fatal("oversized plan unexpectedly passed")
	}
}

func TestLaneLifecycleCommandsPreserveRef(t *testing.T) {
	directory := cliTestRepo(t)
	initializeCLI(t, directory, "lifecycle", "core.txt")
	status := runCLI(t, []string{"--json", "--repo-dir", directory, "status", "--lane", "lifecycle"}, "")
	if status.code != 0 || !status.envelope.OK {
		t.Fatalf("status: %#v", status)
	}
	environment := runCLI(t, []string{"--json", "--repo-dir", directory, "env", "--lane", "lifecycle"}, "")
	if environment.code != 0 || !environment.envelope.OK {
		t.Fatalf("env: %#v", environment)
	}
	claim := runCLI(t, []string{"--json", "--repo-dir", directory, "claim", "--lane", "lifecycle", "--path", "docs.txt"}, "")
	if claim.code != 0 || !claim.envelope.OK {
		t.Fatalf("claim: %#v", claim)
	}
	renew := runCLI(t, []string{"--json", "--repo-dir", directory, "renew", "--lane", "lifecycle"}, "")
	if renew.code != 0 || !renew.envelope.OK {
		t.Fatalf("renew: %#v", renew)
	}
	repo, _ := gitx.Discover(context.Background(), directory)
	laneStore, _ := store.Open(repo)
	lane, _ := laneStore.Load("lifecycle")
	release := runCLI(t, []string{"--json", "--repo-dir", directory, "release", "--lane", "lifecycle"}, "")
	if release.code != 0 || !release.envelope.OK {
		t.Fatalf("release: %#v", release)
	}
	if got := cliGit(t, directory, "rev-parse", lane.Ref); got != lane.CurrentSHA {
		t.Fatalf("release changed or deleted ref: %s", got)
	}
	after := runCLI(t, []string{"--json", "--repo-dir", directory, "status", "--lane", "lifecycle"}, "")
	if after.code == 0 || after.envelope.Error == nil || after.envelope.Error.Code != "LANE_NOT_ACTIVE" {
		t.Fatalf("released lane remained active: %#v", after)
	}
}

func TestFutureProfileSchemaRequiresMigration(t *testing.T) {
	directory := cliTestRepo(t)
	initializeCLI(t, directory, "future-profile", "core.txt")
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := loadProfile(laneStore, "future-profile")
	if err != nil {
		t.Fatal(err)
	}
	saved.SchemaVersion = profileSchemaVersion + 1
	body, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath(laneStore, saved.Lane), append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runCLI(t, []string{"--json", "--repo-dir", directory, "status", "--lane", saved.Lane}, "")
	if result.code == 0 || result.envelope.Error == nil || result.envelope.Error.Code != "MIGRATION_REQUIRED" {
		t.Fatalf("future profile result = %#v stderr=%s", result.envelope, result.stderr)
	}
}

type cliRun struct {
	code           int
	stdout, stderr string
	envelope       envelope
}

func runCLI(t *testing.T, args []string, input string) cliRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(input), &stdout, &stderr)
	result := cliRun{code: code, stdout: stdout.String(), stderr: stderr.String()}
	if strings.Contains(strings.Join(args, " "), "--json") {
		if err := json.Unmarshal(stdout.Bytes(), &result.envelope); err != nil {
			t.Fatalf("decode JSON output %q: %v; stderr=%s", stdout.String(), err, stderr.String())
		}
	}
	return result
}

func decodeData(t *testing.T, value any, target any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatal(err)
	}
}

func initializeCLI(t *testing.T, directory, lane string, paths ...string) {
	t.Helper()
	args := []string{"--json", "--repo-dir", directory, "init", "--mode", "shared", "--lane", lane, "--agent", "agent", "--session", "session", "--non-interactive", "--no-install"}
	for _, path := range paths {
		args = append(args, "--path", path)
	}
	result := runCLI(t, args, "")
	if result.code != 0 {
		t.Fatalf("initialize: %#v stderr=%s", result.envelope, result.stderr)
	}
}

func cliTestRepo(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	cliGit(t, directory, "init", "-b", "main")
	cliGit(t, directory, "config", "user.name", "WIP Tests")
	cliGit(t, directory, "config", "user.email", "wip@example.invalid")
	writeCLI(t, directory, "core.txt", "base core\n")
	writeCLI(t, directory, "docs.txt", "base docs\n")
	writeCLI(t, directory, "foreign.txt", "base foreign\n")
	cliGit(t, directory, "add", ".")
	cliGit(t, directory, "commit", "-m", "test: create fixture")
	return directory
}

func writeCLI(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cliGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(cliGitRaw(t, directory, args...))
}

func cliGitRaw(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func assertCLIFileAtCommit(t *testing.T, directory, commit, path, wanted string) {
	t.Helper()
	if got := cliGit(t, directory, "show", commit+":"+path); got != wanted {
		t.Fatalf("%s:%s = %q, want %q", commit, path, got, wanted)
	}
}
