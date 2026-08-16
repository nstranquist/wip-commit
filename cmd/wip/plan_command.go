package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/store"
)

type splitProposal struct {
	Key             string   `json:"key"`
	SuggestedScope  string   `json:"suggested_scope"`
	SuggestedPrefix string   `json:"suggested_prefix"`
	Rationale       string   `json:"rationale"`
	Files           []string `json:"files"`
}

type planProposal struct {
	SchemaVersion string          `json:"schema_version"`
	Lane          string          `json:"lane"`
	StagedPaths   []string        `json:"staged_paths"`
	Groups        []splitProposal `json:"groups"`
	Instruction   string          `json:"instruction"`
}

func (application app) runPlan(ctx context.Context, laneStore store.Store, args []string) int {
	set := application.flagSet("plan")
	var identity identityFlags
	var paths stringList
	identity.bind(set)
	set.Var(&paths, "path", "leased staged scope to include (repeatable; default all leased staged paths)")
	if err := set.Parse(args); err != nil {
		return application.failure("plan", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("plan", err, nil, 2)
	}
	status, err := resolveStatus(laneStore, identity)
	if err != nil {
		return application.failure("plan", err, nil, 1)
	}
	allowed, err := laneStore.ActivePaths(status.Lane.ID)
	if err != nil {
		return application.failure("plan", err, nil, 1)
	}
	selected, err := selectedStaged(ctx, laneStore, allowed, paths)
	if err != nil {
		return application.failure("plan", err, nil, 1)
	}
	proposal := planProposal{
		SchemaVersion: "1.0.0",
		Lane:          status.Lane.ID,
		StagedPaths:   selected,
		Groups:        proposeSplitGroups(selected),
		Instruction:   "Review dependency closure, move any cross-cutting file into its best-fit group, then write one distinct Conventional Commit message per group in a commit plan.",
	}
	return application.success("plan", proposal, formatPlanProposal(proposal))
}

func proposeSplitGroups(paths []string) []splitProposal {
	grouped := map[string][]string{}
	for _, path := range paths {
		key := splitGroupKey(filepath.ToSlash(path))
		grouped[key] = append(grouped[key], path)
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	groups := make([]splitProposal, 0, len(keys))
	for _, key := range keys {
		files := grouped[key]
		sort.Strings(files)
		scope := splitScope(key)
		groups = append(groups, splitProposal{Key: key, SuggestedScope: scope, SuggestedPrefix: splitSuggestedPrefix(key, scope, files), Rationale: splitRationale(key), Files: files})
	}
	return groups
}

func splitSuggestedPrefix(key, scope string, files []string) string {
	typeName := "<type>"
	switch key {
	case "dependencies":
		typeName = "build"
	case "repository-docs", "docs", "changelog":
		typeName = "docs"
	case ".github/workflows":
		typeName = "ci"
	default:
		if allTestPaths(files) {
			typeName = "test"
		}
	}
	return typeName + "(" + scope + "): "
}

func allTestPaths(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		path = "/" + strings.ToLower(filepath.ToSlash(path))
		base := filepath.Base(path)
		if !strings.HasSuffix(base, "_test.go") && !strings.Contains(base, ".test.") && !strings.Contains(base, ".spec.") && !strings.Contains(path, "/tests/") && !strings.Contains(path, "/testdata/") {
			return false
		}
	}
	return true
}

func splitGroupKey(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 1 {
		name := parts[0]
		switch name {
		case "go.mod", "go.sum", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "Cargo.toml", "Cargo.lock":
			return "dependencies"
		case "README.md", "CONTRIBUTING.md", "SECURITY.md", "THREAT-MODEL.md", "LICENSE", "CODE_OF_CONDUCT.md":
			return "repository-docs"
		case "CHANGELOG.md":
			return "changelog"
		}
		return strings.TrimSuffix(name, filepath.Ext(name))
	}
	if parts[0] == ".github" && len(parts) >= 2 {
		return strings.Join(parts[:2], "/")
	}
	if parts[0] == "docs" {
		if len(parts) >= 3 {
			return strings.Join(parts[:2], "/")
		}
		return "docs"
	}
	for _, componentRoot := range []string{"cmd", "internal", "pkg", "packages", "services", "apps", "skills"} {
		if parts[0] == componentRoot && len(parts) >= 2 {
			return strings.Join(parts[:2], "/")
		}
	}
	return parts[0]
}

func splitScope(key string) string {
	switch key {
	case "dependencies":
		return "deps"
	case "repository-docs", "docs":
		return "docs"
	case ".github/workflows":
		return "ci"
	}
	parts := strings.Split(key, "/")
	value := parts[len(parts)-1]
	var output strings.Builder
	separator := false
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			if separator && output.Len() > 0 {
				output.WriteByte('-')
			}
			output.WriteRune(character)
			separator = false
		} else {
			separator = true
		}
		if output.Len() >= 32 {
			break
		}
	}
	if output.Len() == 0 {
		return "repo"
	}
	return strings.Trim(output.String(), "-")
}

func splitRationale(key string) string {
	switch key {
	case "dependencies":
		return "dependency manifests and lockfiles must stay synchronized"
	case "repository-docs":
		return "repository-level policy and onboarding documentation"
	case "changelog":
		return "release history remains an explicit concern"
	case ".github/workflows":
		return "hosted automation and policy configuration"
	default:
		return "nearest component boundary; review semantic and dependency closure"
	}
}

func formatPlanProposal(proposal planProposal) string {
	lines := []string{fmt.Sprintf("Proposed %d split group(s) for lane %s:", len(proposal.Groups), proposal.Lane)}
	for _, group := range proposal.Groups {
		lines = append(lines, fmt.Sprintf("%s (suggested prefix %s):", group.Key, group.SuggestedPrefix))
		for _, path := range group.Files {
			lines = append(lines, "  "+path)
		}
	}
	lines = append(lines, proposal.Instruction)
	return strings.Join(lines, "\n")
}
