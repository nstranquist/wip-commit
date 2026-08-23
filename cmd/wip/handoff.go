package main

import (
	"context"
	"fmt"
	"time"

	"github.com/nstranquist/wip-commit/internal/engine"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/policy"
	"github.com/nstranquist/wip-commit/internal/store"
)

type handoffCommit struct {
	Index        int      `json:"index"`
	Message      string   `json:"message"`
	Parent       string   `json:"parent"`
	Commit       string   `json:"commit"`
	Tree         string   `json:"tree"`
	ChangedPaths []string `json:"changed_paths"`
}

type captureHandoff struct {
	SchemaVersion       string          `json:"schema_version"`
	Kind                string          `json:"kind"`
	CaptureOnly         bool            `json:"capture_only"`
	LandingAuthorized   bool            `json:"landing_authorized"`
	LaneRef             string          `json:"lane_ref"`
	LaneTip             string          `json:"lane_tip"`
	LaneBaseCommit      string          `json:"lane_base_commit"`
	ParentCommit        string          `json:"parent_commit"`
	FinalTree           string          `json:"final_tree"`
	PlanID              string          `json:"plan_id"`
	CaptureIntentDigest string          `json:"capture_intent_digest"`
	ReviewedPlanDigest  string          `json:"reviewed_plan_digest,omitempty"`
	MessagePolicy       *policy.Receipt `json:"message_policy,omitempty"`
	MessagePolicyBound  bool            `json:"message_policy_bound"`
	IntentState         string          `json:"intent_state"`
	Commits             []handoffCommit `json:"commits"`
}

type guidedCaptureResult struct {
	Capture      engine.Result  `json:"capture"`
	Handoff      captureHandoff `json:"handoff"`
	LaneReleased bool           `json:"lane_released"`
}

func buildCaptureHandoff(ctx context.Context, laneStore store.Store, lane store.Lane, planID, planDigest string) (captureHandoff, error) {
	intent, alreadyClean, err := engine.ValidateApplied(ctx, laneStore.Repo, planID, planDigest, lane.Ref, lane.CurrentSHA)
	if err != nil {
		return captureHandoff{}, err
	}
	if !alreadyClean || intent.State != "complete" {
		return captureHandoff{}, fail.New("HANDOFF_NOT_READY", "capture intent must be complete before a handoff can be issued")
	}
	handoff := captureHandoff{
		SchemaVersion:       "1.0.0",
		Kind:                "capture_handoff",
		CaptureOnly:         true,
		LandingAuthorized:   false,
		LaneRef:             lane.Ref,
		LaneTip:             intent.ExpectedNew,
		LaneBaseCommit:      lane.BaseSHA,
		ParentCommit:        intent.ExpectedOld,
		FinalTree:           intent.FinalTree,
		PlanID:              intent.PlanID,
		CaptureIntentDigest: intent.PlanDigest,
		ReviewedPlanDigest:  intent.ReviewPlanDigest,
		MessagePolicy:       intent.MessagePolicy,
		MessagePolicyBound:  intent.MessagePolicy != nil,
		IntentState:         intent.State,
		Commits:             make([]handoffCommit, 0, len(intent.Commits)),
	}
	for _, commit := range intent.Commits {
		handoff.Commits = append(handoff.Commits, handoffCommit{Index: commit.Index, Message: commit.Message, Parent: commit.Parent, Commit: commit.Commit, Tree: commit.Tree, ChangedPaths: append([]string(nil), commit.ChangedPaths...)})
	}
	return handoff, nil
}

func (application app) runHandoff(ctx context.Context, laneStore store.Store, args []string) int {
	set := application.flagSet("handoff")
	var identity identityFlags
	identity.bind(set)
	planID := set.String("plan-id", "", "capture intent id (default latest intent for the lane ref)")
	planDigest := set.String("plan-digest", "", "capture intent digest; required with --plan-id")
	if err := set.Parse(args); err != nil {
		return application.failure("handoff", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("handoff", err, nil, 2)
	}
	if (*planID == "") != (*planDigest == "") {
		return application.failure("handoff", fail.New("INVALID_ARGS", "--plan-id and --plan-digest must be supplied together"), nil, 2)
	}
	status, err := resolveHandoffStatus(laneStore, identity)
	if err != nil {
		return application.failure("handoff", err, nil, 1)
	}
	if status.Lane.State == "active" {
		writable, openErr := store.Open(laneStore.Repo)
		if openErr != nil {
			return application.failure("handoff", openErr, nil, 1)
		}
		laneStore = writable
	}
	lock, err := laneStore.ExistingLaneLock(status.Lane.ID, filelock.DefaultWait)
	if err != nil {
		return application.failure("handoff", err, nil, 1)
	}
	defer func() { _ = lock.Release() }()
	status, err = resolveHandoffStatus(laneStore, identity)
	if err != nil {
		return application.failure("handoff", err, nil, 1)
	}
	if *planID == "" {
		latest, latestErr := engine.LatestIntent(laneStore.Repo, status.Lane.Ref)
		if latestErr != nil {
			return application.failure("handoff", latestErr, nil, 1)
		}
		if latest == nil {
			return application.failure("handoff", fail.New("HANDOFF_NOT_READY", "lane has no capture intent"), nil, 1)
		}
		*planID, *planDigest = latest.PlanID, latest.PlanDigest
	}
	handoff, err := buildCaptureHandoff(ctx, laneStore, status.Lane, *planID, *planDigest)
	if err != nil {
		return application.failure("handoff", err, nil, 1)
	}
	if application.afterHandoffValidation != nil {
		application.afterHandoffValidation()
	}
	renewal := renewalStatus{}
	if status.Lane.State == "active" {
		renewal.Attempted = true
		if _, err := laneStore.RenewWithLockHeld(status.Lane); err != nil {
			return application.failure("handoff", err, nil, 1)
		}
		now := time.Now().UTC()
		renewal.Succeeded, renewal.RenewedAt = true, &now
		status, err = laneStore.Status(status.Lane.ID)
		if err != nil {
			return application.failure("handoff", err, handoff, 1)
		}
	}
	operational, inspectErr := buildOperationalStatus(ctx, laneStore, status, renewal)
	if inspectErr != nil {
		return application.failure("handoff", inspectErr, nil, 1)
	}
	human := fmt.Sprintf("capture-only handoff for %s at %s (%d commit(s)); landing is not authorized", handoff.LaneRef, handoff.LaneTip, len(handoff.Commits))
	return application.successWithContext("handoff", handoff, human, operational.context())
}

func resolveHandoffStatus(laneStore store.Store, identity identityFlags) (store.Status, error) {
	if identity.lane == "" {
		return resolveStatus(laneStore, identity)
	}
	saved, err := loadProfile(laneStore, identity.lane)
	if err != nil {
		return store.Status{}, err
	}
	if identity.agent != "" && identity.agent != saved.Agent || identity.session != "" && identity.session != saved.Session {
		return store.Status{}, fail.New("IDENTITY_MISMATCH", "lane belongs to another agent or session")
	}
	status, err := laneStore.Status(identity.lane)
	if err != nil {
		return store.Status{}, err
	}
	if status.Lane.Worktree != laneStore.Repo.Root {
		return store.Status{}, fail.New("WORKTREE_MISMATCH", "lane is bound to a different checkout")
	}
	return status, nil
}
