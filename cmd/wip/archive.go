package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/store"
)

type archiveResult struct {
	DryRun     bool                     `json:"dry_run"`
	Cutoff     time.Time                `json:"cutoff,omitempty"`
	PlanDigest string                   `json:"plan_digest,omitempty"`
	Candidates []store.ArchiveCandidate `json:"candidates"`
	Receipt    *store.ArchiveReceipt    `json:"receipt,omitempty"`
}

func (application app) runArchive(ctx context.Context, repo gitx.Repo, args []string) int {
	set := application.flagSet("archive")
	olderThan := set.Duration("older-than", 30*24*time.Hour, "minimum age for released or aborted lanes")
	cutoffText := set.String("cutoff", "", "exact RFC3339 cutoff from a reviewed preview")
	expectedDigest := set.String("plan-digest", "", "exact sha256 digest from a reviewed preview")
	apply := set.Bool("apply", false, "move eligible records into a recoverable archive batch")
	yes := set.Bool("yes", false, "confirm archive or restore mutation")
	restore := set.String("restore", "", "restore one archive receipt id")
	resume := set.String("resume", "", "resume one prepared archive receipt id")
	var lanes stringList
	set.Var(&lanes, "lane", "eligible lane to archive (repeatable; default all eligible lanes)")
	if err := set.Parse(args); err != nil {
		return application.failure("archive", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("archive", err, nil, 2)
	}
	if *olderThan <= 0 {
		return application.failure("archive", fail.New("INVALID_ARGS", "--older-than must be positive"), nil, 2)
	}
	if *restore != "" && *resume != "" {
		return application.failure("archive", fail.New("INVALID_ARGS", "--restore and --resume cannot be combined"), nil, 2)
	}
	if *restore != "" {
		if len(lanes) > 0 || strings.TrimSpace(*cutoffText) != "" || strings.TrimSpace(*expectedDigest) != "" {
			return application.failure("archive", fail.New("INVALID_ARGS", "--restore cannot be combined with --lane, --cutoff, or --plan-digest"), nil, 2)
		}
		if !*apply || !*yes {
			return application.failure("archive", fail.New("CONFIRMATION_REQUIRED", "archive restore requires --apply --yes"), nil, 2)
		}
		laneStore, err := inspectArchiveStore(repo)
		if err != nil {
			return application.failure("archive", err, nil, 1)
		}
		receipt, err := laneStore.RestoreArchive(ctx, *restore)
		result := archiveResult{DryRun: false, Candidates: []store.ArchiveCandidate{}, Receipt: &receipt}
		if err != nil {
			return application.failure("archive", err, result, 1)
		}
		return application.success("archive", result, "restored archived coordination records from "+receipt.ID+"; all lane refs remained unchanged")
	}
	if *resume != "" {
		if len(lanes) > 0 || strings.TrimSpace(*cutoffText) != "" || strings.TrimSpace(*expectedDigest) != "" {
			return application.failure("archive", fail.New("INVALID_ARGS", "--resume cannot be combined with --lane, --cutoff, or --plan-digest"), nil, 2)
		}
		if !*apply || !*yes {
			return application.failure("archive", fail.New("CONFIRMATION_REQUIRED", "archive resume requires --apply --yes"), nil, 2)
		}
		laneStore, err := inspectArchiveStore(repo)
		if err != nil {
			return application.failure("archive", err, nil, 1)
		}
		receipt, err := laneStore.ResumeArchive(ctx, *resume)
		result := archiveResult{DryRun: false, Cutoff: receipt.Before, Candidates: receipt.Candidates, Receipt: &receipt}
		if err != nil {
			return application.failure("archive", err, result, 1)
		}
		return application.success("archive", result, "resumed recoverable archive "+receipt.ID+"; all lane refs and commits were preserved")
	}
	cutoff := time.Now().UTC().Add(-*olderThan)
	if strings.TrimSpace(*cutoffText) != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(*cutoffText))
		if parseErr != nil {
			return application.failure("archive", fail.Wrap("INVALID_ARGS", parseErr), nil, 2)
		}
		cutoff = parsed.UTC()
	}
	laneStore, initialized, err := store.Inspect(repo)
	if err != nil {
		return application.failure("archive", err, nil, 1)
	}
	result := archiveResult{DryRun: !*apply, Cutoff: cutoff, Candidates: []store.ArchiveCandidate{}}
	if !initialized {
		return application.success("archive", result, "no wip coordination state is initialized")
	}
	candidates, err := laneStore.ArchiveCandidates(cutoff)
	if err != nil {
		return application.failure("archive", err, result, 1)
	}
	if len(lanes) > 0 {
		wanted := map[string]bool{}
		for _, lane := range lanes {
			if err := store.ValidateID(lane, "lane"); err != nil {
				return application.failure("archive", err, result, 2)
			}
			if wanted[lane] {
				return application.failure("archive", fail.New("INVALID_ARGS", "duplicate --lane value: "+lane), result, 2)
			}
			wanted[lane] = true
		}
		filtered := candidates[:0]
		for _, candidate := range candidates {
			if wanted[candidate.LaneID] {
				filtered = append(filtered, candidate)
				delete(wanted, candidate.LaneID)
			}
		}
		if len(wanted) > 0 {
			missing := make([]string, 0, len(wanted))
			for lane := range wanted {
				missing = append(missing, lane)
			}
			sort.Strings(missing)
			return application.failure("archive", fail.New("ARCHIVE_REFUSED", "selected lanes are not eligible: "+strings.Join(missing, ", ")), result, 1)
		}
		candidates = filtered
	}
	result.Candidates = candidates
	result.PlanDigest, err = archivePlanDigest(cutoff, candidates)
	if err != nil {
		return application.failure("archive", err, result, 1)
	}
	if !*apply {
		return application.success("archive", result, formatArchivePreview(result))
	}
	if !*yes {
		return application.failure("archive", fail.New("CONFIRMATION_REQUIRED", "archiving records requires --apply --yes after reviewing the dry-run"), result, 2)
	}
	if strings.TrimSpace(*cutoffText) == "" || strings.TrimSpace(*expectedDigest) == "" {
		return application.failure("archive", fail.New("ARCHIVE_PLAN_REQUIRED", "apply requires --cutoff and --plan-digest from the reviewed preview"), result, 2)
	}
	if strings.TrimSpace(*expectedDigest) != result.PlanDigest {
		return application.failure("archive", fail.New("ARCHIVE_PLAN_MOVED", "eligible archive records differ from the reviewed plan"), result, 1)
	}
	if len(candidates) == 0 {
		return application.failure("archive", fail.New("ARCHIVE_EMPTY", "no eligible lanes match the reviewed archive plan"), result, 1)
	}
	laneStore, err = store.Open(repo)
	if err != nil {
		return application.failure("archive", err, result, 1)
	}
	receipt, err := laneStore.Archive(ctx, cutoff, candidates)
	result.DryRun = false
	result.Receipt = &receipt
	if err != nil {
		return application.failure("archive", err, result, 1)
	}
	return application.success("archive", result, fmt.Sprintf("archived %d released lane record(s) in %s; all lane refs and commits were preserved", len(receipt.Candidates), receipt.ID))
}

func inspectArchiveStore(repo gitx.Repo) (store.Store, error) {
	laneStore, initialized, err := store.Inspect(repo)
	if err != nil {
		return store.Store{}, err
	}
	if !initialized {
		return store.Store{}, fail.New("ARCHIVE_NOT_FOUND", "archive receipt does not exist because wip state is not initialized")
	}
	return laneStore, nil
}

func formatArchivePreview(result archiveResult) string {
	if len(result.Candidates) == 0 {
		return "archive preview: no released or aborted lanes are older than " + result.Cutoff.Format(time.RFC3339)
	}
	lines := []string{fmt.Sprintf("archive preview: %d recoverable lane record set(s)", len(result.Candidates))}
	for _, candidate := range result.Candidates {
		lines = append(lines, fmt.Sprintf("%s %s %s leases=%d", candidate.LaneID, candidate.State, candidate.Commit, candidate.LeaseCount))
	}
	lines = append(lines, "cutoff: "+result.Cutoff.Format(time.RFC3339Nano), "plan digest: "+result.PlanDigest, "Review this list, then reuse the exact --cutoff and --plan-digest with --apply --yes. Lane refs and commits are never moved or deleted.")
	return strings.Join(lines, "\n")
}

func archivePlanDigest(cutoff time.Time, candidates []store.ArchiveCandidate) (string, error) {
	body, err := json.Marshal(struct {
		Cutoff     time.Time                `json:"cutoff"`
		Candidates []store.ArchiveCandidate `json:"candidates"`
	}{Cutoff: cutoff.UTC(), Candidates: candidates})
	if err != nil {
		return "", fail.Wrap("ARCHIVE_FAILED", err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
