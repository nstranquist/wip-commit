package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
)

// CompleteIntentChain returns the unique, validated sequence of completed
// capture intents from baseCommit through tipCommit. It fails on a gap,
// ambiguity, foreign branch, moved ref, invalid record, or invalid Git object.
func CompleteIntentChain(ctx context.Context, repo gitx.Repo, targetRef, baseCommit, tipCommit string) ([]Intent, error) {
	if !validIntentTargetRef(targetRef) || !intentObjectPattern.MatchString(baseCommit) || !intentObjectPattern.MatchString(tipCommit) {
		return nil, fail.New("INVALID_HANDOFF_CHAIN", "handoff chain identity is invalid")
	}
	actual, err := repo.Text(ctx, nil, "rev-parse", "--verify", targetRef+"^{commit}")
	if err != nil {
		return nil, fail.Wrap("REF_NOT_FOUND", err)
	}
	if actual != tipCommit {
		return nil, fail.New("REF_MOVED", "lane ref changed before cumulative handoff validation")
	}
	if baseCommit == tipCommit {
		return []Intent{}, nil
	}

	directory := filepath.Join(repo.CommonDir, "wip", "v1", "intents")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fail.New("HANDOFF_CHAIN_INCOMPLETE", "capture intent directory does not exist")
	}
	if err != nil {
		return nil, fail.Wrap("INTENT_READ_FAILED", err)
	}
	if len(entries) > 10_000 {
		return nil, fail.New("INTENT_READ_FAILED", "intent directory exceeds 10000 entries")
	}
	byParent := make(map[string][]Intent)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !planIDPattern.MatchString(id) {
			return nil, fail.New("INTENT_READ_FAILED", "intent directory contains an invalid record name")
		}
		intent, _, err := LoadIntent(repo, id, "")
		if err != nil {
			return nil, err
		}
		if intent.TargetRef != targetRef || intent.State != "complete" {
			continue
		}
		if err := ValidateIntentObjects(ctx, repo, intent); err != nil {
			return nil, err
		}
		byParent[intent.ExpectedOld] = append(byParent[intent.ExpectedOld], intent)
	}

	chain := make([]Intent, 0)
	cursor := baseCommit
	seen := map[string]bool{}
	for cursor != tipCommit {
		if seen[cursor] || len(chain) >= 10_000 {
			return nil, fail.New("HANDOFF_CHAIN_AMBIGUOUS", "capture intent chain contains a cycle or exceeds its bound")
		}
		seen[cursor] = true
		var candidates []Intent
		for _, intent := range byParent[cursor] {
			code, err := repo.Exit(ctx, nil, "merge-base", "--is-ancestor", intent.ExpectedNew, tipCommit)
			if err != nil {
				return nil, fail.Wrap("GIT_FAILED", err)
			}
			if code == 0 {
				candidates = append(candidates, intent)
			} else if code != 1 {
				return nil, fail.New("GIT_FAILED", "Git could not validate a capture segment against the lane tip")
			}
		}
		if len(candidates) == 0 {
			return nil, fail.Errorf("HANDOFF_CHAIN_INCOMPLETE", "no completed capture intent continues lane commit %s", cursor)
		}
		if len(candidates) != 1 {
			return nil, fail.Errorf("HANDOFF_CHAIN_AMBIGUOUS", "%d completed capture intents continue lane commit %s", len(candidates), cursor)
		}
		chain = append(chain, candidates[0])
		cursor = candidates[0].ExpectedNew
	}
	return chain, nil
}
