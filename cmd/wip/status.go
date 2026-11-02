package main

import (
	"context"
	"sort"
	"time"

	"github.com/nstranquist/wip-commit/internal/engine"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/pathid"
	"github.com/nstranquist/wip-commit/internal/policy"
	"github.com/nstranquist/wip-commit/internal/store"
)

type renewalStatus struct {
	Attempted bool       `json:"attempted"`
	Succeeded bool       `json:"succeeded"`
	RenewedAt *time.Time `json:"renewed_at,omitempty"`
}

type refHealth struct {
	Healthy  bool   `json:"healthy"`
	Expected string `json:"expected"`
	Actual   string `json:"actual,omitempty"`
	Issue    string `json:"issue,omitempty"`
}

type operationalStatus struct {
	SchemaVersion string `json:"schema_version"`
	store.Status
	MessagePolicy          policy.Receipt        `json:"message_policy"`
	ActivePaths            []string              `json:"active_paths"`
	LeasedButNotStaged     []string              `json:"leased_but_not_staged"`
	EmptyLeaseScopes       []string              `json:"empty_lease_scopes"`
	CaptureCandidates      []string              `json:"capture_candidates"`
	CapturedButStillStaged []string              `json:"captured_but_still_staged"`
	StagedLeased           []string              `json:"staged_leased"`
	ForeignStaged          []string              `json:"foreign_staged"`
	ExpiredLeaseIDs        []string              `json:"expired_lease_ids"`
	LeaseExpiresAt         *time.Time            `json:"lease_expires_at"`
	LeaseRemainingMS       int64                 `json:"lease_remaining_ms"`
	Renewal                renewalStatus         `json:"renewal"`
	RefHealth              refHealth             `json:"ref_health"`
	LastIntent             *engine.IntentSummary `json:"last_intent,omitempty"`
}

type laneContext struct {
	Lane              string         `json:"lane"`
	Ref               string         `json:"ref"`
	Tip               string         `json:"tip"`
	LeaseExpiresAt    *time.Time     `json:"lease_expires_at"`
	LeaseRemainingMS  int64          `json:"lease_remaining_ms"`
	LeaseRenewed      bool           `json:"lease_renewed"`
	MessagePolicy     policy.Receipt `json:"message_policy"`
	LatestIntentState string         `json:"latest_intent_state,omitempty"`
}

func (status operationalStatus) context() *laneContext {
	value := &laneContext{Lane: status.Lane.ID, Ref: status.Lane.Ref, Tip: status.Lane.CurrentSHA, LeaseExpiresAt: status.LeaseExpiresAt, LeaseRemainingMS: status.LeaseRemainingMS, LeaseRenewed: status.Renewal.Succeeded, MessagePolicy: status.MessagePolicy}
	if status.LastIntent != nil {
		value.LatestIntentState = status.LastIntent.State
	}
	return value
}

func buildOperationalStatus(ctx context.Context, laneStore store.Store, status store.Status, renewal renewalStatus) (operationalStatus, error) {
	result := operationalStatus{SchemaVersion: "2.0.0", Status: status, Renewal: renewal}
	now := time.Now().UTC()
	var activeScopes []string
	for _, lease := range status.Leases {
		if lease.State == "active" && (lease.ExpiresAt == nil || now.Before(*lease.ExpiresAt)) {
			activeScopes = append(activeScopes, lease.Paths...)
			if lease.ExpiresAt != nil && (result.LeaseExpiresAt == nil || lease.ExpiresAt.Before(*result.LeaseExpiresAt)) {
				expires := *lease.ExpiresAt
				result.LeaseExpiresAt = &expires
			}
		} else if lease.State == "active" {
			result.ExpiredLeaseIDs = append(result.ExpiredLeaseIDs, lease.ID)
		}
	}
	result.ActivePaths = canonicalStrings(activeScopes)
	if result.LeaseExpiresAt != nil {
		result.LeaseRemainingMS = maxInt64(0, result.LeaseExpiresAt.Sub(now).Milliseconds())
	}
	saved, err := loadProfile(laneStore, status.Lane.ID)
	if err != nil {
		return result, err
	}
	result.MessagePolicy = saved.MessagePolicy
	indexEntries, err := laneStore.Repo.IndexEntries(ctx, nil)
	if err != nil {
		return result, fail.Wrap("GIT_FAILED", err)
	}
	laneEntries, err := laneStore.Repo.TreeEntries(ctx, status.Lane.CurrentSHA)
	if err != nil {
		return result, fail.Wrap("GIT_FAILED", err)
	}
	headEntries, err := laneStore.Repo.TreeEntries(ctx, status.Lane.BaseSHA)
	if err != nil {
		return result, fail.Wrap("GIT_FAILED", err)
	}
	paths := map[string]bool{}
	for path := range indexEntries {
		paths[path] = true
	}
	for path := range laneEntries {
		paths[path] = true
	}
	for path := range headEntries {
		paths[path] = true
	}
	for path := range paths {
		leased := pathid.Covered(path, result.ActivePaths)
		staged := indexEntries[path] != headEntries[path]
		pending := indexEntries[path] != laneEntries[path]
		if leased {
			if staged {
				result.StagedLeased = append(result.StagedLeased, path)
			}
			if pending {
				result.CaptureCandidates = append(result.CaptureCandidates, path)
			} else if staged {
				result.CapturedButStillStaged = append(result.CapturedButStillStaged, path)
			}
		} else if staged {
			result.ForeignStaged = append(result.ForeignStaged, path)
		}
	}
	for _, scope := range result.ActivePaths {
		populated := false
		for _, path := range append(append([]string(nil), result.StagedLeased...), result.CaptureCandidates...) {
			if pathid.Covered(path, []string{scope}) {
				populated = true
				break
			}
		}
		if !populated {
			result.LeasedButNotStaged = append(result.LeasedButNotStaged, scope)
		}
	}
	result.LeasedButNotStaged = canonicalStrings(result.LeasedButNotStaged)
	result.EmptyLeaseScopes = append([]string{}, result.LeasedButNotStaged...)
	result.CaptureCandidates = canonicalStrings(result.CaptureCandidates)
	result.CapturedButStillStaged = canonicalStrings(result.CapturedButStillStaged)
	result.StagedLeased = canonicalStrings(result.StagedLeased)
	result.ForeignStaged = canonicalStrings(result.ForeignStaged)
	result.ExpiredLeaseIDs = canonicalStrings(result.ExpiredLeaseIDs)
	result.RefHealth = refHealth{Expected: status.Lane.CurrentSHA}
	actual, refErr := laneStore.Repo.Text(ctx, nil, "rev-parse", "--verify", status.Lane.Ref+"^{commit}")
	if refErr != nil {
		result.RefHealth.Issue = "ref_not_found"
	} else {
		result.RefHealth.Actual = actual
		result.RefHealth.Healthy = actual == status.Lane.CurrentSHA
		if !result.RefHealth.Healthy {
			result.RefHealth.Issue = "ref_tip_mismatch"
		}
	}
	result.LastIntent, err = engine.LatestIntent(laneStore.Repo, status.Lane.Ref)
	if err != nil {
		return result, err
	}
	return result, nil
}

func canonicalStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func laneContextFor(ctx context.Context, laneStore store.Store, laneID string, renewal renewalStatus) (*laneContext, error) {
	status, err := laneStore.Status(laneID)
	if err != nil {
		return nil, err
	}
	operational, err := buildOperationalStatus(ctx, laneStore, status, renewal)
	if err != nil {
		return nil, err
	}
	return operational.context(), nil
}
