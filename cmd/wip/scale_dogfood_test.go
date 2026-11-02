package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCaptureSixHundredTrackedPathsWithoutChangingCallerState(t *testing.T) {
	const fileCount = 600
	directory := cliTestRepo(t)
	paths := make([]string, 0, fileCount)
	for index := range fileCount {
		name := fmt.Sprintf("scale/file-%03d.txt", index)
		paths = append(paths, name)
		writeCLI(t, directory, name, fmt.Sprintf("base %03d\n", index))
	}
	cliGit(t, directory, "add", "scale")
	cliGit(t, directory, "commit", "-m", "test: add scale fixture")

	headBefore := cliGit(t, directory, "rev-parse", "HEAD")
	indexBefore := cliGitRaw(t, directory, "ls-files", "-s", "-z")
	for index, name := range paths {
		writeCLI(t, directory, name, fmt.Sprintf("captured %03d\n", index))
	}
	initializeCLI(t, directory, "scale-600", paths...)

	planPath := filepath.Join(t.TempDir(), "scale-600.plan.json")
	drafted := runCLI(t, []string{
		"--json", "--repo-dir", directory, "plan", "--lane", "scale-600",
		"--source", "worktree_snapshot", "--untracked", "forbid", "--out", planPath,
	}, "")
	if drafted.code != 0 || !drafted.envelope.OK {
		t.Fatalf("draft scale plan = %#v stderr=%s", drafted.envelope, drafted.stderr)
	}
	var plan planProposal
	decodeData(t, drafted.envelope.Data, &plan)
	if len(plan.SourcePaths) != fileCount || !slices.Equal(plan.SourcePaths, paths) {
		t.Fatalf("scale plan paths = %d, want %d exact paths", len(plan.SourcePaths), fileCount)
	}
	plan.State = "reviewed"
	for index := range plan.Groups {
		plan.Groups[index].Message = "test(scale): capture six hundred exact paths"
	}
	writeReviewedProposal(t, planPath, plan)
	reviewDigest, err := reviewPlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}

	captured := runCLI(t, []string{
		"--json", "--repo-dir", directory, "capture", "--lane", "scale-600",
		"--plan", planPath, "--review-plan-digest", reviewDigest, "--final", "--lock-wait", "0",
	}, "")
	if captured.code != 0 || !captured.envelope.OK {
		t.Fatalf("capture scale plan = %#v stderr=%s", captured.envelope, captured.stderr)
	}
	var result guidedCaptureResult
	decodeData(t, captured.envelope.Data, &result)
	if !result.LaneReleased || !result.Capture.RefUpdated || result.Capture.GateOutcome != "passed" {
		t.Fatalf("scale capture result = %#v", result)
	}
	if len(result.Capture.SourcePaths) != fileCount || len(result.Capture.RequestedPaths) != fileCount {
		t.Fatalf("scale capture path counts = source %d requested %d", len(result.Capture.SourcePaths), len(result.Capture.RequestedPaths))
	}
	if !result.Handoff.CaptureOnly || result.Handoff.LandingAuthorized || !result.Handoff.ReviewEligible {
		t.Fatalf("scale handoff authority = %#v", result.Handoff)
	}

	changed := strings.Split(cliGit(t, directory, "diff-tree", "--no-commit-id", "--name-only", "-r", result.Capture.FinalCommit), "\n")
	if len(changed) != fileCount || !slices.Equal(changed, paths) {
		t.Fatalf("captured tree changed %d paths, want %d exact paths", len(changed), fileCount)
	}
	if got := cliGit(t, directory, "show", result.Capture.FinalCommit+":"+paths[0]); got != "captured 000" {
		t.Fatalf("first captured path = %q", got)
	}
	if got := cliGit(t, directory, "show", result.Capture.FinalCommit+":"+paths[fileCount-1]); got != "captured 599" {
		t.Fatalf("last captured path = %q", got)
	}
	if got := cliGit(t, directory, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("source HEAD moved: before=%s after=%s", headBefore, got)
	}
	if got := cliGitRaw(t, directory, "ls-files", "-s", "-z"); got != indexBefore {
		t.Fatal("600-path capture changed the caller index")
	}
}

func TestThreeDedicatedWorktreesCaptureIndependently(t *testing.T) {
	anchor := cliTestRepo(t)
	paths := []string{"lanes/one.txt", "lanes/two.txt", "lanes/three.txt"}
	for index, name := range paths {
		writeCLI(t, anchor, name, fmt.Sprintf("base %d\n", index+1))
	}
	cliGit(t, anchor, "add", "lanes")
	cliGit(t, anchor, "commit", "-m", "test: add worktree dogfood fixture")

	headBefore := cliGit(t, anchor, "rev-parse", "HEAD")
	anchorIndexBefore := cliGitRaw(t, anchor, "ls-files", "-s", "-z")
	tips := make([]string, 0, len(paths))
	for index, name := range paths {
		lane := fmt.Sprintf("dedicated-%d", index+1)
		worktree := filepath.Join(t.TempDir(), lane)
		cliGit(t, anchor, "worktree", "add", "--detach", worktree, headBefore)
		worktreeIndexBefore := cliGitRaw(t, worktree, "ls-files", "-s", "-z")
		writeCLI(t, worktree, name, fmt.Sprintf("captured %d\n", index+1))

		initialized := runCLI(t, []string{
			"--json", "--repo-dir", worktree, "init", "--mode", "worktree",
			"--lane", lane, "--agent", "agent", "--session", "session",
			"--path", name, "--non-interactive", "--no-install", "--no-install-skill",
		}, "")
		if initialized.code != 0 || !initialized.envelope.OK {
			t.Fatalf("initialize %s = %#v stderr=%s", lane, initialized.envelope, initialized.stderr)
		}

		planPath := filepath.Join(t.TempDir(), lane+".plan.json")
		drafted := runCLI(t, []string{
			"--json", "--repo-dir", worktree, "plan", "--lane", lane,
			"--source", "worktree_snapshot", "--untracked", "forbid", "--out", planPath,
		}, "")
		if drafted.code != 0 || !drafted.envelope.OK {
			t.Fatalf("draft %s = %#v stderr=%s", lane, drafted.envelope, drafted.stderr)
		}
		var plan planProposal
		decodeData(t, drafted.envelope.Data, &plan)
		plan.State = "reviewed"
		for group := range plan.Groups {
			plan.Groups[group].Message = fmt.Sprintf("test(worktree): capture dedicated lane %d", index+1)
		}
		writeReviewedProposal(t, planPath, plan)
		reviewDigest, err := reviewPlanDigest(plan)
		if err != nil {
			t.Fatal(err)
		}

		captured := runCLI(t, []string{
			"--json", "--repo-dir", worktree, "capture", "--lane", lane,
			"--plan", planPath, "--review-plan-digest", reviewDigest, "--final", "--lock-wait", "0",
		}, "")
		if captured.code != 0 || !captured.envelope.OK {
			t.Fatalf("capture %s = %#v stderr=%s", lane, captured.envelope, captured.stderr)
		}
		var result guidedCaptureResult
		decodeData(t, captured.envelope.Data, &result)
		if !result.LaneReleased || !result.Handoff.CaptureOnly || result.Handoff.LandingAuthorized {
			t.Fatalf("dedicated handoff %s = %#v", lane, result)
		}
		if got := cliGit(t, worktree, "show", result.Capture.FinalCommit+":"+name); got != fmt.Sprintf("captured %d", index+1) {
			t.Fatalf("dedicated capture %s content = %q", lane, got)
		}
		if got := cliGit(t, worktree, "rev-parse", "HEAD"); got != headBefore {
			t.Fatalf("dedicated worktree %s HEAD moved: %s", lane, got)
		}
		if got := cliGitRaw(t, worktree, "ls-files", "-s", "-z"); got != worktreeIndexBefore {
			t.Fatalf("dedicated worktree %s index changed", lane)
		}
		tips = append(tips, result.Capture.FinalCommit)
	}
	if tips[0] == tips[1] || tips[0] == tips[2] || tips[1] == tips[2] {
		t.Fatalf("dedicated lanes did not produce distinct tips: %v", tips)
	}
	if got := cliGit(t, anchor, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("anchor HEAD moved: before=%s after=%s", headBefore, got)
	}
	if got := cliGitRaw(t, anchor, "ls-files", "-s", "-z"); got != anchorIndexBefore {
		t.Fatal("dedicated worktree captures changed the anchor index")
	}
	doctor := runCLI(t, []string{"--json", "--repo-dir", anchor, "doctor"}, "")
	if doctor.code != 0 || !doctor.envelope.OK {
		t.Fatalf("post-dogfood doctor = %#v stderr=%s", doctor.envelope, doctor.stderr)
	}
}

func writeReviewedProposal(t *testing.T, path string, plan planProposal) {
	t.Helper()
	body, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
