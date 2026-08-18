package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/engine"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/policy"
	"github.com/nstranquist/wip-commit/internal/store"
	"github.com/nstranquist/wip-commit/internal/strictjson"
)

const reviewPlanSchemaVersion = "2.0.0"

type resolvedCommitPlan struct {
	Groups              []engine.Group
	ReviewDigest        string
	ExpectedIndexDigest string
	Policy              policy.Receipt
	Reviewed            bool
	Plan                *planProposal
}

func decodeCommitPlan(reader io.Reader) (resolvedCommitPlan, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxPlanBytes+1))
	if err != nil {
		return resolvedCommitPlan{}, fail.Wrap("INVALID_PLAN", err)
	}
	if len(body) > maxPlanBytes {
		return resolvedCommitPlan{}, fail.Errorf("INVALID_PLAN", "plan exceeds %d bytes", maxPlanBytes)
	}
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		groups, err := decodeLegacyPlan(body)
		return resolvedCommitPlan{Groups: groups}, err
	}
	var plan planProposal
	if err := strictjson.Decode(body, &plan); err != nil {
		return resolvedCommitPlan{}, fail.Wrap("INVALID_PLAN", err)
	}
	return resolvedCommitPlan{Groups: planGroups(plan), Reviewed: true, Plan: &plan}, nil
}

func decodeLegacyPlan(body []byte) ([]engine.Group, error) {
	var groups []engine.Group
	if err := strictjson.Decode(body, &groups); err != nil {
		return nil, fail.Wrap("INVALID_PLAN", err)
	}
	if len(groups) == 0 {
		return nil, fail.New("INVALID_PLAN", "plan must contain at least one commit group")
	}
	return groups, nil
}

func planGroups(plan planProposal) []engine.Group {
	groups := make([]engine.Group, 0, len(plan.Groups))
	for _, group := range plan.Groups {
		groups = append(groups, engine.Group{Message: group.Message, Files: append([]string(nil), group.Files...), Verify: append([]engine.VerifyCommand(nil), group.Verify...)})
	}
	return groups
}

func validateReviewedPlan(ctx context.Context, laneStore store.Store, lane store.Lane, allowed []string, plan *planProposal, allowWIP bool) (resolvedCommitPlan, error) {
	if plan.SchemaVersion != reviewPlanSchemaVersion {
		return resolvedCommitPlan{}, fail.Errorf("MIGRATION_REQUIRED", "capture plan schema %q is unsupported; this wip release supports %s", plan.SchemaVersion, reviewPlanSchemaVersion)
	}
	if plan.State != "reviewed" {
		return resolvedCommitPlan{}, fail.New("PLAN_NOT_REVIEWED", "set plan state to reviewed only after replacing every empty message and reviewing each path group")
	}
	if plan.Lane != lane.ID || plan.TargetRef != lane.Ref || plan.LaneTip != lane.CurrentSHA {
		return resolvedCommitPlan{}, fail.New("STALE_PLAN", "plan lane identity or tip no longer matches the active lane")
	}
	if plan.SourceHead != lane.BaseSHA {
		return resolvedCommitPlan{}, fail.New("STALE_PLAN", "plan source HEAD no longer matches the lane base")
	}
	head, err := laneStore.Repo.Text(ctx, nil, "rev-parse", "HEAD")
	if err != nil {
		return resolvedCommitPlan{}, fail.Wrap("GIT_FAILED", err)
	}
	if head != plan.SourceHead {
		return resolvedCommitPlan{}, fail.New("STALE_PLAN", "source HEAD changed after the plan was drafted")
	}
	saved, err := loadProfile(laneStore, lane.ID)
	if err != nil {
		return resolvedCommitPlan{}, err
	}
	if plan.MessagePolicy != saved.MessagePolicy {
		return resolvedCommitPlan{}, fail.New("STALE_PLAN", "plan message policy differs from the lane profile")
	}
	if err := policy.ValidateReceipt(plan.MessagePolicy); err != nil {
		return resolvedCommitPlan{}, err
	}
	status, err := laneStore.Status(lane.ID)
	if err != nil {
		return resolvedCommitPlan{}, err
	}
	if plan.LeaseDigest != leaseScopeDigest(status.Leases, time.Now().UTC()) {
		return resolvedCommitPlan{}, fail.New("STALE_PLAN", "active lease scope changed after the plan was drafted")
	}
	current, err := selectedStaged(ctx, laneStore, lane.CurrentSHA, allowed, nil)
	if err != nil {
		return resolvedCommitPlan{}, err
	}
	if !samePlanStrings(current, plan.StagedPaths) {
		return resolvedCommitPlan{}, fail.New("STALE_PLAN", "capture-candidate path set changed after the plan was drafted")
	}
	entries, err := laneStore.Repo.IndexEntries(ctx, nil)
	if err != nil {
		return resolvedCommitPlan{}, fail.Wrap("GIT_FAILED", err)
	}
	if digest := engine.SelectedIndexDigest(entries, plan.StagedPaths); digest != plan.SourceIndexDigest {
		return resolvedCommitPlan{}, fail.New("STALE_PLAN", "selected staged index entries changed after the plan was drafted")
	}
	groups := planGroups(*plan)
	if len(groups) == 0 {
		return resolvedCommitPlan{}, fail.New("INVALID_PLAN", "reviewed plan must contain at least one commit group")
	}
	var plannedPaths []string
	for index, group := range plan.Groups {
		if strings.TrimSpace(group.Key) == "" || strings.TrimSpace(group.SuggestedScope) == "" || strings.TrimSpace(group.SuggestedPrefix) == "" || strings.TrimSpace(group.Rationale) == "" {
			return resolvedCommitPlan{}, fail.Errorf("INVALID_PLAN", "group %d is missing review guidance", index+1)
		}
		normalized, normalizeErr := laneStore.Repo.NormalizePaths(group.Files)
		if normalizeErr != nil || !samePlanStrings(normalized, group.Files) {
			return resolvedCommitPlan{}, fail.Errorf("INVALID_PLAN", "group %d files are not canonical", index+1)
		}
		plannedPaths = append(plannedPaths, normalized...)
	}
	sort.Strings(plannedPaths)
	if !samePlanStrings(plannedPaths, plan.StagedPaths) {
		return resolvedCommitPlan{}, fail.New("INVALID_PLAN", "reviewed groups must cover every planned path exactly once")
	}
	messages := make([]string, 0, len(groups))
	for _, group := range groups {
		messages = append(messages, group.Message)
	}
	if err := policy.ValidateMessages(plan.MessagePolicy, messages, allowWIP); err != nil {
		return resolvedCommitPlan{}, err
	}
	digest, err := reviewPlanDigest(*plan)
	if err != nil {
		return resolvedCommitPlan{}, err
	}
	return resolvedCommitPlan{
		Groups:              groups,
		ReviewDigest:        digest,
		ExpectedIndexDigest: plan.SourceIndexDigest,
		Policy:              plan.MessagePolicy,
		Reviewed:            true,
	}, nil
}

func reviewPlanDigest(plan planProposal) (string, error) {
	body, err := json.Marshal(plan)
	if err != nil {
		return "", fail.Wrap("INVALID_PLAN", err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func leaseScopeDigest(leases []store.Lease, now time.Time) string {
	type scope struct {
		ID    string   `json:"id"`
		Paths []string `json:"paths"`
	}
	values := make([]scope, 0, len(leases))
	for _, lease := range leases {
		if lease.State != "active" || lease.ExpiresAt != nil && !now.Before(*lease.ExpiresAt) {
			continue
		}
		paths := append([]string(nil), lease.Paths...)
		sort.Strings(paths)
		values = append(values, scope{ID: lease.ID, Paths: paths})
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	body, _ := json.Marshal(values)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func samePlanStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
