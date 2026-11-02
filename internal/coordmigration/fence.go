package coordmigration

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/safeio"
)

const writerFenceSchemaVersion = "wip.coordination-migration-writer-fence/1.0.0"

func writerFencePath(commonDir string) string {
	return filepath.Join(commonDir, "wip-coordination-migration-v1.fence.json")
}

func bootstrapPlanPath(commonDir, migrationID string) string {
	return filepath.Join(commonDir, "wip-coordination-migration-v1."+migrationID+".plan.json")
}

func retiredJournalPath(commonDir, migrationID string) string {
	return filepath.Join(commonDir, "wip-coordination-migration-v1."+migrationID+".rolled-back.journal.json")
}

// retireRolledBackJournal keeps the exact terminal journal as immutable
// history, then frees the single active-journal slot for a newly reviewed
// migration. The caller holds the repository-wide coordination lock.
func retireRolledBackJournal(commonDir string, journal Journal) error {
	if journal.Phase != phaseRolledBack || !migrationIDPattern.MatchString(journal.MigrationID) {
		return fail.New("MIGRATION_JOURNAL_INVALID", "only an exact rolled-back journal can be retired")
	}
	path := migrationJournalPath(commonDir)
	body, err := safeio.ReadRegular(path, MaxPlanBytes)
	if err != nil {
		return fail.Wrap("MIGRATION_JOURNAL_INVALID", err)
	}
	current, exists, err := readJournal(path)
	if err != nil || !exists {
		if err != nil {
			return err
		}
		return fail.New("MIGRATION_JOURNAL_INVALID", "rolled-back journal disappeared before retirement")
	}
	currentBody, err := normalizedJSON(current)
	if err != nil || !bytes.Equal(body, currentBody) || current.MigrationID != journal.MigrationID || current.PlanDigest != journal.PlanDigest || current.Phase != journal.Phase {
		return fail.New("MIGRATION_JOURNAL_INVALID", "rolled-back journal changed before retirement")
	}
	if err := createExact(retiredJournalPath(commonDir, journal.MigrationID), body, 0o400); err != nil {
		return fail.Wrap("MIGRATION_JOURNAL_FAILED", err)
	}
	if err := removeExactFile(path, digest(body)); err != nil {
		return fail.Wrap("MIGRATION_JOURNAL_FAILED", err)
	}
	return nil
}

func persistBootstrapPlan(commonDir string, plan Plan) error {
	body, err := normalizedJSON(plan)
	if err != nil {
		return err
	}
	if len(body) > MaxPlanBytes {
		return fail.Errorf("MIGRATION_PLAN_TOO_LARGE", "migration plan is %d bytes; maximum is %d", len(body), MaxPlanBytes)
	}
	if err := createExact(bootstrapPlanPath(commonDir, plan.MigrationID), body, 0o400); err != nil {
		return fail.Wrap("MIGRATION_PLAN_FAILED", err)
	}
	return nil
}

func loadPersistedPlan(commonDir, migrationID string) (Plan, error) {
	canonical, canonicalErr := loadPlan(migrationPlanPath(commonDir, migrationID))
	bootstrap, bootstrapErr := loadPlan(bootstrapPlanPath(commonDir, migrationID))
	if canonicalErr == nil && bootstrapErr == nil {
		left, leftErr := normalizedJSON(canonical)
		right, rightErr := normalizedJSON(bootstrap)
		if leftErr != nil || rightErr != nil || !bytes.Equal(left, right) {
			return Plan{}, fail.New("MIGRATION_PLAN_INVALID", "canonical and bootstrap migration plans differ")
		}
		return canonical, nil
	}
	if canonicalErr == nil {
		if bootstrapErr != nil && !errors.Is(bootstrapErr, os.ErrNotExist) {
			return Plan{}, bootstrapErr
		}
		return canonical, nil
	}
	if bootstrapErr == nil {
		if canonicalErr != nil && !errors.Is(canonicalErr, os.ErrNotExist) {
			return Plan{}, canonicalErr
		}
		return bootstrap, nil
	}
	if !errors.Is(canonicalErr, os.ErrNotExist) {
		return Plan{}, canonicalErr
	}
	return Plan{}, bootstrapErr
}

func activateWriterFence(commonDir string, plan Plan) error {
	path := writerFencePath(commonDir)
	var existing WriterFence
	if _, err := readJSONFile(path, &existing); err == nil {
		if existing.SchemaVersion != writerFenceSchemaVersion || existing.MigrationID != plan.MigrationID || existing.PlanDigest != plan.PlanDigest {
			return fail.New("MIGRATION_FENCE_CONFLICT", "a different or invalid migration writer fence exists")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail.Wrap("MIGRATION_FENCE_CONFLICT", err)
	}
	fence := WriterFence{SchemaVersion: writerFenceSchemaVersion, MigrationID: plan.MigrationID, PlanDigest: plan.PlanDigest, CreatedAt: time.Now().UTC()}
	if err := writeJSON(path, fence, 0o600, false); err != nil {
		return fail.Wrap("MIGRATION_FENCE_FAILED", err)
	}
	return nil
}

func deactivateWriterFence(commonDir string, plan Plan) error {
	path := writerFencePath(commonDir)
	var fence WriterFence
	if _, err := readJSONFile(path, &fence); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fail.Wrap("MIGRATION_FENCE_CONFLICT", err)
	}
	if fence.SchemaVersion != writerFenceSchemaVersion || fence.MigrationID != plan.MigrationID || fence.PlanDigest != plan.PlanDigest {
		return fail.New("MIGRATION_FENCE_CONFLICT", "writer fence does not bind the completed migration")
	}
	if err := os.Remove(path); err != nil {
		return fail.Wrap("MIGRATION_FENCE_FAILED", err)
	}
	return syncDirectory(commonDir)
}

func verifyJournalPlanBinding(repo gitx.Repo, plan Plan, journal Journal) error {
	if journal.MigrationID != plan.MigrationID || journal.PlanDigest != plan.PlanDigest || journal.ExpectedCanonicalDigest != plan.ExpectedCanonicalData.Digest || journal.PlannedCanonicalDigest != plan.PlannedCanonicalData.Digest {
		return fail.New("MIGRATION_JOURNAL_INVALID", "migration journal does not bind the reviewed canonical prestate and poststate")
	}
	persisted, err := loadPersistedPlan(repo.CommonDir, plan.MigrationID)
	if err != nil {
		return fail.Wrap("MIGRATION_PLAN_INVALID", err)
	}
	if err := persisted.ValidateDigest(plan.PlanDigest); err != nil {
		return fail.Wrap("MIGRATION_PLAN_INVALID", err)
	}
	leftBody, leftErr := normalizedJSON(persisted)
	rightBody, rightErr := normalizedJSON(plan)
	if persisted.PlanDigest != plan.PlanDigest || persisted.MigrationID != plan.MigrationID || leftErr != nil || rightErr != nil || !bytes.Equal(leftBody, rightBody) {
		return fail.New("MIGRATION_PLAN_INVALID", "persisted migration plan differs from the exact reviewed plan")
	}
	return nil
}
