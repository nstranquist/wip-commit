package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/engine"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/pathid"
	"github.com/nstranquist/wip-commit/internal/policy"
	"github.com/nstranquist/wip-commit/internal/store"
)

type splitProposal struct {
	Key             string                 `json:"key"`
	SuggestedScope  string                 `json:"suggested_scope"`
	SuggestedPrefix string                 `json:"suggested_prefix"`
	Rationale       string                 `json:"rationale"`
	Message         string                 `json:"message"`
	Files           []string               `json:"files"`
	Verify          []engine.VerifyCommand `json:"verify,omitempty"`
}

type planProposal struct {
	SchemaVersion   string                 `json:"schema_version"`
	State           string                 `json:"state"`
	Lane            string                 `json:"lane"`
	Agent           string                 `json:"agent"`
	Session         string                 `json:"session"`
	TargetRef       string                 `json:"target_ref"`
	LaneTip         string                 `json:"lane_tip"`
	SourceHead      string                 `json:"source_head"`
	SourceMode      engine.SourceMode      `json:"source_mode"`
	UntrackedPolicy engine.UntrackedPolicy `json:"untracked_policy"`
	SourceDigest    string                 `json:"source_digest"`
	SourceScopes    []string               `json:"source_scopes"`
	LeaseDigest     string                 `json:"lease_digest"`
	MessagePolicy   policy.Receipt         `json:"message_policy"`
	SourcePaths     []string               `json:"source_paths"`
	Groups          []splitProposal        `json:"groups"`
	Instruction     string                 `json:"instruction"`
}

func planObserveRequested(args []string) bool {
	return booleanFlagRequested(args, "observe")
}

func (application app) runPlan(ctx context.Context, laneStore store.Store, args []string) int {
	set := application.flagSet("plan")
	var identity identityFlags
	var paths stringList
	out := set.String("out", "", "write the draft plan without overwriting an existing file")
	source := set.String("source", string(engine.SourceStagedIndex), "capture source: staged_index or worktree_snapshot")
	untracked := set.String("untracked", "", "untracked policy: not_applicable, forbid, or include")
	observe := set.Bool("observe", false, "create a plan without renewing leases or writing an output file")
	identity.bind(set)
	set.Var(&paths, "path", "leased source scope to include (repeatable; default all leased scopes)")
	if err := set.Parse(args); err != nil {
		return application.failure("plan", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("plan", err, nil, 2)
	}
	if *observe && strings.TrimSpace(*out) != "" {
		return application.failure("plan", fail.New("INVALID_ARGS", "--observe cannot be combined with --out"), nil, 2)
	}
	status, err := resolveStatus(laneStore, identity)
	if err != nil {
		return application.failure("plan", err, nil, 1)
	}
	renewal := renewalStatus{}
	if !*observe {
		renewal.Attempted = true
		if _, err := laneStore.Renew(status.Lane.ID, status.Lane.Agent, status.Lane.Session); err != nil {
			return application.failure("plan", err, nil, 1)
		}
		now := time.Now().UTC()
		renewal.Succeeded, renewal.RenewedAt = true, &now
		status, err = laneStore.Status(status.Lane.ID)
		if err != nil {
			return application.failure("plan", err, nil, 1)
		}
	}
	allowed, err := laneStore.ActivePaths(status.Lane.ID)
	if err != nil {
		return application.failure("plan", err, nil, 1)
	}
	mode, untrackedPolicy, err := engine.NormalizeSource(engine.SourceMode(*source), engine.UntrackedPolicy(*untracked))
	if err != nil {
		return application.failure("plan", err, nil, 2)
	}
	scopes, err := planSourceScopes(laneStore, allowed, paths)
	if err != nil {
		return application.failure("plan", err, nil, 1)
	}
	snapshot, err := engine.InspectSource(ctx, laneStore.Repo, status.Lane.CurrentSHA, scopes, mode, untrackedPolicy)
	if err != nil {
		return application.failure("plan", err, nil, 1)
	}
	if len(snapshot.Paths) == 0 {
		return application.failure("plan", fail.New("EMPTY_SELECTION", "no changed source paths are covered by the selected active leases"), nil, 1)
	}
	for _, scope := range scopes {
		matched := false
		for _, path := range snapshot.Paths {
			if pathid.Covered(path, []string{scope}) {
				matched = true
				break
			}
		}
		if len(paths) > 0 && !matched {
			return application.failure("plan", fail.Errorf("EMPTY_SELECTION", "requested scope %q contains no changed source path", scope), nil, 1)
		}
	}
	saved, err := loadProfile(laneStore, status.Lane.ID)
	if err != nil {
		return application.failure("plan", err, nil, 1)
	}
	proposal := planProposal{
		SchemaVersion:   reviewPlanSchemaVersion,
		State:           "draft",
		Lane:            status.Lane.ID,
		Agent:           status.Lane.Agent,
		Session:         status.Lane.Session,
		TargetRef:       status.Lane.Ref,
		LaneTip:         status.Lane.CurrentSHA,
		SourceHead:      status.Lane.BaseSHA,
		SourceMode:      snapshot.Mode,
		UntrackedPolicy: snapshot.UntrackedPolicy,
		SourceDigest:    snapshot.Digest,
		SourceScopes:    append([]string(nil), scopes...),
		LeaseDigest:     leaseScopeDigest(status.Leases, time.Now().UTC()),
		MessagePolicy:   saved.MessagePolicy,
		SourcePaths:     append([]string(nil), snapshot.Paths...),
		Groups:          proposeSplitGroups(snapshot.Paths),
		Instruction:     "Review every path group, add a non-empty message to each group, then change state from draft to reviewed. Capture rejects stale bindings and draft plans.",
	}
	if strings.TrimSpace(*out) != "" {
		body, marshalErr := json.MarshalIndent(proposal, "", "  ")
		if marshalErr != nil {
			return application.failure("plan", fail.Wrap("PLAN_WRITE_FAILED", marshalErr), nil, 1)
		}
		path, absoluteErr := filepath.Abs(*out)
		if absoluteErr != nil {
			return application.failure("plan", fail.Wrap("PLAN_WRITE_FAILED", absoluteErr), nil, 1)
		}
		if writeErr := atomicfile.Create(path, append(body, '\n'), 0o600); writeErr != nil {
			if errors.Is(writeErr, atomicfile.ErrExists) {
				writeErr = fail.New("PLAN_EXISTS", "refusing to overwrite existing plan: "+path)
			} else {
				writeErr = fail.Wrap("PLAN_WRITE_FAILED", writeErr)
			}
			return application.failure("plan", writeErr, nil, 1)
		}
	}
	operational, err := buildOperationalStatus(ctx, laneStore, status, renewal)
	if err != nil {
		return application.failure("plan", err, proposal, 1)
	}
	return application.successWithContext("plan", proposal, formatPlanProposal(proposal), operational.context())
}

func planSourceScopes(laneStore store.Store, allowed, requested []string) ([]string, error) {
	if len(requested) == 0 {
		return append([]string(nil), allowed...), nil
	}
	scopes, err := laneStore.Repo.NormalizePaths(requested)
	if err != nil {
		return nil, err
	}
	for _, scope := range scopes {
		if !pathid.Covered(scope, allowed) {
			return nil, fail.Errorf("LEASE_REQUIRED", "requested scope %q is outside the active lease set", scope)
		}
	}
	return scopes, nil
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
		key := strings.TrimSuffix(name, filepath.Ext(name))
		if strings.TrimSpace(key) == "" {
			return "repository-config"
		}
		return key
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
	case "repository-config":
		return "config"
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
	case "repository-config":
		return "repository-level configuration and tool pins"
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
