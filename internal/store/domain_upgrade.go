package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/safeio"
)

type DomainUpgradePlan struct {
	Path            string
	CurrentSHA256   string
	DesiredSHA256   string
	CurrentSchema   int
	DesiredSchema   int
	AlreadyCurrent  bool
	UpgradeRequired bool
}

// InspectDomainUpgrade creates a byte-bound domain-marker plan without writes.
func InspectDomainUpgrade(repo gitx.Repo) (DomainUpgradePlan, error) {
	laneStore, initialized, err := Inspect(repo)
	if err != nil {
		return DomainUpgradePlan{}, err
	}
	if !initialized || !laneStore.Domain.MarkerExists {
		return DomainUpgradePlan{}, fail.New("STATE_NOT_INITIALIZED", "coordination state has no domain marker; run wip init instead")
	}
	return inspectDomainUpgrade(repo, laneStore.Domain)
}

// ApplyDomainUpgrade replaces only the exact marker reviewed by the caller.
// The coordination lock serializes marker creation and other domain upgrades.
func ApplyDomainUpgrade(repo gitx.Repo, expectedCurrentSHA256 string) (DomainUpgradePlan, error) {
	lock, err := filelock.Acquire(filepath.Join(repo.CommonDir, "wip-coordination.lock"), filelock.DefaultWait)
	if err != nil {
		return DomainUpgradePlan{}, fail.Wrap("LOCK_TIMEOUT", err)
	}
	defer func() { _ = lock.Release() }()
	laneStore, initialized, err := Inspect(repo)
	if err != nil {
		return DomainUpgradePlan{}, err
	}
	if !initialized || !laneStore.Domain.MarkerExists {
		return DomainUpgradePlan{}, fail.New("STATE_NOT_INITIALIZED", "coordination state has no domain marker; run wip init instead")
	}
	plan, err := inspectDomainUpgrade(repo, laneStore.Domain)
	if err != nil {
		return DomainUpgradePlan{}, err
	}
	if expectedCurrentSHA256 == "" || plan.CurrentSHA256 != expectedCurrentSHA256 {
		return DomainUpgradePlan{}, fail.New("UPGRADE_PLAN_MOVED", "domain marker changed after preview; review a new upgrade plan")
	}
	if plan.AlreadyCurrent {
		return plan, nil
	}
	body, err := currentDomainBody()
	if err != nil {
		return DomainUpgradePlan{}, err
	}
	if err := atomicfile.Write(plan.Path, body, 0o600); err != nil {
		return DomainUpgradePlan{}, fail.Wrap("UPGRADE_FAILED", err)
	}
	verified, err := inspectDomainUpgrade(repo, domainMetadata(currentDomainRecord(), true))
	if err != nil {
		return DomainUpgradePlan{}, err
	}
	if verified.CurrentSHA256 != plan.DesiredSHA256 || !verified.AlreadyCurrent {
		return DomainUpgradePlan{}, fail.New("UPGRADE_FAILED", "domain marker verification failed after atomic replacement")
	}
	return verified, nil
}

func inspectDomainUpgrade(repo gitx.Repo, metadata DomainMetadata) (DomainUpgradePlan, error) {
	path := filepath.Join(repo.CommonDir, "wip", "domain.json")
	current, err := safeio.ReadRegular(path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return DomainUpgradePlan{}, fail.New("STATE_NOT_INITIALIZED", "coordination state has no domain marker; run wip init instead")
	}
	if err != nil {
		return DomainUpgradePlan{}, fail.Wrap("STORE_FAILED", err)
	}
	desired, err := currentDomainBody()
	if err != nil {
		return DomainUpgradePlan{}, err
	}
	currentDigest, desiredDigest := digestBytes(current), digestBytes(desired)
	return DomainUpgradePlan{
		Path: path, CurrentSHA256: currentDigest, DesiredSHA256: desiredDigest,
		CurrentSchema: metadata.SchemaVersion, DesiredSchema: domainSchemaVersion,
		AlreadyCurrent: currentDigest == desiredDigest, UpgradeRequired: metadata.UpgradeRequired,
	}, nil
}

func currentDomainBody() ([]byte, error) {
	body, err := json.MarshalIndent(currentDomainRecord(), "", "  ")
	if err != nil {
		return nil, fail.Wrap("UPGRADE_FAILED", err)
	}
	return append(body, '\n'), nil
}

func digestBytes(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}
