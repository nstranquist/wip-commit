package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewedBaselineCoversEverySemanticCase(t *testing.T) {
	reviewed, body, err := loadBaseline("baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 || reviewed.MinimumScore != 1 || !reviewed.ZeroAIRequired {
		t.Fatalf("baseline = %#v", reviewed)
	}
	if !sameStrings(reviewed.RequiredCases, requiredCaseIDs) {
		t.Fatalf("baseline cases = %#v, want %#v", reviewed.RequiredCases, requiredCaseIDs)
	}
}

func TestReportValidationRejectsPathsAndAIMisreporting(t *testing.T) {
	cases := make([]caseResult, 0, len(requiredCaseIDs))
	for _, id := range requiredCaseIDs {
		cases = append(cases, caseResult{
			ID: id, Passed: true, ElapsedMS: 1,
			Positive:        assertion{Passed: true, Detail: "portable positive evidence"},
			NegativeControl: assertion{Passed: true, Detail: "portable negative evidence"},
		})
	}
	reviewed := reviewedBaselineForTest()
	value := assembleReport(cases, candidateInfo{Kind: "repository-build", SHA256: "sha256:" + strings.Repeat("a", 64)}, "sha256:"+strings.Repeat("b", 64), 1)
	value.Overall.MeetsReviewedBaseline = meetsBaseline(value, reviewed)
	if err := validateReport(value, reviewed); err != nil {
		t.Fatal(err)
	}
	value.Cases[0].Positive.Detail = "/private/fixture/path"
	if err := validateReport(value, reviewed); err == nil {
		t.Fatal("path-bearing report detail was accepted")
	}
	value.Cases[0].Positive.Detail = "portable positive evidence"
	value.AIUsage.Calls = 1
	if err := validateReport(value, reviewed); err == nil {
		t.Fatal("nonzero AI usage was accepted as a zero-AI report")
	}
}

func TestReportValidationRejectsInconsistentAggregates(t *testing.T) {
	cases := make([]caseResult, 0, len(requiredCaseIDs))
	for _, id := range requiredCaseIDs {
		cases = append(cases, caseResult{
			ID: id, Passed: true, ElapsedMS: 1,
			Positive:        assertion{Passed: true, Detail: "portable positive evidence"},
			NegativeControl: assertion{Passed: true, Detail: "portable negative evidence"},
		})
	}
	reviewed := reviewedBaselineForTest()
	value := assembleReport(cases, candidateInfo{Kind: "repository-build", SHA256: "sha256:" + strings.Repeat("a", 64)}, "sha256:"+strings.Repeat("b", 64), 1)
	value.Overall.MeetsReviewedBaseline = meetsBaseline(value, reviewed)
	value.Overall.PassedChecks--
	if err := validateReport(value, reviewed); err == nil {
		t.Fatal("inconsistent aggregate outcome was accepted")
	}
}

func TestFreezeCandidateHashesExactExecutableCopy(t *testing.T) {
	sourceDir := t.TempDir()
	workspace := t.TempDir()
	source := filepath.Join(sourceDir, executableName("candidate"))
	original := []byte("candidate bytes\n")
	if err := os.WriteFile(source, original, 0o700); err != nil {
		t.Fatal(err)
	}
	frozen, digest, err := freezeCandidate(source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if frozen == source || digest != sha256Text(original) {
		t.Fatalf("frozen = %q, digest = %q", frozen, digest)
	}
	if err := os.WriteFile(source, []byte("changed source\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(original) {
		t.Fatalf("frozen candidate changed with source: %q", body)
	}
}

func TestIsolatedEnvironmentDropsCredentialsAndAmbientGitState(t *testing.T) {
	t.Setenv("PATH", "/benchmark/bin")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "do-not-pass")
	t.Setenv("GIT_DIR", "/ambient/git")
	t.Setenv("SSH_AUTH_SOCK", "/ambient/agent")
	home := t.TempDir()
	environment := environmentMap(isolatedEnvironment(home))
	for _, name := range []string{"AWS_SECRET_ACCESS_KEY", "GIT_DIR", "SSH_AUTH_SOCK"} {
		if _, exists := environment[name]; exists {
			t.Fatalf("isolated environment retained %s", name)
		}
	}
	for name, expected := range map[string]string{
		"HOME": home, "USERPROFILE": home, "TEMP": home, "TMP": home, "TMPDIR": home,
		"PATH": "/benchmark/bin", "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0",
	} {
		if environment[name] != expected {
			t.Fatalf("%s = %q, want %q", name, environment[name], expected)
		}
	}
}

func environmentMap(environment []string) map[string]string {
	result := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		result[strings.ToUpper(name)] = value
	}
	return result
}

func reviewedBaselineForTest() baseline {
	return baseline{
		SchemaVersion: reportSchemaVersion, Benchmark: benchmarkName, BenchmarkVersion: benchmarkVersion,
		ReviewStatus: "reviewed", MinimumScore: 1, RequiredPositiveChecks: len(requiredCaseIDs),
		RequiredNegativeControls: len(requiredCaseIDs), ZeroAIRequired: true, RequiredCases: append([]string(nil), requiredCaseIDs...),
	}
}

func TestMutableArtifactsCannotReplaceReviewedBaseline(t *testing.T) {
	directory := t.TempDir()
	baselinePath := filepath.Join(directory, "baseline.json")
	original, err := os.ReadFile("baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselinePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	reviewed, _, err := loadBaseline(baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	cases := make([]caseResult, 0, len(requiredCaseIDs))
	for _, id := range requiredCaseIDs {
		cases = append(cases, caseResult{
			ID: id, ElapsedMS: 1,
			Positive:        assertion{Detail: "candidate regression"},
			NegativeControl: assertion{Detail: "negative control regression"},
		})
	}
	value := assembleReport(cases, candidateInfo{Kind: weakenedCandidateLabel, SHA256: "sha256:" + strings.Repeat("a", 64)}, sha256Text(original), 1)
	value.Overall.MeetsReviewedBaseline = meetsBaseline(value, reviewed)
	artifactDir := filepath.Join(directory, "artifacts")
	if err := prepareArtifactDir(artifactDir); err != nil {
		t.Fatal(err)
	}
	if err := writeRunArtifacts(artifactDir, value, reviewed); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("mutable artifact write changed the reviewed baseline")
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "proposal.json")); err != nil {
		t.Fatalf("failing candidate did not produce a proposal: %v", err)
	}
}

func TestDeliberatelyWeakenedCandidateContainsNoSafetyBehavior(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "weakened", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "unsafe-noop") {
		t.Fatal("weakened candidate identity is missing")
	}
	for _, forbidden := range []string{"WORKTREE_REQUIRED", "PATH_LEASE_CONFLICT", "PLAN_NOT_REVIEWED", "SPLIT_PLAN_REQUIRED", "LEASE_REQUIRED", "VERIFY_FAILED", "INTENT_DIGEST_MISMATCH", "os/exec"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("weakened candidate unexpectedly contains %q", forbidden)
		}
	}
}
