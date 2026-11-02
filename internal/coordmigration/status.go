package coordmigration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nstranquist/wip-commit/internal/contract"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/safeio"
)

// InspectStatus reads migration and store state without creating a directory,
// lock, marker, or receipt.
func InspectStatus(repo gitx.Repo, migrationID string) (Status, error) {
	if migrationID != "" && !migrationIDPattern.MatchString(migrationID) {
		return Status{}, fail.New("MIGRATION_STATUS_INVALID", "migration identity is invalid")
	}
	status := Status{SchemaVersion: StatusSchemaVersion, State: "not-started", MigrationID: migrationID}
	legacy, err := regularDirectoryState(filepath.Join(repo.CommonDir, "ndev-wip"))
	if err != nil {
		return Status{}, fail.Wrap("MIGRATION_SOURCE_UNSAFE", err)
	}
	status.LegacyExists = legacy
	canonical, err := exactCanonicalDomain(repo.CommonDir)
	if err != nil {
		return Status{}, err
	}
	status.Canonical = canonical
	review, err := regularDirectoryState(filepath.Join(repo.CommonDir, "wip", "review", "v1"))
	if err != nil {
		return Status{}, fail.Wrap("MIGRATION_DESTINATION_UNSAFE", err)
	}
	status.Review = review

	journal, journalExists, err := readJournal(migrationJournalPath(repo.CommonDir))
	if err != nil {
		return Status{}, err
	}
	if migrationID == "" && journalExists {
		migrationID = journal.MigrationID
		status.MigrationID = migrationID
	}
	if journalExists && journal.MigrationID == migrationID {
		status.PlanDigest = journal.PlanDigest
		status.Phase = journal.Phase
		status.State = "in-progress"
	}
	if migrationID == "" {
		if legacy {
			status.State = "preview-required"
		} else if canonical {
			status.State = "canonical"
		}
		return status, nil
	}

	var plan Plan
	planExists := false
	if loaded, loadErr := loadPersistedPlan(repo.CommonDir, migrationID); loadErr == nil {
		plan = loaded
		planExists = true
		if plan.MigrationID != migrationID {
			return Status{}, fail.New("MIGRATION_PLAN_INVALID", "persisted migration plan has a different migration identity")
		}
		if err := plan.ValidateDigest(plan.PlanDigest); err != nil {
			return Status{}, fail.Wrap("MIGRATION_PLAN_INVALID", err)
		}
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return Status{}, fail.Wrap("MIGRATION_PLAN_INVALID", loadErr)
	} else if journalExists && journal.MigrationID == migrationID {
		return Status{}, fail.New("MIGRATION_PLAN_INVALID", "migration journal exists without its immutable reviewed plan")
	}
	if planExists && journalExists && journal.MigrationID == migrationID {
		if err := verifyJournalPlanBinding(repo, plan, journal); err != nil {
			return Status{}, err
		}
	}

	rollbackPath := filepath.Join(repo.CommonDir, "wip", "migration", "v1", migrationID, "rollback.json")
	var rollback RollbackMarker
	if _, err := readJSONFile(rollbackPath, &rollback); err == nil {
		if rollback.SchemaVersion != RollbackSchemaVersion || rollback.MigrationID != migrationID || rollback.State != "rolled-back" || !digestPattern.MatchString(rollback.PlanDigest) || !planExists || rollback.PlanDigest != plan.PlanDigest {
			return Status{}, fail.New("MIGRATION_ROLLBACK_INVALID", "rollback marker is invalid")
		}
		status.Rollback = &rollback
		status.State = "rolled-back"
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, fail.Wrap("MIGRATION_ROLLBACK_INVALID", err)
	}

	receiptPath := migrationReceiptPath(repo.CommonDir, migrationID)
	var receipt Receipt
	if _, err := readJSONFile(receiptPath, &receipt); err == nil {
		if err := validateReceipt(receipt, migrationID); err != nil {
			return Status{}, fail.Wrap("MIGRATION_RECEIPT_INVALID", err)
		}
		status.Receipt = &receipt
		status.PlanDigest = receipt.PlanDigest
		if !journalExists || journal.MigrationID != migrationID || journal.Phase == phaseComplete {
			status.State = "complete"
		}
		if !planExists {
			return Status{}, fail.New("MIGRATION_PLAN_INVALID", "completion receipt exists without its immutable migration plan")
		}
		if !journalExists || journal.MigrationID != migrationID {
			return Status{}, fail.New("MIGRATION_RECEIPT_INVALID", "completion receipt exists without its exact migration journal")
		}
		if receipt.PlanDigest != plan.PlanDigest || receipt.SourceManifest != plan.SourceLegacy.Digest || receipt.ReconciliationHistory != plan.ReconciliationHistory.Digest || receipt.CanonicalManifest != plan.PlannedCanonicalData.Digest {
			return Status{}, fail.New("MIGRATION_RECEIPT_INVALID", "completion receipt does not bind the persisted plan and planned canonical manifest")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, fail.Wrap("MIGRATION_RECEIPT_INVALID", err)
	}

	archiveRoot := archiveMigrationRoot(repo.CommonDir, migrationID)
	archiveExists, err := regularDirectoryState(filepath.Join(archiveRoot, "source"))
	if err != nil {
		return Status{}, fail.Wrap("MIGRATION_ARCHIVE_INVALID", err)
	}
	if archiveExists {
		var seal Seal
		if _, err := readJSONFile(filepath.Join(archiveRoot, "seal.json"), &seal); err != nil {
			return Status{}, fail.Wrap("MIGRATION_ARCHIVE_INVALID", err)
		}
		manifest, err := manifestTree(repo.CommonDir, filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", migrationID, "source")))
		expectedRelative := filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", migrationID, "source"))
		if err != nil || seal.SchemaVersion != SealSchemaVersion || seal.MigrationID != migrationID || !digestPattern.MatchString(seal.PlanDigest) || !digestPattern.MatchString(seal.SourceManifest) || seal.ArchiveManifest != manifest.Digest || seal.ArchiveRelativeDir != expectedRelative {
			if err != nil {
				return Status{}, fail.Wrap("MIGRATION_ARCHIVE_INVALID", err)
			}
			return Status{}, fail.New("MIGRATION_ARCHIVE_INVALID", "archive seal does not match the archived source")
		}
		status.Archive = true
		if journalExists && journal.MigrationID == migrationID && seal.PlanDigest != journal.PlanDigest {
			return Status{}, fail.New("MIGRATION_ARCHIVE_INVALID", "archive seal and migration journal do not bind the same plan")
		}
		if status.Receipt != nil && (seal.PlanDigest != status.Receipt.PlanDigest || seal.SourceManifest != status.Receipt.SourceManifest || seal.ArchiveManifest != status.Receipt.ArchiveManifest) {
			return Status{}, fail.New("MIGRATION_ARCHIVE_INVALID", "archive seal and completion receipt do not bind the same source and plan")
		}
	}
	if status.Receipt != nil && status.Receipt.ArchiveManifest != "" && !status.Archive {
		return Status{}, fail.New("MIGRATION_ARCHIVE_INVALID", "completion receipt names an archive that is not sealed")
	}
	if status.Receipt != nil && status.LegacyExists {
		return Status{}, fail.New("MIGRATION_SOURCE_NOT_REMOVED", "completion receipt exists while the legacy source remains present")
	}
	if journalExists && journal.MigrationID == migrationID && journal.Phase == phaseComplete && status.Receipt == nil {
		return Status{}, fail.New("MIGRATION_RECEIPT_INVALID", "complete migration journal has no exact receipt")
	}
	if status.Rollback != nil && status.Receipt != nil {
		return Status{}, fail.New("MIGRATION_STATUS_INVALID", "rollback marker and completion receipt cannot coexist")
	}
	if planExists && journalExists && journal.MigrationID == migrationID {
		if status.Receipt != nil && journal.Phase == phaseComplete {
			current, err := manifestCanonicalData(repo.CommonDir)
			if err != nil {
				return Status{}, fail.Wrap("MIGRATION_CANONICAL_MOVED", err)
			}
			// A completion receipt is historical cutover proof. Current writers
			// can change the two live data roots after the writer fence is gone.
			// Keep the static migration envelope exact, and report live-store
			// movement instead of treating it as an interrupted migration.
			if err := verifyCompletedCanonicalEnvelope(repo.CommonDir, plan, journal); err != nil {
				return Status{}, fail.Wrap("MIGRATION_CANONICAL_MOVED", err)
			}
			status.CurrentCanonicalManifestDigest = current.Digest
			status.CanonicalAdvanced = current.Digest != status.Receipt.CanonicalManifest
		} else if err := verifyCanonicalTransition(repo.CommonDir, plan, journal, phaseRequiresCanonicalPost(journal.Phase)); err != nil {
			return Status{}, fail.Wrap("MIGRATION_CANONICAL_MOVED", err)
		}
	}
	return status, nil
}

func exactCanonicalDomain(commonDir string) (bool, error) {
	rootExists, err := regularDirectoryState(filepath.Join(commonDir, "wip"))
	if err != nil || !rootExists {
		return false, err
	}
	marker := domainRecord{SchemaVersion: 2, Domain: DomainID, StateVersion: 1, MinimumReaderProtocol: contract.Protocol, RequiredCapabilities: contract.StateReaderCapabilities()}
	want, err := normalizedJSON(marker)
	if err != nil {
		return false, err
	}
	got, err := safeio.ReadRegular(filepath.Join(commonDir, "wip", "domain.json"), maxRecordBytes)
	if errors.Is(err, os.ErrNotExist) {
		return false, fail.New("MIGRATION_DESTINATION_INVALID", "wip root exists without its canonical domain marker")
	}
	if err != nil {
		return false, fail.Wrap("MIGRATION_DESTINATION_INVALID", err)
	}
	if !bytes.Equal(got, want) {
		return false, fail.New("COORDINATION_DOMAIN_CONFLICT", "wip root has a foreign or unsupported domain marker")
	}
	return true, nil
}

func regularDirectoryState(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s is not a regular directory", path)
	}
	return true, nil
}

func validateReceipt(receipt Receipt, migrationID string) error {
	if receipt.SchemaVersion != ReceiptSchemaVersion || receipt.MigrationID != migrationID || !digestPattern.MatchString(receipt.PlanDigest) || !digestPattern.MatchString(receipt.SourceManifest) || !digestPattern.MatchString(receipt.CanonicalManifest) || !digestPattern.MatchString(receipt.ReconciliationHistory) || receipt.ArchiveManifest != "" && !digestPattern.MatchString(receipt.ArchiveManifest) || !digestPattern.MatchString(receipt.ReceiptDigest) {
		return errors.New("receipt schema, identity, or digest is invalid")
	}
	want := receipt.ReceiptDigest
	receipt.ReceiptDigest = ""
	body, err := normalizedJSON(receipt)
	if err != nil {
		return err
	}
	if digest(body) != want {
		return errors.New("receipt digest does not match its content")
	}
	return nil
}
