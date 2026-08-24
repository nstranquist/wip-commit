package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nstranquist/wip-commit/internal/engine"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/policy"
	"github.com/nstranquist/wip-commit/internal/store"
)

const (
	maxHandoffBytes    = 16 << 20
	maxHandoffSegments = 1_000
	maxHandoffCommits  = 10_000
)

type handoffCommit struct {
	Index        int      `json:"index"`
	Message      string   `json:"message"`
	Parent       string   `json:"parent"`
	Commit       string   `json:"commit"`
	Tree         string   `json:"tree"`
	ChangedPaths []string `json:"changed_paths"`
	VerifyCount  int      `json:"verify_count"`
	Repairs      int      `json:"repairs_applied"`
}

type handoffSegment struct {
	Index               int                    `json:"index"`
	PlanID              string                 `json:"plan_id"`
	CaptureIntentDigest string                 `json:"capture_intent_digest"`
	ReviewedPlanDigest  string                 `json:"reviewed_plan_digest,omitempty"`
	MessagePolicy       *policy.Receipt        `json:"message_policy,omitempty"`
	MessagePolicyBound  bool                   `json:"message_policy_bound"`
	IntentState         string                 `json:"intent_state"`
	SourceHead          string                 `json:"source_head"`
	SourceMode          engine.SourceMode      `json:"source_mode,omitempty"`
	UntrackedPolicy     engine.UntrackedPolicy `json:"untracked_policy,omitempty"`
	SourceDigest        string                 `json:"source_digest,omitempty"`
	SourcePaths         []string               `json:"source_paths,omitempty"`
	ParentCommit        string                 `json:"parent_commit"`
	FinalCommit         string                 `json:"final_commit"`
	FinalTree           string                 `json:"final_tree"`
	Commits             []handoffCommit        `json:"commits"`
}

type captureHandoff struct {
	SchemaVersion          string           `json:"schema_version"`
	Kind                   string           `json:"kind"`
	HandoffDigest          string           `json:"handoff_digest"`
	CaptureOnly            bool             `json:"capture_only"`
	LandingAuthorized      bool             `json:"landing_authorized"`
	ReviewEligible         bool             `json:"review_eligible"`
	LaneRef                string           `json:"lane_ref"`
	LaneTip                string           `json:"lane_tip"`
	LaneBaseCommit         string           `json:"lane_base_commit"`
	FinalTree              string           `json:"final_tree"`
	TipPlanID              string           `json:"tip_plan_id"`
	TipCaptureIntentDigest string           `json:"tip_capture_intent_digest"`
	Segments               []handoffSegment `json:"segments"`
}

type guidedCaptureResult struct {
	Capture      engine.Result  `json:"capture"`
	Handoff      captureHandoff `json:"handoff"`
	LaneReleased bool           `json:"lane_released"`
}

func buildCaptureHandoff(ctx context.Context, laneStore store.Store, lane store.Lane, planID, planDigest string) (captureHandoff, error) {
	chain, err := engine.CompleteIntentChain(ctx, laneStore.Repo, lane.Ref, lane.BaseSHA, lane.CurrentSHA)
	if err != nil {
		return captureHandoff{}, err
	}
	if len(chain) == 0 {
		return captureHandoff{}, fail.New("HANDOFF_NOT_READY", "lane has no completed capture intent chain")
	}
	if len(chain) > maxHandoffSegments {
		return captureHandoff{}, fail.Errorf("HANDOFF_TOO_LARGE", "capture chain has %d segments; maximum is %d", len(chain), maxHandoffSegments)
	}
	tip := chain[len(chain)-1]
	if planID != "" || planDigest != "" {
		if planID == "" || planDigest == "" || tip.PlanID != planID || tip.PlanDigest != planDigest {
			return captureHandoff{}, fail.New("HANDOFF_TIP_MISMATCH", "requested capture intent does not match the lane tip segment")
		}
	}
	handoff := captureHandoff{
		SchemaVersion:          "2.0.0",
		Kind:                   "capture_handoff",
		CaptureOnly:            true,
		LandingAuthorized:      false,
		ReviewEligible:         true,
		LaneRef:                lane.Ref,
		LaneTip:                lane.CurrentSHA,
		LaneBaseCommit:         lane.BaseSHA,
		FinalTree:              tip.FinalTree,
		TipPlanID:              tip.PlanID,
		TipCaptureIntentDigest: tip.PlanDigest,
		Segments:               make([]handoffSegment, 0, len(chain)),
	}
	for segmentIndex, intent := range chain {
		messagePolicyBound := intent.ReviewPlanDigest != "" && intent.MessagePolicy != nil
		segment := handoffSegment{
			Index: segmentIndex + 1, PlanID: intent.PlanID, CaptureIntentDigest: intent.PlanDigest,
			ReviewedPlanDigest: intent.ReviewPlanDigest, MessagePolicy: intent.MessagePolicy,
			MessagePolicyBound: messagePolicyBound, IntentState: intent.State,
			SourceHead: intent.SourceHead, SourceMode: intent.SourceMode, UntrackedPolicy: intent.UntrackedPolicy,
			SourceDigest: intent.SourceDigest, SourcePaths: append([]string(nil), intent.SourcePaths...),
			ParentCommit: intent.ExpectedOld, FinalCommit: intent.ExpectedNew, FinalTree: intent.FinalTree,
			Commits: make([]handoffCommit, 0, len(intent.Commits)),
		}
		if !messagePolicyBound || intent.SourceMode == "" || intent.UntrackedPolicy == "" || intent.SourceDigest == "" || len(intent.SourcePaths) == 0 {
			handoff.ReviewEligible = false
		}
		for _, commit := range intent.Commits {
			segment.Commits = append(segment.Commits, handoffCommit{
				Index: commit.Index, Message: commit.Message, Parent: commit.Parent, Commit: commit.Commit,
				Tree: commit.Tree, ChangedPaths: append([]string(nil), commit.ChangedPaths...),
				VerifyCount: commit.VerifyCount, Repairs: commit.Repairs,
			})
		}
		handoff.Segments = append(handoff.Segments, segment)
	}
	if count := handoffCommitCount(handoff); count > maxHandoffCommits {
		return captureHandoff{}, fail.Errorf("HANDOFF_TOO_LARGE", "capture chain has %d commits; maximum is %d", count, maxHandoffCommits)
	}
	handoff.HandoffDigest, err = digestCaptureHandoff(handoff)
	if err != nil {
		return captureHandoff{}, err
	}
	return handoff, nil
}

func digestCaptureHandoff(handoff captureHandoff) (string, error) {
	handoff.HandoffDigest = ""
	body, err := json.Marshal(handoff)
	if err != nil {
		return "", fail.Wrap("HANDOFF_INVALID", err)
	}
	if len(body) > maxHandoffBytes {
		return "", fail.Errorf("HANDOFF_TOO_LARGE", "capture handoff is %d bytes; maximum is %d", len(body), maxHandoffBytes)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func handoffCommitCount(handoff captureHandoff) int {
	total := 0
	for _, segment := range handoff.Segments {
		total += len(segment.Commits)
	}
	return total
}

func (application app) runHandoff(ctx context.Context, laneStore store.Store, args []string) int {
	set := application.flagSet("handoff")
	var identity identityFlags
	identity.bind(set)
	planID := set.String("plan-id", "", "tip capture intent id (default complete lane chain)")
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
	if status.Lane.State == "active" && !laneStore.WriterFenced() {
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
	human := fmt.Sprintf("cumulative capture-only handoff for %s at %s (%d segment(s), %d commit(s)); landing is not authorized", handoff.LaneRef, handoff.LaneTip, len(handoff.Segments), handoffCommitCount(handoff))
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
