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
		"actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6",
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
