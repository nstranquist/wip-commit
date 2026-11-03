package wipcommit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestWorkflowActionsUseImmutableCommits(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(".github", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	usePattern := regexp.MustCompile(`(?m)^\s*-?\s*uses:\s+([^\s@]+)@([^\s#]+)`)
	commitPattern := regexp.MustCompile(`^[0-9a-f]{40}$`)
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		path := filepath.Join(".github", "workflows", entry.Name())
		body := readOSSTestFile(t, path)
		for _, match := range usePattern.FindAllStringSubmatch(body, -1) {
			count++
			if !commitPattern.MatchString(match[2]) {
				t.Errorf("%s action %s uses mutable ref %q", path, match[1], match[2])
			}
		}
	}
	if count == 0 {
		t.Fatal("no workflow actions found")
	}
}

func TestBetaReleaseWorkflowContract(t *testing.T) {
	body := readOSSTestFile(t, filepath.Join(".github", "workflows", "release-beta.yml"))
	for _, required := range []string{
		`- "v*-*"`,
		"github.repository == 'nstranquist/wip-commit'",
		"attestations: write",
		"artifact-metadata: write",
		"contents: write",
		"id-token: write",
		"go test -race -count=3 ./...",
		"go run ./scripts/release",
		"actions/attest@508db95dd578ae2727ebd6217d5ba78e4fbda05d",
		"dist/checksums.txt",
		"dist/release-receipt.json",
		"--prerelease",
		"--verify-tag",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("release workflow is missing %q", required)
		}
	}
	for _, forbidden := range []string{"workflow_dispatch:", "pull_request:", "branches:"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("release workflow contains unexpected trigger %q", forbidden)
		}
	}
}

func TestCIWorkflowPinsTheLintToolchain(t *testing.T) {
	body := readOSSTestFile(t, filepath.Join(".github", "workflows", "ci.yml"))
	for _, required := range []string{
		"lint:",
		"golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a",
		"version: v2.12.2",
		"args: --timeout=5m",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("CI workflow is missing %q", required)
		}
	}
}

func TestGitleaksWorkflowIsDedicatedAndFailClosed(t *testing.T) {
	body := readOSSTestFile(t, filepath.Join(".github", "workflows", "gitleaks.yml"))
	ci := readOSSTestFile(t, filepath.Join(".github", "workflows", "ci.yml"))
	release := readOSSTestFile(t, filepath.Join(".github", "workflows", "release-beta.yml"))
	if strings.Contains(ci, "gitleaks") || strings.Contains(release, "gitleaks") {
		t.Fatal("secret scan is folded into another workflow")
	}
	for _, required := range []string{
		"push:\n    branches: [main]\n  pull_request:\n  workflow_dispatch:",
		"permissions:\n  contents: read\n  pull-requests: read\n",
		"  gitleaks:\n    if: github.event_name != 'workflow_dispatch'",
		"  gitleaks-full-history:\n    if: github.event_name == 'workflow_dispatch'",
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
		"fetch-depth: 0",
		"persist-credentials: false",
		"gitleaks/gitleaks-action@e0c47f4f8be36e29cdc102c57e68cb5cbf0e8d1e # v3.0.0",
		`GITLEAKS_VERSION: "8.30.1"`,
		"GITLEAKS_ENABLE_COMMENTS: false",
		"GITLEAKS_ENABLE_UPLOAD_ARTIFACT: false",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("gitleaks workflow is missing %q", required)
		}
	}
	for _, pinned := range []string{
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
		"gitleaks/gitleaks-action@e0c47f4f8be36e29cdc102c57e68cb5cbf0e8d1e # v3.0.0",
		"          fetch-depth: 0\n",
		"          persist-credentials: false\n",
		`          GITLEAKS_VERSION: "8.30.1"`,
		"          GITLEAKS_ENABLE_COMMENTS: false",
		"          GITLEAKS_ENABLE_UPLOAD_ARTIFACT: false",
	} {
		if strings.Count(body, pinned) != 2 {
			t.Errorf("gitleaks workflow has %d copies of %q, want 2", strings.Count(body, pinned), pinned)
		}
	}
	for _, forbidden := range []string{
		"paths-ignore:",
		"paths:",
		"write",
		"8.24.3",
		"pre-commit",
		"GITLEAKS_LICENSE",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("gitleaks workflow contains %q", forbidden)
		}
	}
}
