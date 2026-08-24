package coordmigration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/nstranquist/wip-commit/internal/contract"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/safeio"
)

const (
	phaseJournalCreated    = "journal-created"
	phaseCanonicalPrepared = "canonical-prepared"
	phaseRecordsPublished  = "records-published"
	phaseSourceMoved       = "source-moved"
	phaseArchiveSealed     = "archive-sealed"
	phaseComplete          = "complete"
	phaseRollbackStarted   = "rollback-started"
	phaseRolledBack        = "rolled-back"
)

var phaseOrder = map[string]int{
	phaseJournalCreated:    0,
	phaseCanonicalPrepared: 1,
	phaseRecordsPublished:  2,
	phaseSourceMoved:       3,
	phaseArchiveSealed:     4,
	phaseComplete:          5,
	phaseRollbackStarted:   6,
	phaseRolledBack:        7,
}

// ApplyOptions contains only recovery and test-control settings. Apply always
// requires a separate exact reviewed plan digest.
type ApplyOptions struct {
	Resume     bool
	Rollback   bool
	LockWait   time.Duration
	AfterPhase func(string) error
	// AfterOperation is a package-level fault-injection seam used to prove
	// recovery between durable operations. The command never exposes it.
	AfterOperation func(string) error
	// Producer is recomputed by command dispatch from the running executable.
	// It must equal the identity embedded in the reviewed plan on every apply,
	// resume, and rollback invocation.
	Producer ProducerIdentity
}

type domainRecord struct {
	SchemaVersion         int      `json:"schema_version"`
	Domain                string   `json:"domain"`
	StateVersion          int      `json:"state_version"`
	MinimumReaderProtocol string   `json:"minimum_reader_protocol"`
	RequiredCapabilities  []string `json:"required_capabilities"`
}

func Apply(ctx context.Context, repo gitx.Repo, plan Plan, expectedDigest string, options ApplyOptions) (Result, error) {
	if err := validateApplyRequest(repo, plan, expectedDigest, options); err != nil {
		return Result{}, err
	}
	lock, err := filelock.Acquire(filepath.Join(repo.CommonDir, "wip-coordination.lock"), options.LockWait)
	if err != nil {
		return Result{}, fail.Wrap("MIGRATION_LOCKED", err)
	}
	defer func() { _ = lock.Release() }()

	journalPath := migrationJournalPath(repo.CommonDir)
	journal, journalExists, err := readJournal(journalPath)
	if err != nil {
		return Result{}, err
	}
	if journalExists && journal.Phase == phaseRolledBack {
		rolledBackPlan, loadErr := loadPersistedPlan(repo.CommonDir, journal.MigrationID)
		if loadErr != nil {
			return Result{}, fail.Wrap("MIGRATION_PLAN_INVALID", loadErr)
		}
		if bindingErr := verifyJournalPlanBinding(repo, rolledBackPlan, journal); bindingErr != nil {
			return Result{}, bindingErr
		}
		status, statusErr := InspectStatus(repo, journal.MigrationID)
		if statusErr != nil {
			return Result{}, statusErr
		}
		if status.State != "rolled-back" || status.Rollback == nil {
			return Result{}, fail.New("MIGRATION_ROLLBACK_INVALID", "rolled-back journal has no exact rollback marker")
		}
		if err := deactivateWriterFence(repo.CommonDir, rolledBackPlan); err != nil {
			return Result{}, err
		}
		if journal.MigrationID == plan.MigrationID && journal.PlanDigest == plan.PlanDigest {
			if options.Rollback {
				return Result{SchemaVersion: StatusSchemaVersion, Mode: "rollback", RolledBack: true, Plan: plan, Status: status}, nil
			}
			return Result{}, fail.New("MIGRATION_ROLLED_BACK", "this exact migration was rolled back; create and review a new preview")
		}
		if err := retireRolledBackJournal(repo.CommonDir, journal); err != nil {
			return Result{}, err
		}
		journal, journalExists = Journal{}, false
	}
	if journalExists && (journal.MigrationID != plan.MigrationID || journal.PlanDigest != plan.PlanDigest) {
		return Result{}, fail.New("MIGRATION_IN_PROGRESS", "a different coordination migration journal already exists")
	}
	if !journalExists {
		if options.Resume || options.Rollback {
			return Result{}, fail.New("MIGRATION_NOT_STARTED", "no migration journal exists for resume or rollback")
		}
		current, previewErr := Preview(ctx, repo, PreviewOptions{Producer: options.Producer})
		if previewErr != nil {
			return Result{}, previewErr
		}
		if current.PlanDigest != plan.PlanDigest {
			return Result{}, fail.Errorf("MIGRATION_PRESTATE_CHANGED", "current preview is %s, reviewed plan is %s", current.PlanDigest, plan.PlanDigest)
		}
		if err := verifyExactCanonicalPrestate(repo.CommonDir, plan); err != nil {
			return Result{}, fail.Wrap("MIGRATION_PRESTATE_CHANGED", err)
		}
	}
	if err := activateWriterFence(repo.CommonDir, plan); err != nil {
		return Result{}, err
	}
	if err := persistBootstrapPlan(repo.CommonDir, plan); err != nil {
		return Result{}, err
	}
	if journalExists {
		if journal.Phase == phaseComplete {
			status, statusErr := InspectStatus(repo, plan.MigrationID)
			if statusErr == nil {
				statusErr = deactivateWriterFence(repo.CommonDir, plan)
			}
			return Result{SchemaVersion: StatusSchemaVersion, Mode: mutationMode(options), Applied: true, Resumed: options.Resume, Plan: plan, Status: status, Receipt: status.Receipt}, statusErr
		}
		if !options.Resume {
			if options.Rollback {
				// The exact plan, journal, and canonical prefix are verified below
				// before rollback removes any published migration output.
			} else {
				return Result{}, fail.New("MIGRATION_RESUME_REQUIRED", "an incomplete migration journal exists; use the exact plan and --resume or --rollback")
			}
		}
	} else {
		journal = Journal{
			SchemaVersion: JournalSchemaVersion, MigrationID: plan.MigrationID, PlanDigest: plan.PlanDigest,
			ExpectedCanonicalDigest: plan.ExpectedCanonicalData.Digest, PlannedCanonicalDigest: plan.PlannedCanonicalData.Digest,
			PublishedPaths: []string{}, Phase: phaseJournalCreated, UpdatedAt: time.Now().UTC(),
		}
		if err := writeJSON(journalPath, journal, 0o600, false); err != nil {
			return Result{}, fail.Wrap("MIGRATION_JOURNAL_FAILED", err)
		}
		if err := afterPhase(journalPath, &journal, options, phaseJournalCreated); err != nil {
			return Result{}, err
		}
	}
	if err := verifyJournalPlanBinding(repo, plan, journal); err != nil {
		return Result{}, recordJournalError(journalPath, &journal, err)
	}
	if err := verifyExistingMigrationArtifacts(repo, plan); err != nil {
		return Result{}, recordJournalError(journalPath, &journal, err)
	}
	if err := verifyCanonicalTransition(repo.CommonDir, plan, journal, phaseRequiresCanonicalPost(journal.Phase)); err != nil {
		return Result{}, recordJournalError(journalPath, &journal, fail.Wrap("MIGRATION_CANONICAL_MOVED", err))
	}
	if journal.Phase == phaseRollbackStarted && !options.Rollback {
		return Result{}, fail.New("MIGRATION_ROLLBACK_REQUIRED", "rollback has started; retry rollback with the exact reviewed plan")
	}
	if options.Rollback {
		return rollback(ctx, repo, plan, journal, options)
	}

	if phaseBefore(journal.Phase, phaseCanonicalPrepared) {
		if err := verifySourcePrestate(repo, plan, false); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		if err := prepareCanonical(repo, plan, func(path string) error {
			return authorizePublishedPath(journalPath, &journal, path)
		}); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, fail.Wrap("MIGRATION_PREPARE_FAILED", err))
		}
		if err := advanceJournal(journalPath, &journal, phaseCanonicalPrepared); err != nil {
			return Result{}, err
		}
		if err := afterPhase(journalPath, &journal, options, phaseCanonicalPrepared); err != nil {
			return Result{}, err
		}
	}
	if phaseBefore(journal.Phase, phaseRecordsPublished) {
		if err := verifySourcePrestate(repo, plan, false); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		if err := verifyExpectedRefs(ctx, repo, plan.ExpectedRefs); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		if err := publishRecords(repo, plan, false, func(path string) error {
			return authorizePublishedPath(journalPath, &journal, path)
		}, func(operation string) error {
			return afterOperation(journalPath, &journal, options, operation)
		}); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		if err := verifyPublished(repo, plan); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		if err := verifyCanonicalTransition(repo.CommonDir, plan, journal, true); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, fail.Wrap("MIGRATION_CANONICAL_MOVED", err))
		}
		if err := advanceJournal(journalPath, &journal, phaseRecordsPublished); err != nil {
			return Result{}, err
		}
		if err := afterPhase(journalPath, &journal, options, phaseRecordsPublished); err != nil {
			return Result{}, err
		}
	}
	if phaseBefore(journal.Phase, phaseSourceMoved) {
		moved, err := sourceAlreadyMoved(repo, plan)
		if err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		if !moved {
			if err := verifySourcePrestate(repo, plan, false); err != nil {
				return Result{}, recordJournalError(journalPath, &journal, err)
			}
			if err := verifyPublished(repo, plan); err != nil {
				return Result{}, recordJournalError(journalPath, &journal, err)
			}
			if err := moveLegacySource(repo, plan, func(path string) error {
				return authorizePublishedPath(journalPath, &journal, path)
			}); err != nil {
				return Result{}, recordJournalError(journalPath, &journal, err)
			}
			if err := afterOperation(journalPath, &journal, options, "source-renamed"); err != nil {
				return Result{}, err
			}
		}
		if err := advanceJournal(journalPath, &journal, phaseSourceMoved); err != nil {
			return Result{}, err
		}
		if err := afterPhase(journalPath, &journal, options, phaseSourceMoved); err != nil {
			return Result{}, err
		}
	}
	if phaseBefore(journal.Phase, phaseArchiveSealed) {
		if err := verifyArchivedSource(repo, plan); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		if err := sealLegacyArchive(repo, plan, func(path string) error {
			return authorizePublishedPath(journalPath, &journal, path)
		}); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		if err := afterOperation(journalPath, &journal, options, "archive-seal-written"); err != nil {
			return Result{}, err
		}
		if err := advanceJournal(journalPath, &journal, phaseArchiveSealed); err != nil {
			return Result{}, err
		}
		if err := afterPhase(journalPath, &journal, options, phaseArchiveSealed); err != nil {
			return Result{}, err
		}
	}
	if phaseBefore(journal.Phase, phaseComplete) {
		if err := verifyExpectedRefs(ctx, repo, plan.ExpectedRefs); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		if err := verifyPublished(repo, plan); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		if err := verifyCanonicalTransition(repo.CommonDir, plan, journal, true); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, fail.Wrap("MIGRATION_CANONICAL_MOVED", err))
		}
		if err := verifyArchivedSource(repo, plan); err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		receiptPath := migrationReceiptRelativePath(plan.MigrationID)
		if err := authorizePublishedPath(journalPath, &journal, receiptPath); err != nil {
			return Result{}, err
		}
		receipt, err := completeReceipt(repo, plan)
		if err != nil {
			return Result{}, recordJournalError(journalPath, &journal, err)
		}
		if err := afterOperation(journalPath, &journal, options, "receipt-written"); err != nil {
			return Result{}, err
		}
		if err := advanceJournal(journalPath, &journal, phaseComplete); err != nil {
			return Result{}, err
		}
		if err := afterPhase(journalPath, &journal, options, phaseComplete); err != nil {
			return Result{}, err
		}
		status, statusErr := InspectStatus(repo, plan.MigrationID)
		if statusErr == nil {
			statusErr = deactivateWriterFence(repo.CommonDir, plan)
		}
		return Result{SchemaVersion: StatusSchemaVersion, Mode: mutationMode(options), Applied: true, Resumed: options.Resume, Plan: plan, Status: status, Receipt: &receipt}, statusErr
	}
	return Result{}, fail.New("MIGRATION_JOURNAL_INVALID", "migration journal has an unsupported phase")
}

func verifyExistingMigrationArtifacts(repo gitx.Repo, plan Plan) error {
	if plan.SourceLegacy.Exists {
		moved, err := sourceAlreadyMoved(repo, plan)
		if err != nil {
			return err
		}
		if moved {
			if err := verifyArchivedSource(repo, plan); err != nil {
				return err
			}
		}
	}
	checks := []struct {
		path string
		code string
		run  func() error
	}{
		{filepath.Join(archiveMigrationRoot(repo.CommonDir, plan.MigrationID), "seal.json"), "MIGRATION_ARCHIVE_INVALID", func() error { return validateTransitionSeal(repo.CommonDir, plan) }},
		{migrationReceiptPath(repo.CommonDir, plan.MigrationID), "MIGRATION_RECEIPT_INVALID", func() error { return validateTransitionReceipt(repo.CommonDir, plan) }},
		{filepath.Join(repo.CommonDir, "wip", "migration", "v1", plan.MigrationID, "rollback.json"), "MIGRATION_ROLLBACK_INVALID", func() error { return validateTransitionRollback(repo.CommonDir, plan) }},
	}
	for _, check := range checks {
		if _, err := os.Lstat(check.path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fail.Wrap(check.code, err)
		}
		if err := check.run(); err != nil {
			return fail.Wrap(check.code, err)
		}
	}
	return nil
}

func mutationMode(options ApplyOptions) string {
	if options.Resume {
		return "resume"
	}
	if options.Rollback {
		return "rollback"
	}
	return "apply"
}

func validateApplyRequest(repo gitx.Repo, plan Plan, expectedDigest string, options ApplyOptions) error {
	if plan.SchemaVersion != PlanSchemaVersion || plan.State != "ready" || len(plan.Conflicts) != 0 {
		return fail.New("MIGRATION_PLAN_BLOCKED", "only a ready, conflict-free migration plan can be applied")
	}
	if filepath.Clean(plan.RepositoryCommonDir) != filepath.Clean(repo.CommonDir) {
		return fail.New("MIGRATION_REPOSITORY_MISMATCH", "the plan belongs to a different Git common directory")
	}
	if err := plan.ValidateDigest(expectedDigest); err != nil {
		return fail.Wrap("MIGRATION_PLAN_DIGEST_MISMATCH", err)
	}
	if err := options.Producer.Validate(); err != nil {
		return fail.Wrap("MIGRATION_PRODUCER_INVALID", err)
	}
	if options.Producer != plan.Producer {
		return fail.New("MIGRATION_PRODUCER_MISMATCH", "the running executable, source, protocol, or portable bundle differs from the reviewed plan producer")
	}
	if !digestPattern.MatchString(plan.ExpectedCanonicalData.Digest) || !digestPattern.MatchString(plan.PlannedCanonicalData.Digest) {
		return fail.New("MIGRATION_PLAN_BLOCKED", "migration plan has invalid canonical manifest bindings")
	}
	wantPlanned, err := planCanonicalData(plan.ExpectedCanonicalData, plan)
	if err != nil || wantPlanned.Digest != plan.PlannedCanonicalData.Digest {
		return fail.New("MIGRATION_PLAN_BLOCKED", "migration plan post-publication manifest is invalid")
	}
	if options.Resume && options.Rollback {
		return fail.New("INVALID_ARGUMENT", "resume and rollback are mutually exclusive")
	}
	if options.LockWait < 0 || options.LockWait > filelock.MaxWait {
		return fail.New("INVALID_ARGUMENT", "migration lock wait is outside the supported range")
	}
	return nil
}

func migrationJournalPath(commonDir string) string {
	return filepath.Join(commonDir, "wip-coordination-migration-v1.journal.json")
}

func readJournal(path string) (Journal, bool, error) {
	var journal Journal
	_, err := readJSONFileLimit(path, &journal, MaxPlanBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Journal{}, false, nil
	}
	if err != nil {
		return Journal{}, false, fail.Wrap("MIGRATION_JOURNAL_INVALID", err)
	}
	if journal.SchemaVersion != JournalSchemaVersion || phaseOrder[journal.Phase] < 0 || !migrationIDPattern.MatchString(journal.MigrationID) || !digestPattern.MatchString(journal.PlanDigest) || !digestPattern.MatchString(journal.ExpectedCanonicalDigest) || !digestPattern.MatchString(journal.PlannedCanonicalDigest) {
		return Journal{}, false, fail.New("MIGRATION_JOURNAL_INVALID", "migration journal schema or phase is invalid")
	}
	if _, ok := phaseOrder[journal.Phase]; !ok {
		return Journal{}, false, fail.New("MIGRATION_JOURNAL_INVALID", "migration journal phase is invalid")
	}
	seen := map[string]bool{}
	for _, path := range journal.PublishedPaths {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		if clean != path || filepath.IsAbs(path) || path == "." || path == ".." || lenPrefix(path, "../") || seen[path] {
			return Journal{}, false, fail.New("MIGRATION_JOURNAL_INVALID", "migration journal contains an invalid or duplicate published path")
		}
		seen[path] = true
	}
	return journal, true, nil
}

func authorizePublishedPath(path string, journal *Journal, published string) error {
	published = filepath.ToSlash(filepath.Clean(filepath.FromSlash(published)))
	for _, existing := range journal.PublishedPaths {
		if existing == published {
			return nil
		}
	}
	journal.PublishedPaths = append(journal.PublishedPaths, published)
	journal.UpdatedAt = time.Now().UTC()
	if err := writeJSON(path, journal, 0o600, true); err != nil {
		return fail.Wrap("MIGRATION_JOURNAL_FAILED", err)
	}
	return nil
}

func phaseBefore(current, target string) bool { return phaseOrder[current] < phaseOrder[target] }

func phaseRequiresCanonicalPost(phase string) bool {
	return !phaseBefore(phase, phaseRecordsPublished) && phase != phaseRollbackStarted && phase != phaseRolledBack
}

func advanceJournal(path string, journal *Journal, phase string) error {
	journal.Phase = phase
	journal.UpdatedAt = time.Now().UTC()
	journal.LastError = ""
	if err := writeJSON(path, journal, 0o600, true); err != nil {
		return fail.Wrap("MIGRATION_JOURNAL_FAILED", err)
	}
	return nil
}

func recordJournalError(path string, journal *Journal, cause error) error {
	journal.UpdatedAt = time.Now().UTC()
	journal.LastError = cause.Error()
	if err := writeJSON(path, journal, 0o600, true); err != nil {
		return fail.Wrap("MIGRATION_JOURNAL_FAILED", errors.Join(cause, err))
	}
	return cause
}

func afterPhase(path string, journal *Journal, options ApplyOptions, phase string) error {
	if options.AfterPhase == nil {
		return nil
	}
	if err := options.AfterPhase(phase); err != nil {
		return recordJournalError(path, journal, fail.Wrap("MIGRATION_INTERRUPTED", err))
	}
	return nil
}

func afterOperation(path string, journal *Journal, options ApplyOptions, operation string) error {
	if options.AfterOperation == nil {
		return nil
	}
	if err := options.AfterOperation(operation); err != nil {
		return recordJournalError(path, journal, fail.Wrap("MIGRATION_INTERRUPTED", err))
	}
	return nil
}

func prepareCanonical(repo gitx.Repo, plan Plan, authorize func(string) error) error {
	paths := []string{
		"wip", "wip/v1", "wip/v1/lanes", "wip/v1/leases", "wip/v1/locks", "wip/v1/intents", "wip/v1/profiles", "wip/v1/init-intents", "wip/v1/archive",
		"wip/review", "wip/review/v1", "wip/review/v1/locks", "wip/review/v1/handoffs", "wip/review/v1/landing-intents", "wip/review/v1/archive", "wip/review/v1/plans", "wip/review/v1/reconciliation",
		"wip/archive", "wip/archive/legacy-ndev-wip", "wip/migration", "wip/migration/v1", filepath.ToSlash(filepath.Join("wip", "migration", "v1", plan.MigrationID)),
	}
	for _, path := range paths {
		if authorize != nil {
			if err := authorize(path); err != nil {
				return err
			}
		}
		if err := ensureDirectoryPath(repo.CommonDir, path, 0o700); err != nil {
			return err
		}
	}
	if !manifestContainsPath(plan.ExpectedCanonicalData.Capture, captureRegistryLock) {
		lockRelative := "wip/v1/" + captureRegistryLock
		if authorize != nil {
			if err := authorize(lockRelative); err != nil {
				return err
			}
		}
		if err := createConfinedExact(repo.CommonDir, lockRelative, nil, 0o600); err != nil {
			return err
		}
	}
	marker := domainRecord{SchemaVersion: 2, Domain: DomainID, StateVersion: 1, MinimumReaderProtocol: contract.Protocol, RequiredCapabilities: contract.StateReaderCapabilities()}
	markerBody, err := normalizedJSON(marker)
	if err != nil {
		return err
	}
	markerPath := filepath.Join(repo.CommonDir, "wip", "domain.json")
	if plan.ExpectedCanonical.Exists {
		existing, readErr := safeio.ReadRegular(markerPath, maxRecordBytes)
		if readErr != nil || !bytes.Equal(existing, markerBody) {
			return fmt.Errorf("canonical domain marker changed after preview")
		}
	} else {
		if authorize != nil {
			if err := authorize("wip/domain.json"); err != nil {
				return err
			}
		}
		if err := createConfinedExact(repo.CommonDir, "wip/domain.json", markerBody, 0o600); err != nil {
			return err
		}
	}
	planBody, err := normalizedJSON(plan)
	if err != nil {
		return err
	}
	planRelative := filepath.ToSlash(filepath.Join("wip", "migration", "v1", plan.MigrationID, "plan.json"))
	if authorize != nil {
		if err := authorize(planRelative); err != nil {
			return err
		}
	}
	return createConfinedExact(repo.CommonDir, planRelative, planBody, 0o400)
}

func manifestContainsPath(manifest TreeManifest, path string) bool {
	for _, entry := range manifest.Entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func ensureDirectoryPath(commonDir, relative string, mode os.FileMode) error {
	clean := filepath.Clean(filepath.FromSlash(relative))
	current := commonDir
	for _, component := range splitPath(clean) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, mode); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			if err != nil {
				return err
			}
			return fmt.Errorf("state directory is unsafe: %s", current)
		}
	}
	return nil
}

func splitPath(path string) []string {
	var parts []string
	for path != "." && path != string(filepath.Separator) && path != "" {
		directory, base := filepath.Split(path)
		parts = append(parts, base)
		path = filepath.Clean(directory)
	}
	slices.Reverse(parts)
	return parts
}

func migrationPlanPath(commonDir, migrationID string) string {
	return filepath.Join(commonDir, "wip", "migration", "v1", migrationID, "plan.json")
}

func migrationReceiptPath(commonDir, migrationID string) string {
	return filepath.Join(commonDir, "wip", "migration", "v1", migrationID, "receipt.json")
}

func archiveMigrationRoot(commonDir, migrationID string) string {
	return filepath.Join(commonDir, "wip", "archive", "legacy-ndev-wip", migrationID)
}

func publishRecords(repo gitx.Repo, plan Plan, archived bool, authorize func(string) error, onPublished func(string) error) error {
	legacyRoot := filepath.Join(repo.CommonDir, "ndev-wip")
	if archived {
		legacyRoot = filepath.Join(archiveMigrationRoot(repo.CommonDir, plan.MigrationID), "source")
	}
	for _, record := range append(slices.Clone(plan.LaneImports), plan.LeaseImports...) {
		if record.Action == "match" || record.Action == "archive-only" {
			continue
		}
		if record.Action != "import" {
			return fmt.Errorf("unsupported import action %s", record.Action)
		}
		var value any
		source := filepath.Join(legacyRoot, filepath.FromSlash(stringsTrimLegacy(record.Source)))
		if record.Kind == "lane" {
			value = &legacyLane{}
		} else {
			value = &legacyLease{}
		}
		if _, err := readJSONFile(source, value); err != nil {
			return err
		}
		body, err := normalizedJSON(value)
		if err != nil || digest(body) != record.SourceSHA256 {
			return fmt.Errorf("normalized source changed for %s %s", record.Kind, record.ID)
		}
		if authorize != nil {
			if err := ensureAuthorizedParents(repo.CommonDir, record.Destination, authorize); err != nil {
				return err
			}
			if err := authorize(record.Destination); err != nil {
				return err
			}
		}
		if err := createConfinedExact(repo.CommonDir, record.Destination, body, 0o600); err != nil {
			return err
		}
		if onPublished != nil {
			if err := onPublished("record-published:" + record.Destination); err != nil {
				return err
			}
		}
	}
	for _, record := range append(slices.Clone(plan.ReviewCopies), plan.ReconciliationCopies...) {
		if record.Action == "match" {
			continue
		}
		mode, err := parseMode(record.Mode)
		if err != nil {
			return err
		}
		source := filepath.Join(repo.CommonDir, filepath.FromSlash(record.Source))
		if archived && record.Kind == "legacy-review" {
			source = filepath.Join(legacyRoot, filepath.FromSlash(stringsTrimLegacy(record.Source)))
		}
		if authorize != nil {
			if err := ensureAuthorizedParents(repo.CommonDir, record.Destination, authorize); err != nil {
				return err
			}
			if err := authorize(record.Destination); err != nil {
				return err
			}
		}
		if err := copyConfinedExact(source, repo.CommonDir, record.Destination, record.SourceSHA256, mode); err != nil {
			return err
		}
		if onPublished != nil {
			if err := onPublished("record-published:" + record.Destination); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureAuthorizedParents(commonDir, destination string, authorize func(string) error) error {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(destination)))
	parent := filepath.ToSlash(filepath.Dir(clean))
	var parents []string
	for parent != "." && parent != "" {
		parents = append(parents, parent)
		next := filepath.ToSlash(filepath.Dir(parent))
		if next == parent {
			break
		}
		parent = next
	}
	slices.Reverse(parents)
	for _, path := range parents {
		if err := authorize(path); err != nil {
			return err
		}
		if err := ensureDirectoryPath(commonDir, path, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func stringsTrimLegacy(path string) string {
	prefix := "ndev-wip/"
	if len(path) >= len(prefix) && filepath.ToSlash(path[:len(prefix)]) == prefix {
		return path[len(prefix):]
	}
	return path
}

func verifyPublished(repo gitx.Repo, plan Plan) error {
	for _, record := range append(slices.Clone(plan.LaneImports), plan.LeaseImports...) {
		if record.Action == "archive-only" {
			continue
		}
		body, err := safeio.ReadRegular(filepath.Join(repo.CommonDir, filepath.FromSlash(record.Destination)), maxRecordBytes)
		if err != nil || digest(body) != record.SourceSHA256 {
			return fmt.Errorf("published %s %s does not match the reviewed normalized record", record.Kind, record.ID)
		}
	}
	for _, record := range append(slices.Clone(plan.ReviewCopies), plan.ReconciliationCopies...) {
		path := filepath.Join(repo.CommonDir, filepath.FromSlash(record.Destination))
		body, err := safeio.ReadRegular(path, maxManifestBytes)
		if err != nil || digest(body) != record.SourceSHA256 {
			return fmt.Errorf("published review record does not match %s", record.Destination)
		}
		info, err := os.Lstat(path)
		if err != nil || modeString(info.Mode().Perm()) != record.Mode {
			return fmt.Errorf("published review mode does not match %s", record.Destination)
		}
	}
	return nil
}

func verifyExpectedRefs(ctx context.Context, repo gitx.Repo, refs []RefExpectation) error {
	for _, expected := range refs {
		actual, err := repo.Text(ctx, nil, "rev-parse", "--verify", expected.Ref+"^{commit}")
		if err != nil || actual != expected.SHA {
			return fail.Errorf("MIGRATION_REF_MOVED", "ref %s no longer equals reviewed SHA %s", expected.Ref, expected.SHA)
		}
	}
	return nil
}

func verifySourcePrestate(repo gitx.Repo, plan Plan, archived bool) error {
	logical := "ndev-wip"
	if archived {
		logical = filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID, "source"))
	}
	current, err := manifestTree(repo.CommonDir, logical)
	if err != nil {
		return fail.Wrap("MIGRATION_SOURCE_UNSAFE", err)
	}
	if !archived && current.Digest != plan.SourceLegacy.Digest {
		return fail.New("MIGRATION_SOURCE_MOVED", "legacy source changed after the reviewed preview")
	}
	if archived {
		return compareArchivedSource(plan.SourceLegacy, current)
	}
	return nil
}

func verifyArchivedSource(repo gitx.Repo, plan Plan) error {
	if !plan.SourceLegacy.Exists {
		return nil
	}
	current, err := manifestTree(repo.CommonDir, filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID, "source")))
	if err != nil {
		return fail.Wrap("MIGRATION_ARCHIVE_UNSAFE", err)
	}
	return compareArchivedSource(plan.SourceLegacy, current)
}

// Sealing intentionally changes permissions. Compare the exact topology and
// file bytes while allowing only that reviewed permission transformation.
func compareArchivedSource(expected, current TreeManifest) error {
	if !current.Exists && expected.Exists {
		return fail.New("MIGRATION_ARCHIVE_MISSING", "moved legacy source is absent from the expected archive")
	}
	if expected.Exists != current.Exists || len(expected.Entries) != len(current.Entries) {
		return fail.New("MIGRATION_ARCHIVE_MOVED", "moved legacy archive topology differs from the reviewed source")
	}
	want := sortedEntries(expected.Entries)
	got := sortedEntries(current.Entries)
	for index := range want {
		if want[index].Path != got[index].Path || want[index].Type != got[index].Type || want[index].Size != got[index].Size || want[index].SHA256 != got[index].SHA256 {
			return fail.New("MIGRATION_ARCHIVE_MOVED", "moved legacy archive paths or bytes differ from the reviewed source")
		}
		if got[index].Type == "file" && got[index].Mode != want[index].Mode && got[index].Mode != "0400" {
			return fail.New("MIGRATION_ARCHIVE_MOVED", "moved legacy archive has an unexpected file mode")
		}
		if got[index].Type == "directory" && got[index].Mode != want[index].Mode && got[index].Mode != "0500" {
			return fail.New("MIGRATION_ARCHIVE_MOVED", "moved legacy archive has an unexpected directory mode")
		}
	}
	return nil
}

func moveLegacySource(repo gitx.Repo, plan Plan, authorize func(string) error) error {
	if !plan.SourceLegacy.Exists {
		return nil
	}
	archiveRoot := archiveMigrationRoot(repo.CommonDir, plan.MigrationID)
	archiveRelative := filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID))
	if err := ensureAuthorizedParents(repo.CommonDir, filepath.ToSlash(filepath.Join(archiveRelative, "source")), authorize); err != nil {
		return err
	}
	for _, entry := range plan.SourceLegacy.Entries {
		path := filepath.ToSlash(filepath.Join(archiveRelative, "source"))
		if entry.Path != "." {
			path = filepath.ToSlash(filepath.Join(path, filepath.FromSlash(entry.Path)))
		}
		if err := authorize(path); err != nil {
			return err
		}
	}
	destination := filepath.Join(archiveRoot, "source")
	if _, err := os.Lstat(destination); err == nil {
		return fail.New("MIGRATION_ARCHIVE_CONFLICT", "archive source already exists before the legacy move")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(filepath.Join(repo.CommonDir, "ndev-wip"), destination); err != nil {
		return fail.Wrap("MIGRATION_SOURCE_MOVE_FAILED", err)
	}
	if err := syncDirectory(repo.CommonDir); err != nil {
		return err
	}
	return syncDirectory(archiveRoot)
}

func sourceAlreadyMoved(repo gitx.Repo, plan Plan) (bool, error) {
	if !plan.SourceLegacy.Exists {
		return true, nil
	}
	sourceInfo, sourceErr := os.Lstat(filepath.Join(repo.CommonDir, "ndev-wip"))
	archiveInfo, archiveErr := os.Lstat(filepath.Join(archiveMigrationRoot(repo.CommonDir, plan.MigrationID), "source"))
	sourceExists := sourceErr == nil && sourceInfo.IsDir() && sourceInfo.Mode()&os.ModeSymlink == 0
	archiveExists := archiveErr == nil && archiveInfo.IsDir() && archiveInfo.Mode()&os.ModeSymlink == 0
	if sourceExists && !archiveExists {
		return false, nil
	}
	if !sourceExists && archiveExists {
		return true, nil
	}
	if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
		return false, sourceErr
	}
	if archiveErr != nil && !errors.Is(archiveErr, os.ErrNotExist) {
		return false, archiveErr
	}
	return false, fail.New("MIGRATION_SOURCE_STATE_INVALID", "legacy source and archive source are both present or both absent")
}

func sealLegacyArchive(repo gitx.Repo, plan Plan, authorize func(string) error) error {
	if !plan.SourceLegacy.Exists {
		return nil
	}
	archiveRoot := archiveMigrationRoot(repo.CommonDir, plan.MigrationID)
	sourceRoot := filepath.Join(archiveRoot, "source")
	sealRelative := filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID, "seal.json"))
	if err := authorize(sealRelative); err != nil {
		return err
	}
	if err := verifyArchivedSource(repo, plan); err != nil {
		return err
	}
	if err := sealArchiveTree(sourceRoot); err != nil {
		return fail.Wrap("MIGRATION_SEAL_FAILED", err)
	}
	if err := verifyArchivedSource(repo, plan); err != nil {
		return err
	}
	archiveManifest, err := manifestTree(repo.CommonDir, filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID, "source")))
	if err != nil {
		return err
	}
	seal := Seal{SchemaVersion: SealSchemaVersion, MigrationID: plan.MigrationID, PlanDigest: plan.PlanDigest, SourceManifest: plan.SourceLegacy.Digest, ArchiveManifest: archiveManifest.Digest, ArchiveRelativeDir: filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID, "source")), SealedAt: time.Now().UTC()}
	if err := writeJSON(filepath.Join(archiveRoot, "seal.json"), seal, 0o400, false); err != nil {
		if !isExactSeal(filepath.Join(archiveRoot, "seal.json"), seal) {
			return err
		}
	}
	return syncDirectory(archiveRoot)
}

func isExactSeal(path string, expected Seal) bool {
	var actual Seal
	if _, err := readJSONFile(path, &actual); err != nil {
		return false
	}
	return actual.SchemaVersion == expected.SchemaVersion && actual.MigrationID == expected.MigrationID && actual.PlanDigest == expected.PlanDigest && actual.SourceManifest == expected.SourceManifest && actual.ArchiveManifest == expected.ArchiveManifest && actual.ArchiveRelativeDir == expected.ArchiveRelativeDir
}

func completeReceipt(repo gitx.Repo, plan Plan) (Receipt, error) {
	path := migrationReceiptPath(repo.CommonDir, plan.MigrationID)
	var existing Receipt
	if _, err := readJSONFile(path, &existing); err == nil {
		if validateErr := validateReceipt(existing, plan.MigrationID); validateErr != nil || existing.PlanDigest != plan.PlanDigest || existing.SourceManifest != plan.SourceLegacy.Digest || existing.ReconciliationHistory != plan.ReconciliationHistory.Digest {
			if validateErr != nil {
				return Receipt{}, validateErr
			}
			return Receipt{}, errors.New("existing migration receipt does not match the reviewed plan")
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Receipt{}, err
	}
	if plan.SourceLegacy.Exists {
		if _, err := os.Lstat(filepath.Join(repo.CommonDir, "ndev-wip")); !errors.Is(err, os.ErrNotExist) {
			return Receipt{}, fail.New("MIGRATION_SOURCE_NOT_REMOVED", "legacy source remains present after archive publication")
		}
		if err := verifyArchivedSource(repo, plan); err != nil {
			return Receipt{}, err
		}
	}
	canonical, err := manifestCanonicalData(repo.CommonDir)
	if err != nil {
		return Receipt{}, err
	}
	if canonical.Digest != plan.PlannedCanonicalData.Digest {
		return Receipt{}, fail.New("MIGRATION_CANONICAL_MOVED", "canonical data does not match the reviewed post-publication manifest")
	}
	canonicalDigest := canonical.Digest
	archiveDigest := ""
	if plan.SourceLegacy.Exists {
		archive, manifestErr := manifestTree(repo.CommonDir, filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID, "source")))
		if manifestErr != nil {
			return Receipt{}, manifestErr
		}
		archiveDigest = archive.Digest
	}
	receipt := Receipt{SchemaVersion: ReceiptSchemaVersion, MigrationID: plan.MigrationID, PlanDigest: plan.PlanDigest, SourceManifest: plan.SourceLegacy.Digest, CanonicalManifest: canonicalDigest, ArchiveManifest: archiveDigest, ReconciliationHistory: plan.ReconciliationHistory.Digest, CompletedAt: time.Now().UTC()}
	body, err := normalizedJSON(receipt)
	if err != nil {
		return Receipt{}, err
	}
	receipt.ReceiptDigest = digest(body)
	if err := writeJSON(path, receipt, 0o400, false); err != nil {
		var existing Receipt
		if _, readErr := readJSONFile(path, &existing); readErr != nil || existing.PlanDigest != receipt.PlanDigest || existing.CanonicalManifest != receipt.CanonicalManifest || existing.ArchiveManifest != receipt.ArchiveManifest || existing.ReceiptDigest != receipt.ReceiptDigest {
			return Receipt{}, err
		}
		return existing, nil
	}
	return receipt, nil
}

func rollback(ctx context.Context, repo gitx.Repo, plan Plan, journal Journal, options ApplyOptions) (Result, error) {
	moved, err := sourceAlreadyMoved(repo, plan)
	if err != nil && plan.SourceLegacy.Exists {
		return Result{}, err
	}
	if moved && plan.SourceLegacy.Exists {
		return Result{}, fail.New("MIGRATION_ROLLBACK_REFUSED", "legacy source has moved; resume forward to preserve the verified archive")
	}
	if err := verifyExpectedRefs(ctx, repo, plan.ExpectedRefs); err != nil {
		return Result{}, err
	}
	journalPath := migrationJournalPath(repo.CommonDir)
	if journal.Phase != phaseRollbackStarted {
		if err := advanceJournal(journalPath, &journal, phaseRollbackStarted); err != nil {
			return Result{}, err
		}
		if err := afterPhase(journalPath, &journal, options, phaseRollbackStarted); err != nil {
			return Result{}, err
		}
	}
	for _, record := range append(slices.Clone(plan.ReconciliationCopies), plan.ReviewCopies...) {
		if record.Action == "copy" {
			if err := removeExactFile(filepath.Join(repo.CommonDir, filepath.FromSlash(record.Destination)), record.SourceSHA256); err != nil {
				return Result{}, fail.Wrap("MIGRATION_ROLLBACK_FAILED", err)
			}
			if err := afterOperation(journalPath, &journal, options, "rollback-removed:"+record.Destination); err != nil {
				return Result{}, err
			}
		}
	}
	for _, record := range append(slices.Clone(plan.LeaseImports), plan.LaneImports...) {
		if record.Action == "import" {
			if err := removeExactFile(filepath.Join(repo.CommonDir, filepath.FromSlash(record.Destination)), record.SourceSHA256); err != nil {
				return Result{}, fail.Wrap("MIGRATION_ROLLBACK_FAILED", err)
			}
			if err := afterOperation(journalPath, &journal, options, "rollback-removed:"+record.Destination); err != nil {
				return Result{}, err
			}
		}
	}
	marker := RollbackMarker{SchemaVersion: RollbackSchemaVersion, MigrationID: plan.MigrationID, PlanDigest: plan.PlanDigest, State: "rolled-back", Reason: "operator requested rollback before the legacy source move", UpdatedAt: time.Now().UTC()}
	markerPath := filepath.Join(repo.CommonDir, "wip", "migration", "v1", plan.MigrationID, "rollback.json")
	markerRelative := filepath.ToSlash(filepath.Join("wip", "migration", "v1", plan.MigrationID, "rollback.json"))
	if err := authorizePublishedPath(journalPath, &journal, markerRelative); err != nil {
		return Result{}, err
	}
	if err := writeJSON(markerPath, marker, 0o400, true); err != nil {
		return Result{}, fail.Wrap("MIGRATION_ROLLBACK_FAILED", err)
	}
	if err := advanceJournal(journalPath, &journal, phaseRolledBack); err != nil {
		return Result{}, err
	}
	status, statusErr := InspectStatus(repo, plan.MigrationID)
	if statusErr == nil {
		statusErr = deactivateWriterFence(repo.CommonDir, plan)
	}
	return Result{SchemaVersion: StatusSchemaVersion, Mode: "rollback", RolledBack: true, Plan: plan, Status: status}, statusErr
}

func sortedEntries(entries []ManifestEntry) []ManifestEntry {
	result := slices.Clone(entries)
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}
