package wipcommit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableSkillContract(t *testing.T) {
	skillPath := filepath.Join("skills", "wip-commit", "SKILL.md")
	body := readOSSTestFile(t, skillPath)

	parts := strings.SplitN(body, "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		t.Fatal("skill must start with bounded YAML frontmatter")
	}
	frontmatter := strings.TrimSpace(parts[1])
	for _, line := range strings.Split(frontmatter, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, _, ok := strings.Cut(line, ":")
		if !ok || key != "name" && key != "description" {
			t.Fatalf("unsupported skill frontmatter line %q", line)
		}
	}
	for _, required := range []string{
		"name: wip-commit",
		"wip init",
		"wip --json commit --plan",
		"git add -- <owned-path>",
		"ref_updated",
		"gate_outcome",
		"intent_state",
		"wip reconcile",
		"wip release",
		"references/automation.md",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("skill is missing required contract %q", required)
		}
	}
	referencePath := filepath.Join("skills", "wip-commit", "references", "automation.md")
	reference := readOSSTestFile(t, referencePath)
	agent := readOSSTestFile(t, filepath.Join("skills", "wip-commit", "agents", "openai.yaml"))
	portableSurface := body + "\n" + reference + "\n" + agent
	for _, forbidden := range []string{
		"/Users/",
		".codex",
		"nicos-tools",
		"git add" + " .",
		"git reset",
		"git stash",
		"git clean",
		"git push",
	} {
		if strings.Contains(strings.ToLower(portableSurface), strings.ToLower(forbidden)) {
			t.Errorf("skill contains non-portable or unsafe text %q", forbidden)
		}
	}

	for _, required := range []string{"timeout_ms", "plan_id", "plan_digest", "disjoint leases"} {
		if !strings.Contains(reference, required) {
			t.Errorf("automation reference is missing %q", required)
		}
	}

	for _, required := range []string{"display_name:", "short_description:", "default_prompt:", "$wip-commit"} {
		if !strings.Contains(agent, required) {
			t.Errorf("agent metadata is missing %q", required)
		}
	}
}

func readOSSTestFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
