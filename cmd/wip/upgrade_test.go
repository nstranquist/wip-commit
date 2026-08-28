package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/store"
	skillbundle "github.com/nstranquist/wip-commit/skills/wip-commit"
)

func TestBinaryUpgradeRequiresExactReviewedDigest(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, binaryName())
	oldBody := []byte("old executable\n")
	if err := os.WriteFile(path, oldBody, 0o755); err != nil {
		t.Fatal(err)
	}
	preview := runCLI(t, []string{"--json", "upgrade", "--binary", "--install-dir", directory}, "")
	if preview.code != 0 || !preview.envelope.OK {
		t.Fatalf("binary preview = %#v stderr=%s", preview.envelope, preview.stderr)
	}
	var plan upgradeResult
	decodeData(t, preview.envelope.Data, &plan)
	if plan.Target != "binary" || plan.AlreadyCurrent || !validSHA256(plan.PlanDigest) {
		t.Fatalf("binary plan = %#v", plan)
	}
	if plan.SchemaVersion != "2.0.0" || plan.RevisionStore == "" || plan.PreviousRevisionPath == "" || plan.DesiredRevisionPath == "" {
		t.Fatalf("binary retention plan = %#v", plan)
	}
	if _, err := os.Lstat(plan.RevisionStore); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("binary preview changed revision store: %v", err)
	}
	if err := os.WriteFile(path, []byte("moved executable\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	moved := runCLI(t, []string{"--json", "upgrade", "--binary", "--install-dir", directory, "--apply", "--yes", "--plan-digest", plan.PlanDigest}, "")
	if moved.code == 0 || moved.envelope.Error == nil || moved.envelope.Error.Code != "UPGRADE_PLAN_DIGEST_MISMATCH" {
		t.Fatalf("moved binary apply = %#v", moved.envelope)
	}
	preview = runCLI(t, []string{"--json", "upgrade", "--binary", "--install-dir", directory}, "")
	decodeData(t, preview.envelope.Data, &plan)
	applied := runCLI(t, []string{"--json", "upgrade", "--binary", "--install-dir", directory, "--apply", "--yes", "--plan-digest", plan.PlanDigest}, "")
	if applied.code != 0 || !applied.envelope.OK {
		t.Fatalf("binary apply = %#v stderr=%s", applied.envelope, applied.stderr)
	}
	var receipt upgradeResult
	decodeData(t, applied.envelope.Data, &receipt)
	if !receipt.Applied || receipt.DesiredSHA256 != plan.DesiredSHA256 {
		t.Fatalf("binary receipt = %#v", receipt)
	}
	retained, err := os.ReadFile(plan.PreviousRevisionPath)
	if err != nil || string(retained) != "moved executable\n" {
		t.Fatalf("previous revision = %q, err=%v", retained, err)
	}
	desired, err := os.ReadFile(plan.DesiredRevisionPath)
	if err != nil || digest(desired) != plan.DesiredSHA256 {
		t.Fatalf("desired revision digest=%s, err=%v", digest(desired), err)
	}
	current, err := inspectBinaryUpgrade(directory)
	if err != nil || !current.alreadyCurrent {
		t.Fatalf("binary verification = %#v err=%v", current, err)
	}
}

func TestBinaryUpgradeRejectsConflictingRetainedRevisionWithoutReplacingTarget(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, binaryName())
	oldBody := []byte("old executable\n")
	if err := os.WriteFile(path, oldBody, 0o755); err != nil {
		t.Fatal(err)
	}
	preview := runCLI(t, []string{"--json", "upgrade", "--binary", "--install-dir", directory}, "")
	var plan upgradeResult
	decodeData(t, preview.envelope.Data, &plan)
	if err := os.Mkdir(plan.RevisionStore, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.PreviousRevisionPath, []byte("conflict\n"), 0o555); err != nil {
		t.Fatal(err)
	}
	applied := runCLI(t, []string{"--json", "upgrade", "--binary", "--install-dir", directory, "--apply", "--yes", "--plan-digest", plan.PlanDigest}, "")
	if applied.code == 0 || applied.envelope.Error == nil || applied.envelope.Error.Code != "UPGRADE_CONFLICT" {
		t.Fatalf("conflicting revision apply = %#v", applied.envelope)
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != string(oldBody) {
		t.Fatalf("failed upgrade changed target: %q, err=%v", current, err)
	}
}

func TestBinaryUpgradeRejectsRevisionStoreSymlinkWithoutReplacingTarget(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, binaryName())
	oldBody := []byte("old executable\n")
	if err := os.WriteFile(path, oldBody, 0o755); err != nil {
		t.Fatal(err)
	}
	preview := runCLI(t, []string{"--json", "upgrade", "--binary", "--install-dir", directory}, "")
	var plan upgradeResult
	decodeData(t, preview.envelope.Data, &plan)
	outside := t.TempDir()
	if err := os.Symlink(outside, plan.RevisionStore); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	applied := runCLI(t, []string{"--json", "upgrade", "--binary", "--install-dir", directory, "--apply", "--yes", "--plan-digest", plan.PlanDigest}, "")
	if applied.code == 0 || applied.envelope.Error == nil || applied.envelope.Error.Code != "UPGRADE_CONFLICT" {
		t.Fatalf("symbolic-link revision store apply = %#v", applied.envelope)
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != string(oldBody) {
		t.Fatalf("failed upgrade changed target: %q, err=%v", current, err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("failed upgrade wrote through revision-store link: entries=%v err=%v", entries, err)
	}
}

func TestRetainBinaryRevisionNeverPrunesRollbackArtifacts(t *testing.T) {
	store := filepath.Join(t.TempDir(), ".wip.revisions")
	const revisions = 6
	for index := range revisions {
		body := []byte(fmt.Sprintf("revision-%d\n", index))
		value := digest(body)
		if err := retainBinaryRevision(store, binaryRevisionPath(store, value), body, value); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(store)
	if err != nil || len(entries) != revisions {
		t.Fatalf("retained revisions=%d, want=%d, err=%v", len(entries), revisions, err)
	}
}

func TestPortableSkillUpgradePublishesContractLastAndVerifiesBundle(t *testing.T) {
	directory := t.TempDir()
	target := portableSkillPath(directory)
	for _, name := range skillbundle.PayloadPaths {
		body, err := skillbundle.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if name == "SKILL.md" {
			body = append(append([]byte{}, body...), []byte("\nlegacy\n")...)
		}
		path := filepath.Join(target, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	preview := runCLI(t, []string{"--json", "upgrade", "--skill", "--skill-dir", directory}, "")
	if preview.code != 0 || !preview.envelope.OK {
		t.Fatalf("skill preview = %#v stderr=%s", preview.envelope, preview.stderr)
	}
	var plan upgradeResult
	decodeData(t, preview.envelope.Data, &plan)
	applied := runCLI(t, []string{"--json", "upgrade", "--skill", "--skill-dir", directory, "--apply", "--yes", "--plan-digest", plan.PlanDigest}, "")
	if applied.code != 0 || !applied.envelope.OK {
		t.Fatalf("skill apply = %#v stderr=%s", applied.envelope, applied.stderr)
	}
	if _, err := os.Lstat(filepath.Join(target, "contract.json")); err != nil {
		t.Fatalf("contract marker missing after skill upgrade: %v", err)
	}
	current, err := inspectSkillUpgrade(directory)
	if err != nil || !current.alreadyCurrent || current.currentSHA256 != plan.DesiredSHA256 {
		t.Fatalf("skill verification = %#v err=%v", current, err)
	}
}

func TestStateUpgradeIsQuiescentAndDigestBound(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repo.CommonDir, "wip", "domain.json")
	legacy := []byte("{\n  \"schema_version\": 1,\n  \"domain\": \"github.com/nstranquist/wip-commit\",\n  \"state_version\": 1\n}\n")
	if err := os.WriteFile(marker, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	doctor := runCLI(t, []string{"--json", "--repo-dir", directory, "doctor"}, "")
	var report doctorReport
	decodeData(t, doctor.envelope.Data, &report)
	if doctor.code != 1 || !doctor.envelope.OK || report.Healthy || !report.UpgradeRequired || len(report.Findings) != 1 || report.Findings[0].Code != "STATE_UPGRADE_REQUIRED" || report.Findings[0].Severity != "recovery" {
		t.Fatalf("legacy marker doctor = %#v report=%#v", doctor.envelope, report)
	}
	preview := runCLI(t, []string{"--json", "--repo-dir", directory, "upgrade", "--state"}, "")
	if preview.code != 0 || !preview.envelope.OK {
		t.Fatalf("state preview = %#v stderr=%s", preview.envelope, preview.stderr)
	}
	var plan upgradeResult
	decodeData(t, preview.envelope.Data, &plan)
	if plan.Target != "state" || plan.AlreadyCurrent {
		t.Fatalf("state plan = %#v", plan)
	}
	if _, err := laneStore.Create(context.Background(), store.CreateOptions{ID: "upgrade-blocker", Agent: "agent", Session: "session", Mode: store.ModeShared}); err == nil {
		blocked := runCLI(t, []string{"--json", "--repo-dir", directory, "upgrade", "--state", "--apply", "--yes", "--plan-digest", plan.PlanDigest}, "")
		if blocked.code == 0 || blocked.envelope.Error == nil || blocked.envelope.Error.Code != "UPGRADE_BLOCKED" {
			t.Fatalf("active-lane state apply = %#v", blocked.envelope)
		}
		if err := laneStore.Release("upgrade-blocker", "agent", "session", true); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal(err)
	}
	applied := runCLI(t, []string{"--json", "--repo-dir", directory, "upgrade", "--state", "--apply", "--yes", "--plan-digest", plan.PlanDigest}, "")
	if applied.code != 0 || !applied.envelope.OK {
		t.Fatalf("state apply = %#v stderr=%s", applied.envelope, applied.stderr)
	}
	opened, err := store.Open(repo)
	if err != nil || opened.Domain.SchemaVersion != 2 || opened.Domain.UpgradeRequired {
		t.Fatalf("upgraded domain = %#v err=%v", opened.Domain, err)
	}
}
