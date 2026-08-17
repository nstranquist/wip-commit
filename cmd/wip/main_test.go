package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nstranquist/wip-commit/internal/engine"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/store"
	skillbundle "github.com/nstranquist/wip-commit/skills/wip-commit"
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
	var setup initResult
	decodeData(t, first.envelope.Data, &setup)
	if setup.IntentState != "complete" || len(setup.CompletedSteps) != len(initStepOrder) {
		t.Fatalf("init receipt = %#v", setup)
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

func TestIdentityEnvironmentDefaultsRemainSupported(t *testing.T) {
	directory := cliTestRepo(t)
	initializeCLI(t, directory, "environment-defaults", "core.txt")
	t.Setenv("WIP_LANE", "environment-defaults")
	t.Setenv("WIP_AGENT", "agent")
	t.Setenv("WIP_SESSION", "session")
	result := runCLIWithAmbientIdentity(t, []string{"--json", "--repo-dir", directory, "status"}, "")
	if result.code != 0 || !result.envelope.OK {
		t.Fatalf("environment identity status = %#v stderr=%s", result.envelope, result.stderr)
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
			errorCode, errorMessage := "", ""
			if output.Error != nil {
				errorCode, errorMessage = output.Error.Code, output.Error.Message
			}
			data, marshalErr := json.Marshal(output.Data)
			if marshalErr != nil {
				data = []byte("<could not encode data: " + marshalErr.Error() + ">")
			}
			var recovery engine.Result
			if output.Data != nil {
				_ = json.Unmarshal(data, &recovery)
			}
			t.Fatalf("%s capture: code=%d error_code=%q error_message=%q recovery={ref_updated:%t plan_id:%q plan_digest:%q intent_path:%q intent_state:%q final_commit:%q} data=%s stdout=%q stderr=%q",
				captured.lane, captured.code, errorCode, errorMessage,
				recovery.RefUpdated, recovery.PlanID, recovery.PlanDigest, recovery.IntentPath, recovery.IntentState, recovery.FinalCommit,
				data, captured.stdout, captured.stderr)
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

func TestPlanPreviewUsesComponentBoundariesWithoutMutation(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	writeCLI(t, directory, "docs.txt", "new docs\n")
	cliGit(t, directory, "add", "core.txt", "docs.txt")
	initializeCLI(t, directory, "plan-preview", "core.txt", "docs.txt")
	head := cliGit(t, directory, "rev-parse", "HEAD")
	index := cliGitRaw(t, directory, "ls-files", "-s", "-z")
	preview := runCLI(t, []string{"--json", "--repo-dir", directory, "plan", "--lane", "plan-preview"}, "")
	if preview.code != 0 || !preview.envelope.OK {
		t.Fatalf("plan preview: %#v stderr=%s", preview.envelope, preview.stderr)
	}
	var proposal planProposal
	decodeData(t, preview.envelope.Data, &proposal)
	if len(proposal.Groups) != 2 || proposal.Groups[0].SuggestedScope != "core" || proposal.Groups[1].SuggestedScope != "docs" {
		t.Fatalf("plan proposal = %#v", proposal)
	}
	if got := cliGit(t, directory, "rev-parse", "HEAD"); got != head {
		t.Fatalf("plan moved HEAD: %s -> %s", head, got)
	}
	if got := cliGitRaw(t, directory, "ls-files", "-s", "-z"); got != index {
		t.Fatal("plan changed the index")
	}
	groups := proposeSplitGroups([]string{"internal/store/store.go", "internal/store/store_test.go", "go.mod", "go.sum", "README.md", "THREAT-MODEL.md", ".github/workflows/ci.yml"})
	if len(groups) != 4 || groups[0].Key != ".github/workflows" || groups[1].Key != "dependencies" || groups[2].Key != "internal/store" || groups[3].Key != "repository-docs" {
		t.Fatalf("component proposals = %#v", groups)
	}
	if groups[0].SuggestedPrefix != "ci(ci): " || groups[1].SuggestedPrefix != "build(deps): " || groups[2].SuggestedPrefix != "<type>(store): " || groups[3].SuggestedPrefix != "docs(docs): " {
		t.Fatalf("component naming hints = %#v", groups)
	}
	testGroups := proposeSplitGroups([]string{"internal/parser/parser_test.go", "internal/parser/testdata/input.txt"})
	if len(testGroups) != 1 || testGroups[0].SuggestedPrefix != "test(parser): " {
		t.Fatalf("test naming hint = %#v", testGroups)
	}
	unicodeGroups := proposeSplitGroups([]string{"cmd/Δ/handler.go"})
	if len(unicodeGroups) != 1 || unicodeGroups[0].SuggestedPrefix != "<type>(repo): " {
		t.Fatalf("unicode naming hint = %#v", unicodeGroups)
	}
	if err := engine.ValidateMessage(strings.Replace(unicodeGroups[0].SuggestedPrefix, "<type>", "fix", 1)+"handle request", false); err != nil {
		t.Fatalf("suggested prefix is not a valid Conventional Commit prefix: %v", err)
	}
}

func TestPlanPreviewExcludesAlreadyCapturedStagedPaths(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	writeCLI(t, directory, "docs.txt", "new docs\n")
	cliGit(t, directory, "add", "core.txt", "docs.txt")
	initializeCLI(t, directory, "incremental-plan", "core.txt", "docs.txt")
	planPath := filepath.Join(t.TempDir(), "plan.json")
	plan := `[
  {"message":"fix(core): capture core behavior","files":["core.txt"]},
  {"message":"docs(guide): capture docs behavior","files":["docs.txt"]}
]`
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	captured := runCLI(t, []string{"--json", "--repo-dir", directory, "commit", "--lane", "incremental-plan", "--plan", planPath}, "")
	if captured.code != 0 || !captured.envelope.OK {
		t.Fatalf("initial capture: %#v stderr=%s", captured.envelope, captured.stderr)
	}

	writeCLI(t, directory, "docs.txt", "newer docs\n")
	cliGit(t, directory, "add", "docs.txt")
	preview := runCLI(t, []string{"--json", "--repo-dir", directory, "plan", "--lane", "incremental-plan"}, "")
	if preview.code != 0 || !preview.envelope.OK {
		t.Fatalf("incremental preview: %#v stderr=%s", preview.envelope, preview.stderr)
	}
	var proposal planProposal
	decodeData(t, preview.envelope.Data, &proposal)
	if len(proposal.StagedPaths) != 1 || proposal.StagedPaths[0] != "docs.txt" || len(proposal.Groups) != 1 {
		t.Fatalf("incremental proposal = %#v", proposal)
	}
}

func TestInteractiveInitAndSplitPlannerKeepJSONClean(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	writeCLI(t, directory, "docs.txt", "new docs\n")
	cliGit(t, directory, "add", "core.txt", "docs.txt")
	skillDir := t.TempDir()
	input := "shared\n\n\n\ninteractive-lane\nyes\nyes\nyes\nyes\n"
	initialized := runCLI(t, []string{"--json", "--repo-dir", directory, "init", "--no-install", "--skill-dir", skillDir}, input)
	if initialized.code != 0 || !initialized.envelope.OK {
		t.Fatalf("interactive init: %#v stderr=%s stdout=%s", initialized.envelope, initialized.stderr, initialized.stdout)
	}
	if strings.Contains(initialized.stdout, "Lane mode") {
		t.Fatalf("prompt corrupted JSON stdout: %q", initialized.stdout)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "wip-commit", "SKILL.md")); err != nil {
		t.Fatalf("interactive skill install: %v", err)
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
	var worktreeDryRun initResult
	decodeData(t, result.envelope.Data, &worktreeDryRun)
	if worktreeDryRun.DiffCheckRun || worktreeDryRun.DiffCheckPassed {
		t.Fatalf("missing worktree diff check = %#v", worktreeDryRun)
	}
	if _, err := os.Lstat(linked); !os.IsNotExist(err) {
		t.Fatalf("dry-run created worktree: %v", err)
	}
	gitDir := cliGit(t, directory, "rev-parse", "--absolute-git-dir")
	if _, err := os.Lstat(filepath.Join(gitDir, "wip")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created WIP store: %v", err)
	}
	human := runCLI(t, []string{"--repo-dir", directory, "init", "--mode", "worktree", "--create-worktree", linked, "--lane", "dry-run-human", "--agent", "agent", "--session", "session", "--path", "core.txt", "--non-interactive", "--no-install", "--no-install-skill", "--dry-run"}, "")
	if human.code != 0 || !strings.Contains(human.stdout, "staged diff check did not run") {
		t.Fatalf("missing-worktree human output = %q, stderr=%q", human.stdout, human.stderr)
	}
	skillDir := filepath.Join(t.TempDir(), "skills")
	skillDryRun := runCLI(t, []string{"--json", "--repo-dir", directory, "init", "--mode", "shared", "--lane", "skill-dry-run", "--agent", "agent", "--session", "session", "--path", "core.txt", "--non-interactive", "--no-install", "--install-skill", "--skill-dir", skillDir, "--dry-run"}, "")
	if skillDryRun.code != 0 || !skillDryRun.envelope.OK {
		t.Fatalf("skill dry-run: %#v stderr=%s", skillDryRun.envelope, skillDryRun.stderr)
	}
	var sharedDryRun initResult
	decodeData(t, skillDryRun.envelope.Data, &sharedDryRun)
	if !sharedDryRun.DiffCheckRun || !sharedDryRun.DiffCheckPassed {
		t.Fatalf("shared checkout diff check = %#v", sharedDryRun)
	}
	if _, err := os.Lstat(portableSkillPath(skillDir)); !os.IsNotExist(err) {
		t.Fatalf("dry-run installed skill: %v", err)
	}
	installDir := t.TempDir()
	path, installed, valid, err := installSelf(installDir)
	if err != nil || !installed || valid {
		t.Fatalf("first install path=%s installed=%v valid=%v err=%v", path, installed, valid, err)
	}
	if entries, readErr := os.ReadDir(installDir); readErr != nil || len(entries) != 1 || entries[0].Name() != binaryName() {
		t.Fatalf("binary install left temporary entries: %#v, err=%v", entries, readErr)
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
	skillPath, installed, valid, err := installPortableSkill(skillDir)
	if err != nil || !installed || valid {
		t.Fatalf("first skill install path=%s installed=%v valid=%v err=%v", skillPath, installed, valid, err)
	}
	_, installed, valid, err = installPortableSkill(skillDir)
	if err != nil || installed || !valid {
		t.Fatalf("second skill install installed=%v valid=%v err=%v", installed, valid, err)
	}
	partialDir := t.TempDir()
	partialPath := portableSkillPath(partialDir)
	if err := os.Mkdir(partialPath, 0o755); err != nil {
		t.Fatal(err)
	}
	partialBody, err := skillbundle.FS.ReadFile("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partialPath, "SKILL.md"), partialBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, exists, valid, err := inspectPortableSkill(partialDir); err != nil || !exists || valid {
		t.Fatalf("partial skill state exists=%v valid=%v err=%v", exists, valid, err)
	}
	if _, installed, valid, err := installPortableSkill(partialDir); err != nil || !installed || valid {
		t.Fatalf("partial skill resume installed=%v valid=%v err=%v", installed, valid, err)
	}
	if _, _, valid, err := inspectPortableSkill(partialDir); err != nil || !valid {
		t.Fatalf("resumed skill valid=%v err=%v", valid, err)
	}
	skillFile := filepath.Join(skillPath, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("different skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := installPortableSkill(skillDir); fail.Code(err) != "SKILL_INSTALL_CONFLICT" {
		t.Fatalf("different skill error = %v (%s)", err, fail.Code(err))
	}
	if body, err := os.ReadFile(skillFile); err != nil || string(body) != "different skill\n" {
		t.Fatalf("skill conflict was overwritten: %q err=%v", body, err)
	}
}

func TestInitDryRunReportsWhitespaceAndFailsClosedOnGitInspectionError(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "trailing whitespace   \n")
	cliGit(t, directory, "add", "core.txt")
	args := []string{"--repo-dir", directory, "init", "--mode", "shared", "--lane", "diff-check", "--agent", "agent", "--session", "session", "--path", "core.txt", "--non-interactive", "--no-install", "--no-install-skill", "--dry-run"}
	jsonRun := runCLI(t, append([]string{"--json"}, args...), "")
	if jsonRun.code != 0 || !jsonRun.envelope.OK {
		t.Fatalf("whitespace dry-run = %#v stderr=%s", jsonRun.envelope, jsonRun.stderr)
	}
	var result initResult
	decodeData(t, jsonRun.envelope.Data, &result)
	if !result.DiffCheckRun || result.DiffCheckPassed {
		t.Fatalf("whitespace result = %#v", result)
	}
	humanRun := runCLI(t, args, "")
	if humanRun.code != 0 || !strings.Contains(humanRun.stdout, "found staged whitespace errors") {
		t.Fatalf("whitespace human output = %q, stderr=%q", humanRun.stdout, humanRun.stderr)
	}

	broken := cliTestRepo(t)
	index := cliGit(t, broken, "rev-parse", "--git-path", "index")
	if !filepath.IsAbs(index) {
		index = filepath.Join(broken, index)
	}
	if err := os.Rename(index, index+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(index, 0o700); err != nil {
		t.Fatal(err)
	}
	failed := runCLI(t, []string{"--json", "--repo-dir", broken, "init", "--mode", "shared", "--lane", "broken-index", "--agent", "agent", "--session", "session", "--path", "core.txt", "--non-interactive", "--no-install", "--no-install-skill", "--dry-run"}, "")
	if failed.code == 0 || failed.envelope.Error == nil || failed.envelope.Error.Code != "GIT_FAILED" {
		t.Fatalf("broken-index result = %#v stderr=%s", failed.envelope, failed.stderr)
	}
	gitDir := cliGit(t, broken, "rev-parse", "--absolute-git-dir")
	if _, err := os.Lstat(filepath.Join(gitDir, "wip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed dry-run initialized state: %v", err)
	}
}

func TestInitSkipsHomeResolutionWhenInstallationIsDisabled(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	options := initOptions{
		mode: string(store.ModeShared), lane: "no-home", agent: "agent", session: "session", baseRef: "HEAD",
		paths: stringList{"core.txt"}, nonInteractive: true, noInstall: true, noInstallSkill: true,
	}
	if err := prepareInitOptions(context.Background(), repo, &options, prompt{reader: bufio.NewReader(strings.NewReader("")), out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if options.installDir != "" || options.skillDir != "" {
		t.Fatalf("disabled installation resolved directories: %#v", options)
	}
}

func TestInteractiveInitCanSkipConflictingOptionalSkill(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	skillRoot := t.TempDir()
	target := portableSkillPath(skillRoot)
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "foreign.md"), []byte("foreign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := initOptions{mode: string(store.ModeShared), lane: "skip-skill", agent: "agent", session: "session", baseRef: "HEAD", paths: stringList{"core.txt"}, noInstall: true, skillDir: skillRoot}
	var output bytes.Buffer
	prompter := prompt{reader: bufio.NewReader(strings.NewReader("\n\n\n\n\n")), out: &output}
	if err := prepareInitOptions(context.Background(), repo, &options, prompter); err != nil {
		t.Fatal(err)
	}
	if !options.noInstallSkill || options.installSkill || !strings.Contains(output.String(), "Continue without portable skill installation") {
		t.Fatalf("conflicting optional skill options=%#v output=%q", options, output.String())
	}
	body, err := os.ReadFile(filepath.Join(target, "foreign.md"))
	if err != nil || string(body) != "foreign\n" {
		t.Fatalf("conflicting skill changed: %q err=%v", body, err)
	}
}

func TestInitFailureReturnsDurableResumableReceipt(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := laneStore.Create(context.Background(), store.CreateOptions{ID: "partial-init", Agent: "agent", Session: "session", Mode: store.ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	path, err := profilePath(laneStore, lane.ID)
	if err != nil {
		t.Fatal(err)
	}
	conflict := profile{SchemaVersion: profileSchemaVersion, Lane: lane.ID, Agent: "other", Session: lane.Session, Mode: lane.Mode, Worktree: lane.Worktree}
	body, err := json.MarshalIndent(conflict, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runCLI(t, []string{"--json", "--repo-dir", directory, "init", "--mode", "shared", "--lane", lane.ID, "--agent", lane.Agent, "--session", lane.Session, "--path", "core.txt", "--non-interactive", "--no-install", "--no-install-skill"}, "")
	if result.code == 0 || result.envelope.Error == nil || result.envelope.Error.Code != "PROFILE_READ_FAILED" {
		t.Fatalf("partial init result = %#v stderr=%s", result.envelope, result.stderr)
	}
	var partial initResult
	decodeData(t, result.envelope.Data, &partial)
	if partial.IntentID == "" || partial.IntentPath == "" || partial.IntentState != "pending" || len(partial.CompletedSteps) != 3 || len(partial.Recovery) != 3 {
		t.Fatalf("partial receipt = %#v", partial)
	}
	intent, err := loadInitIntent(partial.IntentPath)
	if err != nil || intent.State != "pending" || len(intent.CompletedSteps) != 3 {
		t.Fatalf("durable partial intent = %#v err=%v", intent, err)
	}
	if got := cliGit(t, directory, "rev-parse", lane.Ref); got != lane.BaseSHA {
		t.Fatalf("partial init moved lane ref: %s", got)
	}
	doctor := runCLI(t, []string{"--json", "--repo-dir", directory, "doctor"}, "")
	if doctor.code != 1 || !doctor.envelope.OK {
		t.Fatalf("doctor did not report resumable init: %#v stderr=%s", doctor.envelope, doctor.stderr)
	}
	var report doctorReport
	decodeData(t, doctor.envelope.Data, &report)
	if report.Healthy || len(report.PendingInit) != 1 || report.PendingInit[0] != partial.IntentID {
		t.Fatalf("doctor partial report = %#v", report)
	}
}

func TestInitRetryRepairsInterruptedLeaseBackReference(t *testing.T) {
	directory := cliTestRepo(t)
	args := []string{"--json", "--repo-dir", directory, "init", "--mode", "shared", "--lane", "init-claim-retry", "--agent", "agent", "--session", "session", "--path", "core.txt", "--non-interactive", "--no-install", "--no-install-skill"}
	first := runCLI(t, args, "")
	if first.code != 0 || !first.envelope.OK {
		t.Fatalf("first init = %#v stderr=%s", first.envelope, first.stderr)
	}
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	status, err := laneStore.Status("init-claim-retry")
	if err != nil || len(status.Leases) != 1 {
		t.Fatalf("initial status = %#v, err=%v", status, err)
	}
	lane := status.Lane
	lane.LeaseIDs = nil
	body, err := json.MarshalIndent(lane, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(laneStore.Root, "lanes", lane.ID+".json"), append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	second := runCLI(t, args, "")
	if second.code != 0 || !second.envelope.OK {
		t.Fatalf("recovery init = %#v stderr=%s", second.envelope, second.stderr)
	}
	status, err = laneStore.Status(lane.ID)
	if err != nil || len(status.Leases) != 1 || len(status.Lane.LeaseIDs) != 1 || status.Lane.LeaseIDs[0] != status.Leases[0].ID {
		t.Fatalf("repaired status = %#v, err=%v", status, err)
	}
	doctor := runCLI(t, []string{"--json", "--repo-dir", directory, "doctor"}, "")
	if doctor.code != 0 || !doctor.envelope.OK {
		t.Fatalf("doctor after repair = %#v stderr=%s", doctor.envelope, doctor.stderr)
	}
	var report doctorReport
	decodeData(t, doctor.envelope.Data, &report)
	if !report.Healthy {
		t.Fatalf("doctor after repair = %#v", report)
	}
}

func TestOversizedInitIntentFailsBeforeRecordPublication(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 10_000)
	for index := range paths {
		paths[index] = fmt.Sprintf("generated/%04d/%0140d.txt", index, index)
	}
	candidate := initIntent{
		Lane: "oversized-init", Agent: "agent", Session: "session", Mode: store.ModeShared,
		BaseSHA: cliGit(t, directory, "rev-parse", "HEAD"), Worktree: repo.Root, Paths: paths,
	}
	if _, path, err := beginInitIntent(laneStore, candidate); fail.Code(err) != "INIT_INTENT_FAILED" {
		t.Fatalf("oversized init intent path=%s error=%v (%s)", path, err, fail.Code(err))
	}
	entries, err := os.ReadDir(filepath.Join(laneStore.Root, "init-intents"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("oversized init intent records = %#v, err=%v", entries, err)
	}
}

func TestInitIntentReservesSpaceForEveryCompletionStep(t *testing.T) {
	timestamp := maximumInitIntentTimestamp()
	intent := initIntent{
		SchemaVersion: initIntentSchemaVersion, ID: "init-capacity", Digest: "sha256:" + strings.Repeat("a", 64), State: "pending",
		Lane: "capacity", Agent: "agent", Session: "session", Mode: store.ModeShared,
		BaseSHA: strings.Repeat("b", 40), Worktree: "/tmp/capacity", Paths: []string{"x"}, CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	pending, err := marshalInitIntentRecord(intent)
	if err != nil {
		t.Fatal(err)
	}
	delta := int(maxInitIntentBytes) - 1 - len(pending)
	if delta <= 0 {
		t.Fatalf("unexpected pending record size %d", len(pending))
	}
	intent.Paths[0] = strings.Repeat("x", delta+1)
	if _, err := marshalInitIntentRecord(intent); err != nil {
		t.Fatalf("pending form does not fit: %v", err)
	}
	if err := validateInitIntentCapacity(intent); fail.Code(err) != "INIT_INTENT_FAILED" {
		t.Fatalf("completion capacity error = %v (%s)", err, fail.Code(err))
	}
}

func TestDoctorIsReadOnlyAndAuditsHealthyState(t *testing.T) {
	directory := cliTestRepo(t)
	gitDir := cliGit(t, directory, "rev-parse", "--absolute-git-dir")
	empty := runCLI(t, []string{"--json", "--repo-dir", directory, "doctor"}, "")
	if empty.code != 0 || !empty.envelope.OK {
		t.Fatalf("empty doctor: %#v stderr=%s", empty.envelope, empty.stderr)
	}
	var emptyReport doctorReport
	decodeData(t, empty.envelope.Data, &emptyReport)
	if !emptyReport.Healthy || emptyReport.State != "not-initialized" {
		t.Fatalf("empty doctor report = %#v", emptyReport)
	}
	if _, err := os.Lstat(filepath.Join(gitDir, "wip")); !os.IsNotExist(err) {
		t.Fatalf("doctor created state: %v", err)
	}
	initializeCLI(t, directory, "doctor-lane", "core.txt")
	healthy := runCLI(t, []string{"--json", "--repo-dir", directory, "doctor"}, "")
	if healthy.code != 0 || !healthy.envelope.OK {
		t.Fatalf("healthy doctor: %#v stderr=%s", healthy.envelope, healthy.stderr)
	}
	var report doctorReport
	decodeData(t, healthy.envelope.Data, &report)
	if !report.Healthy || report.State != "initialized" || len(report.ActiveLanes) != 1 || report.Counts["lanes"] != 1 || report.Counts["init_intents"] != 1 {
		t.Fatalf("healthy doctor report = %#v", report)
	}
}

func TestReadOnlyLaneCommandsDoNotClaimPublicDomain(t *testing.T) {
	for _, command := range [][]string{{"status"}, {"env", "--lane", "missing"}, {"plan"}} {
		name := command[0]
		t.Run(name, func(t *testing.T) {
			directory := cliTestRepo(t)
			result := runCLI(t, append([]string{"--json", "--repo-dir", directory}, command...), "")
			if result.code != 1 || result.envelope.OK || result.envelope.Error == nil || result.envelope.Error.Code != "LANE_NOT_ACTIVE" {
				t.Fatalf("read-only %s result = %#v stderr=%s", name, result.envelope, result.stderr)
			}
			gitDir := cliGit(t, directory, "rev-parse", "--absolute-git-dir")
			for _, path := range []string{filepath.Join(gitDir, "wip"), filepath.Join(gitDir, "wip-coordination.lock")} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("read-only %s created %s: %v", name, path, err)
				}
			}
		})
	}
}

func TestOnlyInitCanClaimAnUninitializedPublicDomain(t *testing.T) {
	tests := []struct {
		name, wantedError string
		wantedCode        int
		command           []string
	}{
		{name: "claim", wantedError: "LANE_NOT_ACTIVE", wantedCode: 1, command: []string{"claim"}},
		{name: "renew", wantedError: "LANE_NOT_ACTIVE", wantedCode: 1, command: []string{"renew"}},
		{name: "commit", wantedError: "LANE_NOT_ACTIVE", wantedCode: 1, command: []string{"commit"}},
		{name: "reconcile", wantedError: "LANE_NOT_ACTIVE", wantedCode: 1, command: []string{"reconcile"}},
		{name: "release", wantedError: "LANE_NOT_ACTIVE", wantedCode: 1, command: []string{"release"}},
		{name: "abort", wantedError: "LANE_NOT_ACTIVE", wantedCode: 1, command: []string{"abort"}},
		{name: "unknown", wantedError: "INVALID_ARGS", wantedCode: 2, command: []string{"not-a-command"}},
		{name: "archive resume", wantedError: "ARCHIVE_NOT_FOUND", wantedCode: 1, command: []string{"archive", "--resume", "archive-000000000000000000000000", "--apply", "--yes"}},
		{name: "archive restore", wantedError: "ARCHIVE_NOT_FOUND", wantedCode: 1, command: []string{"archive", "--restore", "archive-000000000000000000000000", "--apply", "--yes"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := cliTestRepo(t)
			result := runCLI(t, append([]string{"--json", "--repo-dir", directory}, test.command...), "")
			if result.code != test.wantedCode || result.envelope.OK || result.envelope.Error == nil || result.envelope.Error.Code != test.wantedError {
				t.Fatalf("uninitialized %s result = %#v stderr=%s", test.name, result.envelope, result.stderr)
			}
			gitDir := cliGit(t, directory, "rev-parse", "--absolute-git-dir")
			for _, path := range []string{filepath.Join(gitDir, "wip"), filepath.Join(gitDir, "wip-coordination.lock")} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("uninitialized %s created %s: %v", test.name, path, err)
				}
			}
		})
	}
}

func TestDoctorAcceptsMissingAdditiveV1DirectoriesWithoutCreatingThem(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"init-intents", "archive"} {
		if err := os.Remove(filepath.Join(laneStore.Root, name)); err != nil {
			t.Fatal(err)
		}
	}
	doctor := runCLI(t, []string{"--json", "--repo-dir", directory, "doctor"}, "")
	if doctor.code != 0 || !doctor.envelope.OK {
		t.Fatalf("additive-directory doctor: %#v stderr=%s", doctor.envelope, doctor.stderr)
	}
	var report doctorReport
	decodeData(t, doctor.envelope.Data, &report)
	if !report.Healthy || report.State != "initialized" {
		t.Fatalf("additive-directory report = %#v", report)
	}
	for _, name := range []string{"init-intents", "archive"} {
		if _, err := os.Lstat(filepath.Join(laneStore.Root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("doctor created optional directory %s: %v", name, err)
		}
	}
}

func TestDoctorRejectsLeaseOwnerMismatch(t *testing.T) {
	directory := cliTestRepo(t)
	initializeCLI(t, directory, "doctor-owner", "core.txt")
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(laneStore.Root, "leases"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("lease entries = %#v, err=%v", entries, err)
	}
	leasePath := filepath.Join(laneStore.Root, "leases", entries[0].Name())
	body, err := os.ReadFile(leasePath)
	if err != nil {
		t.Fatal(err)
	}
	var lease store.Lease
	if err := json.Unmarshal(body, &lease); err != nil {
		t.Fatal(err)
	}
	lease.Agent = "different-agent"
	body, err = json.MarshalIndent(lease, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leasePath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	doctor := runCLI(t, []string{"--json", "--repo-dir", directory, "doctor"}, "")
	if doctor.code != 1 || !doctor.envelope.OK {
		t.Fatalf("doctor mismatch: %#v stderr=%s", doctor.envelope, doctor.stderr)
	}
	var report doctorReport
	decodeData(t, doctor.envelope.Data, &report)
	found := false
	for _, finding := range report.Findings {
		if finding.Code == "LEASE_OWNER_MISMATCH" {
			found = true
		}
	}
	if report.Healthy || !found {
		t.Fatalf("doctor mismatch report = %#v", report)
	}
}

func TestArchiveRequiresExactPreviewAndPreservesRefs(t *testing.T) {
	directory := cliTestRepo(t)
	initializeCLI(t, directory, "archive-cli", "core.txt")
	release := runCLI(t, []string{"--json", "--repo-dir", directory, "release", "--lane", "archive-cli"}, "")
	if release.code != 0 || !release.envelope.OK {
		t.Fatalf("release for archive: %#v stderr=%s", release.envelope, release.stderr)
	}
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	lane, err := laneStore.Load("archive-cli")
	if err != nil {
		t.Fatal(err)
	}
	lane.UpdatedAt = time.Now().UTC().Add(-48 * time.Hour)
	body, err := json.MarshalIndent(lane, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(laneStore.Root, "lanes", lane.ID+".json"), append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	refBefore := cliGit(t, directory, "rev-parse", lane.Ref)
	preview := runCLI(t, []string{"--json", "--repo-dir", directory, "archive", "--older-than", "24h"}, "")
	if preview.code != 0 || !preview.envelope.OK {
		t.Fatalf("archive preview: %#v stderr=%s", preview.envelope, preview.stderr)
	}
	var plan archiveResult
	decodeData(t, preview.envelope.Data, &plan)
	if !plan.DryRun || len(plan.Candidates) != 1 || plan.PlanDigest == "" {
		t.Fatalf("archive plan = %#v", plan)
	}
	missing := runCLI(t, []string{"--json", "--repo-dir", directory, "archive", "--older-than", "24h", "--lane", "not-eligible"}, "")
	if missing.code == 0 || missing.envelope.Error == nil || missing.envelope.Error.Code != "ARCHIVE_REFUSED" {
		t.Fatalf("missing archive lane = %#v", missing.envelope)
	}
	wrong := runCLI(t, []string{"--json", "--repo-dir", directory, "archive", "--cutoff", plan.Cutoff.Format(time.RFC3339Nano), "--plan-digest", "sha256:wrong", "--apply", "--yes"}, "")
	if wrong.code == 0 || wrong.envelope.Error == nil || wrong.envelope.Error.Code != "ARCHIVE_PLAN_MOVED" {
		t.Fatalf("wrong archive digest = %#v", wrong.envelope)
	}
	if _, err := laneStore.Load(lane.ID); err != nil {
		t.Fatalf("wrong archive digest moved state: %v", err)
	}
	applied := runCLI(t, []string{"--json", "--repo-dir", directory, "archive", "--cutoff", plan.Cutoff.Format(time.RFC3339Nano), "--plan-digest", plan.PlanDigest, "--apply", "--yes"}, "")
	if applied.code != 0 || !applied.envelope.OK {
		t.Fatalf("archive apply: %#v stderr=%s", applied.envelope, applied.stderr)
	}
	var archived archiveResult
	decodeData(t, applied.envelope.Data, &archived)
	if archived.Receipt == nil || archived.Receipt.State != "complete" {
		t.Fatalf("archive receipt = %#v", archived)
	}
	receiptPath := filepath.Join(laneStore.Root, "archive", archived.Receipt.ID, "receipt.json")
	archived.Receipt.State = "prepared"
	receiptBody, err := json.MarshalIndent(archived.Receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, append(receiptBody, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	doctor := runCLI(t, []string{"--json", "--repo-dir", directory, "doctor"}, "")
	if doctor.code != 1 || !doctor.envelope.OK {
		t.Fatalf("doctor prepared archive: %#v stderr=%s", doctor.envelope, doctor.stderr)
	}
	var doctorState doctorReport
	decodeData(t, doctor.envelope.Data, &doctorState)
	if len(doctorState.PendingArchive) != 1 || doctorState.PendingArchive[0] != archived.Receipt.ID {
		t.Fatalf("doctor prepared archive report = %#v", doctorState)
	}
	resumed := runCLI(t, []string{"--json", "--repo-dir", directory, "archive", "--resume", archived.Receipt.ID, "--apply", "--yes"}, "")
	if resumed.code != 0 || !resumed.envelope.OK {
		t.Fatalf("idempotent archive resume: %#v stderr=%s", resumed.envelope, resumed.stderr)
	}
	if _, err := laneStore.Load(lane.ID); fail.Code(err) != "LANE_NOT_FOUND" {
		t.Fatalf("archive left live lane: %v (%s)", err, fail.Code(err))
	}
	if got := cliGit(t, directory, "rev-parse", lane.Ref); got != refBefore {
		t.Fatalf("archive moved ref: %s -> %s", refBefore, got)
	}
	restored := runCLI(t, []string{"--json", "--repo-dir", directory, "archive", "--restore", archived.Receipt.ID, "--apply", "--yes"}, "")
	if restored.code != 0 || !restored.envelope.OK {
		t.Fatalf("archive restore: %#v stderr=%s", restored.envelope, restored.stderr)
	}
	if lane, err := laneStore.Load(lane.ID); err != nil || lane.State != "released" {
		t.Fatalf("restored lane = %#v err=%v", lane, err)
	}
	if got := cliGit(t, directory, "rev-parse", lane.Ref); got != refBefore {
		t.Fatalf("restore moved ref: %s -> %s", refBefore, got)
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
	path, err := profilePath(laneStore, saved.Lane)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runCLI(t, []string{"--json", "--repo-dir", directory, "status", "--lane", saved.Lane}, "")
	if result.code == 0 || result.envelope.Error == nil || result.envelope.Error.Code != "MIGRATION_REQUIRED" {
		t.Fatalf("future profile result = %#v stderr=%s", result.envelope, result.stderr)
	}
}

func TestFutureInitIntentSchemaRequiresMigration(t *testing.T) {
	directory := cliTestRepo(t)
	result := runCLI(t, []string{"--json", "--repo-dir", directory, "init", "--mode", "shared", "--lane", "future-init", "--agent", "agent", "--session", "session", "--path", "core.txt", "--non-interactive", "--no-install", "--no-install-skill"}, "")
	if result.code != 0 || !result.envelope.OK {
		t.Fatalf("init: %#v stderr=%s", result.envelope, result.stderr)
	}
	var setup initResult
	decodeData(t, result.envelope.Data, &setup)
	intent, err := loadInitIntent(setup.IntentPath)
	if err != nil {
		t.Fatal(err)
	}
	intent.SchemaVersion = initIntentSchemaVersion + 1
	body, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(setup.IntentPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadInitIntent(setup.IntentPath); fail.Code(err) != "MIGRATION_REQUIRED" {
		t.Fatalf("future init intent error = %v (%s)", err, fail.Code(err))
	}
}

func TestProfileIdentityAndShellOutputFailClosed(t *testing.T) {
	directory := cliTestRepo(t)
	initializeCLI(t, directory, "profile-guard", "core.txt")
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadProfile(laneStore, "../../outside"); fail.Code(err) != "INVALID_ID" {
		t.Fatalf("traversal profile error = %v (%s)", err, fail.Code(err))
	}
	path, err := profilePath(laneStore, "profile-guard")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := loadProfile(laneStore, "profile-guard")
	if err != nil {
		t.Fatal(err)
	}
	saved.Agent = "different-agent"
	body, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProfile(laneStore, "profile-guard"); fail.Code(err) != "PROFILE_READ_FAILED" {
		t.Fatalf("profile/manifest mismatch error = %v (%s)", err, fail.Code(err))
	}
	if got := quotePOSIX("agent'; touch /tmp/nope; '"); got != `'agent'"'"'; touch /tmp/nope; '"'"''` {
		t.Fatalf("POSIX quote = %q", got)
	}
	if got := quotePowerShell("agent'; Write-Output nope; '"); got != `'agent''; Write-Output nope; '''` {
		t.Fatalf("PowerShell quote = %q", got)
	}
}

func TestInitRecoveryQuotesEveryDynamicArgument(t *testing.T) {
	options := initOptions{
		mode:           string(store.ModeShared),
		lane:           "safe-lane",
		agent:          "safe-agent",
		session:        "safe-session",
		baseRef:        "topic branch",
		paths:          []string{"dir/has space/file.txt", "dir/has'quote.txt"},
		createWorktree: filepath.Join(t.TempDir(), "linked worktree"),
		install:        true,
		installDir:     filepath.Join(t.TempDir(), "bin dir"),
		installSkill:   true,
		skillDir:       filepath.Join(t.TempDir(), "skill dir"),
	}
	result := initResult{RepoDir: filepath.Join(t.TempDir(), "repo dir"), BaseSHA: strings.Repeat("a", 40), ClaimedPaths: []string{"canonical path/file.txt", "canonical'quote.txt"}, CompletedSteps: []string{"worktree-ready"}}
	recovery := initRecovery(result, options)
	if len(recovery) != 3 {
		t.Fatalf("recovery = %#v", recovery)
	}
	command := recovery[2]
	for _, wanted := range []string{
		"--repo-dir " + quoteCommandArg(result.RepoDir),
		"--base-ref " + quoteCommandArg(result.BaseSHA),
		"--path " + quoteCommandArg(result.ClaimedPaths[0]),
		"--path " + quoteCommandArg(result.ClaimedPaths[1]),
		"--create-worktree " + quoteCommandArg(options.createWorktree),
		"--install-dir " + quoteCommandArg(options.installDir),
		"--skill-dir " + quoteCommandArg(options.skillDir),
	} {
		if !strings.Contains(command, wanted) {
			t.Fatalf("recovery command %q does not contain %q", command, wanted)
		}
	}
}

func TestInitIntentRejectsNonCanonicalGitPaths(t *testing.T) {
	base := initIntent{
		SchemaVersion: initIntentSchemaVersion,
		ID:            "init-valid",
		Lane:          "lane",
		Agent:         "agent",
		Session:       "session",
		Mode:          store.ModeShared,
		BaseSHA:       strings.Repeat("a", 40),
		Worktree:      t.TempDir(),
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	for _, path := range []string{"../escape", "dir/../../escape", "dir/../escape", ".", "dir//file"} {
		t.Run(strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			intent := base
			intent.Paths = []string{path}
			if err := validateInitIntent(intent); fail.Code(err) != "INIT_INTENT_FAILED" {
				t.Fatalf("path %q error = %v (%s)", path, err, fail.Code(err))
			}
		})
	}
}

func TestInitIntentRequiresAnExactStepPrefix(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	base := cliGit(t, directory, "rev-parse", "HEAD")
	intent, path, err := beginInitIntent(laneStore, initIntent{Lane: "strict-steps", Agent: "agent", Session: "session", Mode: store.ModeShared, BaseSHA: base, Worktree: repo.Root, Paths: []string{"core.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := markInitStep(path, intent, "lane-ready"); fail.Code(err) != "INIT_INTENT_FAILED" {
		t.Fatalf("out-of-order step error = %v (%s)", err, fail.Code(err))
	}
	intent.CompletedSteps = []string{"lane-ready"}
	if err := writeInitIntent(path, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := loadInitIntent(path); fail.Code(err) != "INIT_INTENT_FAILED" {
		t.Fatalf("gapped step history error = %v (%s)", err, fail.Code(err))
	}
}

func TestConcurrentInitIntentProgressIsMonotonic(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	base := cliGit(t, directory, "rev-parse", "HEAD")
	intent, path, err := beginInitIntent(laneStore, initIntent{Lane: "concurrent-init", Agent: "agent", Session: "session", Mode: store.ModeShared, BaseSHA: base, Worktree: repo.Root, Paths: []string{"core.txt"}})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 24
	start := make(chan struct{})
	errorsFound := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			current := intent
			for _, step := range initStepOrder {
				updated, markErr := markInitStep(path, current, step)
				if markErr != nil {
					errorsFound <- markErr
					return
				}
				current = updated
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for markErr := range errorsFound {
		t.Errorf("concurrent progress: %v", markErr)
	}
	final, err := loadInitIntent(path)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != "complete" || len(final.CompletedSteps) != len(initStepOrder) {
		t.Fatalf("final concurrent intent = %#v", final)
	}
}

func TestLeaseHeartbeatKeepsLongCaptureFenceActive(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	laneStore.LeaseTTL = 3 * time.Second
	lane, err := laneStore.Create(context.Background(), store.CreateOptions{ID: "heartbeat", Agent: "agent", Session: "session", Mode: store.ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, []string{"core.txt"}); err != nil {
		t.Fatal(err)
	}
	other, err := laneStore.Create(context.Background(), store.CreateOptions{ID: "heartbeat-other", Agent: "other", Session: "session", Mode: store.ModeShared})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := laneStore.LaneLock(lane.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	_, stop, err := startLeaseHeartbeat(context.Background(), laneStore, lane, []string{"core.txt"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)
	if active, err := laneStore.ActivePaths(lane.ID); err != nil || len(active) != 1 || active[0] != "core.txt" {
		t.Fatalf("heartbeat active paths = %v, err=%v", active, err)
	}
	if _, err := laneStore.Claim(other.ID, other.Agent, other.Session, []string{"core.txt"}); fail.Code(err) != "PATH_LEASE_CONFLICT" {
		t.Fatalf("heartbeat did not fence competing claim: %v (%s)", err, fail.Code(err))
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestLateHeartbeatFailureReturnsAppliedReceiptForReconciliation(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	cliGit(t, directory, "add", "core.txt")
	initializeCLI(t, directory, "heartbeat-recovery", "core.txt")
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	application := app{
		stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr, jsonMode: true,
		leaseHeartbeat: func(parent context.Context, _ store.Store, _ store.Lane, _ []string) (context.Context, func() error, error) {
			return parent, func() error {
				return fail.New("LEASE_HEARTBEAT_FAILED", "simulated late heartbeat failure")
			}, nil
		},
	}
	code := application.runCommit(context.Background(), laneStore, []string{"--lane", "heartbeat-recovery", "--single", "--message", "fix(lease): preserve late failure receipt", "--path", "core.txt"})
	var output envelope
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode output %q: %v; stderr=%s", stdout.String(), err, stderr.String())
	}
	if code != 1 || output.OK || output.Error == nil || output.Error.Code != "LEASE_HEARTBEAT_FAILED" {
		t.Fatalf("late heartbeat output = %#v, code=%d stderr=%s", output, code, stderr.String())
	}
	var result engine.Result
	decodeData(t, output.Data, &result)
	if !result.RefUpdated || result.IntentState != "applied" || result.PlanID == "" || result.PlanDigest == "" {
		t.Fatalf("late heartbeat receipt = %#v", result)
	}
	reconciled := runCLI(t, []string{"--json", "--repo-dir", directory, "reconcile", "--lane", "heartbeat-recovery", "--plan-id", result.PlanID, "--plan-digest", result.PlanDigest}, "")
	if reconciled.code != 0 || !reconciled.envelope.OK {
		t.Fatalf("reconcile late heartbeat = %#v stderr=%s", reconciled.envelope, reconciled.stderr)
	}
}

type cliRun struct {
	code           int
	stdout, stderr string
	envelope       envelope
}

func runCLI(t *testing.T, args []string, input string) cliRun {
	t.Helper()
	// The in-process CLI reads these defaults from the process environment.
	// Keep a maintainer's active capture identity out of disposable fixtures.
	for _, name := range []string{"WIP_LANE", "WIP_AGENT", "WIP_SESSION"} {
		t.Setenv(name, "")
	}
	return runCLIWithAmbientIdentity(t, args, input)
}

func runCLIWithAmbientIdentity(t *testing.T, args []string, input string) cliRun {
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
