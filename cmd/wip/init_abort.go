package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/store"
)

type initAbortResult struct {
	SchemaVersion  string   `json:"schema_version"`
	IntentID       string   `json:"intent_id"`
	IntentDigest   string   `json:"intent_digest"`
	Lane           string   `json:"lane"`
	Agent          string   `json:"agent"`
	Session        string   `json:"session"`
	CompletedSteps []string `json:"completed_steps"`
	CanAbort       bool     `json:"can_abort"`
	Applied        bool     `json:"applied"`
	ArchiveRef     string   `json:"archive_ref,omitempty"`
	ArchiveObject  string   `json:"archive_object,omitempty"`
}

func (application app) runInitAbort(ctx context.Context, repo gitx.Repo, args []string) int {
	set := application.flagSet("init-abort")
	intentID := set.String("intent", "", "pending initialization intent id")
	intentDigest := set.String("intent-digest", "", "exact digest from the reviewed preview")
	apply := set.Bool("apply", false, "remove the reviewed safe intent")
	yes := set.Bool("yes", false, "confirm the reviewed abort")
	if err := set.Parse(args); err != nil {
		return application.failure("init-abort", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("init-abort", err, nil, 2)
	}
	if strings.TrimSpace(*intentID) == "" {
		return application.failure("init-abort", fail.New("INVALID_ARGS", "--intent is required"), nil, 2)
	}
	if *apply != *yes {
		return application.failure("init-abort", fail.New("INVALID_ARGS", "abort requires both --apply and --yes"), nil, 2)
	}
	if *apply && strings.TrimSpace(*intentDigest) == "" {
		return application.failure("init-abort", fail.New("INVALID_ARGS", "--intent-digest is required with --apply"), nil, 2)
	}

	laneStore, initialized, err := store.Inspect(repo)
	if err != nil {
		return application.failure("init-abort", err, nil, 1)
	}
	if !initialized {
		return application.failure("init-abort", fail.New("LANE_NOT_ACTIVE", "wip state is not initialized"), nil, 1)
	}
	if *apply {
		laneStore, err = store.Open(repo)
		if err != nil {
			return application.failure("init-abort", err, nil, 1)
		}
	}
	result, err := inspectInitAbort(ctx, laneStore, *intentID, *intentDigest, *apply)
	if err != nil {
		return application.failure("init-abort", err, result, 1)
	}
	human := fmt.Sprintf("initialization intent %s can be safely aborted with digest %s", result.IntentID, result.IntentDigest)
	if result.Applied {
		human = fmt.Sprintf("aborted pre-lane initialization intent %s", result.IntentID)
	}
	return application.success("init-abort", result, human)
}

func inspectInitAbort(ctx context.Context, laneStore store.Store, id, expectedDigest string, apply bool) (initAbortResult, error) {
	result := initAbortResult{SchemaVersion: "1.0.0"}
	if err := store.ValidateID(id, "init intent"); err != nil || !strings.HasPrefix(id, "init-") {
		if err == nil {
			err = fail.New("INVALID_ID", "initialization intent id must use the init- prefix")
		}
		return result, err
	}
	intentPath := filepath.Join(laneStore.Root, "init-intents", id+".json")
	lock, err := filelock.Acquire(initIntentLockPath(intentPath), 0)
	if err != nil {
		return result, fail.Wrap("LOCK_TIMEOUT", err)
	}
	defer func() { _ = lock.Release() }()

	archiveRef := "refs/wip/init-aborts/" + id
	intent, archiveObject, archived, err := loadInitAbortIntent(ctx, laneStore, intentPath, archiveRef)
	if err != nil {
		return result, err
	}
	result.IntentID = intent.ID
	result.IntentDigest = intent.Digest
	result.Lane = intent.Lane
	result.Agent = intent.Agent
	result.Session = intent.Session
	result.CompletedSteps = append([]string(nil), intent.CompletedSteps...)
	result.ArchiveRef = archiveRef
	result.ArchiveObject = archiveObject
	if expectedDigest != "" && expectedDigest != intent.Digest {
		return result, fail.New("INIT_INTENT_MOVED", "initialization intent digest does not match the reviewed preview")
	}
	if archived {
		result.CanAbort = true
		result.Applied = true
		return result, nil
	}
	if intent.State != "pending" {
		return result, fail.New("INIT_ABORT_REFUSED", "only a pending initialization intent can be aborted")
	}
	if len(intent.CompletedSteps) > 1 || len(intent.CompletedSteps) == 1 && intent.CompletedSteps[0] != "worktree-ready" {
		return result, fail.New("INIT_ABORT_REFUSED", "initialization reached lane creation; use its typed lane recovery")
	}
	if _, loadErr := laneStore.Load(intent.Lane); loadErr == nil || fail.Code(loadErr) != "LANE_NOT_FOUND" {
		if loadErr == nil {
			loadErr = fail.New("INIT_ABORT_REFUSED", "initialization created a lane record")
		}
		return result, loadErr
	}
	if _, profileErr := os.Lstat(filepath.Join(laneStore.Root, "profiles", intent.Lane+".json")); profileErr == nil || !errors.Is(profileErr, os.ErrNotExist) {
		if profileErr == nil {
			profileErr = fail.New("INIT_ABORT_REFUSED", "initialization created a lane profile")
		}
		return result, profileErr
	}
	ref := store.LaneRef(intent.Agent, intent.Lane)
	if _, refErr := laneStore.Repo.Text(ctx, nil, "rev-parse", "--verify", ref+"^{commit}"); refErr == nil {
		return result, fail.New("INIT_ABORT_REFUSED", "initialization created a lane ref")
	}
	leaseEntries, err := os.ReadDir(filepath.Join(laneStore.Root, "leases"))
	if err != nil {
		return result, fail.Wrap("INIT_ABORT_REFUSED", err)
	}
	if len(leaseEntries) > maxDoctorEntries {
		return result, fail.New("INIT_ABORT_REFUSED", "lease registry exceeds the bounded recovery scan")
	}
	for _, entry := range leaseEntries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			return result, fail.New("INIT_ABORT_REFUSED", "lease registry contains an unexpected entry")
		}
		leaseID := strings.TrimSuffix(entry.Name(), ".json")
		lease, loadErr := laneStore.LoadLease(leaseID)
		if loadErr != nil {
			return result, loadErr
		}
		if lease.LaneID == intent.Lane {
			return result, fail.New("INIT_ABORT_REFUSED", "initialization created a path lease")
		}
	}
	result.CanAbort = true
	if !apply {
		return result, nil
	}
	archiveObject, err = publishInitAbortArchive(ctx, laneStore.Repo, intentPath, archiveRef, intent)
	if err != nil {
		return result, err
	}
	result.ArchiveObject = archiveObject
	if err := os.Remove(intentPath); err != nil {
		return result, fail.Wrap("INIT_ABORT_FAILED", err)
	}
	result.Applied = true
	if runtime.GOOS != "windows" {
		directory, openErr := os.Open(filepath.Dir(intentPath))
		if openErr != nil {
			return result, fail.Wrap("INIT_ABORT_FAILED", openErr)
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil {
			return result, fail.Wrap("INIT_ABORT_FAILED", syncErr)
		}
		if closeErr != nil {
			return result, fail.Wrap("INIT_ABORT_FAILED", closeErr)
		}
	}
	return result, nil
}

func loadInitAbortIntent(ctx context.Context, laneStore store.Store, intentPath, archiveRef string) (initIntent, string, bool, error) {
	intent, err := loadInitIntent(intentPath)
	if err == nil {
		return intent, "", false, nil
	}
	if fail.Code(err) != "INIT_INTENT_NOT_FOUND" {
		return initIntent{}, "", false, err
	}
	object, refErr := laneStore.Repo.Text(ctx, nil, "rev-parse", "--verify", archiveRef+"^{blob}")
	if refErr != nil {
		return initIntent{}, "", false, err
	}
	body, blobErr := laneStore.Repo.Raw(ctx, nil, "cat-file", "blob", object)
	if blobErr != nil {
		return initIntent{}, "", false, fail.Wrap("INIT_ABORT_FAILED", blobErr)
	}
	intent, decodeErr := decodeInitIntent([]byte(body))
	if decodeErr != nil {
		return initIntent{}, "", false, decodeErr
	}
	if intent.ID != strings.TrimSuffix(filepath.Base(intentPath), ".json") {
		return initIntent{}, "", false, fail.New("INIT_ABORT_FAILED", "archived initialization intent identity does not match the requested intent")
	}
	return intent, object, true, nil
}

func publishInitAbortArchive(ctx context.Context, repo gitx.Repo, intentPath, archiveRef string, intent initIntent) (string, error) {
	if _, err := repo.Text(ctx, nil, "check-ref-format", archiveRef); err != nil {
		return "", fail.Wrap("INIT_ABORT_FAILED", err)
	}
	object, err := repo.Text(ctx, nil, "hash-object", "-w", "--", intentPath)
	if err != nil {
		return "", fail.Wrap("INIT_ABORT_FAILED", err)
	}
	existing, refErr := repo.Text(ctx, nil, "rev-parse", "--verify", archiveRef+"^{blob}")
	if refErr == nil {
		if existing != object {
			return "", fail.New("INIT_ABORT_REFUSED", "initialization abort archive ref already contains different evidence")
		}
	} else if _, err := repo.Text(ctx, nil, "update-ref", "-m", "wip init-abort: preserve recovery evidence", archiveRef, object, ""); err != nil {
		return "", fail.Wrap("INIT_ABORT_FAILED", err)
	}
	body, err := repo.Raw(ctx, nil, "cat-file", "blob", object)
	if err != nil {
		return "", fail.Wrap("INIT_ABORT_FAILED", err)
	}
	archived, err := decodeInitIntent([]byte(body))
	if err != nil {
		return "", err
	}
	if archived.ID != intent.ID || archived.Digest != intent.Digest {
		return "", fail.New("INIT_ABORT_FAILED", "archived initialization evidence does not match the active intent")
	}
	return object, nil
}
