package wipcommit

import (
	"os"
	"strings"
	"testing"

	"github.com/nstranquist/wip-commit/internal/contract"
)

func TestCurrentProtocolDocsMatchPrivateSourceContracts(t *testing.T) {
	required := map[string][]string{
		"README.md": {
			contract.Protocol,
			`"schema_version": "3.0.0"`,
			`"agent": "codex"`,
			`"session": "parser-errors-review"`,
			`"source_mode": "worktree_snapshot"`,
			`"source_digest": "sha256:<exact digest>"`,
			"version 2 cumulative handoff",
			"`landing_authorized` to `false`",
			"`wip plan --observe`",
		},
		"CHANGELOG.md": {
			contract.Protocol,
			"version 3 draft/reviewed plan schema",
			"version 2 cumulative capture-only handoff",
		},
		"docs/ARCHITECTURE.md": {
			"`worktree_snapshot`",
			"`source_digest`",
			"version 3 object",
			"version 2 output",
			"handoffs/<handoff-digest>.json",
			"plans/<review-digest>.json",
			"`wip/v1/locks/leases.lock` artifact",
		},
		"docs/STATE-COMPATIBILITY.md": {
			contract.Protocol,
			"capture-intent schema version 3",
			"`handoffs` and `plans` review directories",
			"`wip/v1/locks/leases.lock` artifact before imported",
		},
		"docs/OSS-PUBLIC-BETA.requirements.yaml": {
			"one version 3 plan",
			"cumulative version 2 handoff",
			"manifest-bound registry lock",
		},
		"cmd/wip/app.go": {
			"selected leased source paths",
			"reviewed v3 plan",
			"cumulative capture-only handoff",
			"digest-bound coordination migration",
		},
		"skills/wip-commit/SKILL.md": {
			"--source worktree_snapshot",
			"--source staged_index",
			"cumulative base-to-tip capture",
		},
		"skills/wip-commit/references/automation.md": {
			"--source worktree_snapshot",
			"continuous parent chain",
			"handoff_digest",
		},
	}
	for path, fragments := range required {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, fragment := range fragments {
			if !strings.Contains(text, fragment) {
				t.Errorf("%s does not contain current contract text %q", path, fragment)
			}
		}
	}

	currentDocs := []string{
		"README.md",
		"CHANGELOG.md",
		"THREAT-MODEL.md",
		"cmd/wip/app.go",
		"docs/ARCHITECTURE.md",
		"docs/BETA-EXERCISE.md",
		"docs/ERRORS.md",
		"docs/KEP-0001-capture-to-landing-boundary.md",
		"docs/KEP-0003-agent-facing-capture-workflow.md",
		"docs/OSS-PRACTICE-GUIDE.md",
		"docs/OSS-PUBLIC-BETA.md",
		"docs/OSS-PUBLIC-BETA.requirements.yaml",
		"docs/OSS-READINESS.md",
		"docs/STATE-COMPATIBILITY.md",
		"examples/README.md",
		"SECURITY.md",
		"skills/wip-commit/SKILL.md",
		"skills/wip-commit/benchmark/README.md",
		"skills/wip-commit/references/automation.md",
	}
	forbidden := []string{
		"wip.command/2.0.0",
		"source_index_digest",
		`"staged_paths"`,
		"SOURCE_INDEX_MOVED",
		"staged leased paths",
		"complete source-index",
		"one version 2 plan",
		"Capture one reviewed v2 plan",
		"latest complete capture receipt",
	}
	for _, path := range currentDocs {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, fragment := range forbidden {
			if strings.Contains(string(body), fragment) {
				t.Errorf("%s contains stale contract text %q", path, fragment)
			}
		}
	}
}
