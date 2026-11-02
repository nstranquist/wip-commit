package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nstranquist/wip-commit/internal/coordmigration"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/safeio"
)

const migrationPlanLimit = coordmigration.MaxPlanBytes

type migrationProducerFunc func() (coordmigration.ProducerIdentity, error)

type migrationCommandResult struct {
	SchemaVersion string                 `json:"schema_version"`
	Mode          string                 `json:"mode"`
	Plan          *coordmigration.Plan   `json:"plan,omitempty"`
	Status        *coordmigration.Status `json:"status,omitempty"`
	Applied       bool                   `json:"applied,omitempty"`
	Resumed       bool                   `json:"resumed,omitempty"`
	RolledBack    bool                   `json:"rolled_back,omitempty"`
}

func (application *app) runMigrate(ctx context.Context, repo gitx.Repo, args []string) int {
	set := application.flagSet("migrate")
	preview := set.Bool("preview", false, "create a read-only migration plan")
	statusMode := set.Bool("status", false, "inspect migration state without mutation")
	apply := set.Bool("apply", false, "apply an exact reviewed migration plan")
	resume := set.Bool("resume", false, "resume an interrupted exact migration plan")
	rollback := set.Bool("rollback", false, "roll back exact outputs before the source move")
	planPath := set.String("plan", "", "strict migration plan file")
	planDigest := set.String("plan-digest", "", "exact reviewed sha256 plan digest")
	migrationID := set.String("migration-id", "", "migration identity for status")
	yes := set.Bool("yes", false, "confirm an apply, resume, or rollback")
	lockWait := set.Duration("lock-wait", 0, "common writer-lock wait")
	if err := set.Parse(args); err != nil {
		return application.failure("migrate", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if set.NArg() != 0 {
		return application.failure("migrate", fail.New("INVALID_ARGS", "migrate accepts no positional arguments"), nil, 2)
	}
	modes := 0
	for _, selected := range []bool{*preview, *statusMode, *apply, *resume, *rollback} {
		if selected {
			modes++
		}
	}
	if modes != 1 {
		return application.failure("migrate", fail.New("INVALID_ARGS", "select exactly one of --preview, --status, --apply, --resume, or --rollback"), nil, 2)
	}
	if *preview {
		if *planPath != "" || *planDigest != "" || *migrationID != "" || *yes {
			return application.failure("migrate", fail.New("INVALID_ARGS", "preview does not accept apply, status, or confirmation inputs"), nil, 2)
		}
		producer, err := application.resolveMigrationProducer()
		if err != nil {
			return application.failure("migrate", fail.Wrap("MIGRATION_PRODUCER_INVALID", err), nil, 1)
		}
		plan, err := coordmigration.Preview(ctx, repo, coordmigration.PreviewOptions{Producer: producer})
		if err != nil {
			return application.failure("migrate", err, nil, 1)
		}
		result := migrationCommandResult{SchemaVersion: coordmigration.StatusSchemaVersion, Mode: "preview", Plan: &plan}
		human := fmt.Sprintf("Migration preview %s is %s. Review plan digest %s.", plan.MigrationID, plan.State, plan.PlanDigest)
		return application.success("migrate", result, human)
	}
	if *statusMode {
		if *planPath != "" || *planDigest != "" || *yes {
			return application.failure("migrate", fail.New("INVALID_ARGS", "status does not accept a plan or confirmation"), nil, 2)
		}
		status, err := coordmigration.InspectStatus(repo, strings.TrimSpace(*migrationID))
		if err != nil {
			return application.failure("migrate", err, nil, 1)
		}
		result := migrationCommandResult{SchemaVersion: coordmigration.StatusSchemaVersion, Mode: "status", Status: &status}
		return application.success("migrate", result, fmt.Sprintf("Coordination migration state: %s.", status.State))
	}
	if strings.TrimSpace(*migrationID) != "" {
		return application.failure("migrate", fail.New("INVALID_ARGS", "--migration-id is valid only with --status"), nil, 2)
	}
	if !*yes || strings.TrimSpace(*planPath) == "" || strings.TrimSpace(*planDigest) == "" {
		return application.failure("migrate", fail.New("CONFIRMATION_REQUIRED", "apply, resume, and rollback require --plan, --plan-digest, and --yes"), nil, 2)
	}
	plan, err := readMigrationPlan(application.stdin, *planPath)
	if err != nil {
		return application.failure("migrate", fail.Wrap("MIGRATION_PLAN_INVALID", err), nil, 1)
	}
	producer, err := application.resolveMigrationProducer()
	if err != nil {
		return application.failure("migrate", fail.Wrap("MIGRATION_PRODUCER_INVALID", err), nil, 1)
	}
	options := coordmigration.ApplyOptions{Resume: *resume, Rollback: *rollback, LockWait: *lockWait, Producer: producer}
	applied, err := coordmigration.Apply(ctx, repo, plan, strings.TrimSpace(*planDigest), options)
	if err != nil {
		return application.failure("migrate", err, nil, 1)
	}
	result := migrationCommandResult{SchemaVersion: coordmigration.StatusSchemaVersion, Mode: applied.Mode, Plan: &plan, Status: &applied.Status, Applied: applied.Applied, Resumed: applied.Resumed, RolledBack: applied.RolledBack}
	return application.success("migrate", result, fmt.Sprintf("Coordination migration %s: %s.", plan.MigrationID, applied.Status.State))
}

func (application *app) resolveMigrationProducer() (coordmigration.ProducerIdentity, error) {
	if application.migrationProducer != nil {
		producer, err := application.migrationProducer()
		if err != nil {
			return coordmigration.ProducerIdentity{}, err
		}
		if err := producer.Validate(); err != nil {
			return coordmigration.ProducerIdentity{}, err
		}
		return producer, nil
	}
	return currentMigrationProducer()
}

func currentMigrationProducer() (coordmigration.ProducerIdentity, error) {
	versionInfo := currentVersionInfo()
	if !versionInfo.SourceStamped || versionInfo.Revision == "" {
		return coordmigration.ProducerIdentity{}, errors.New("running executable has no exact source revision")
	}
	if versionInfo.SourceState != "clean" && versionInfo.SourceState != "modified" {
		return coordmigration.ProducerIdentity{}, errors.New("running executable has no exact clean or modified source state")
	}
	if versionInfo.ReleaseTag != "" && versionInfo.Channel != "release" {
		return coordmigration.ProducerIdentity{}, errors.New("running executable has an unverified release tag")
	}
	contractResult, err := currentContractResult()
	if err != nil {
		return coordmigration.ProducerIdentity{}, err
	}
	_, executable, err := selfExecutable()
	if err != nil {
		return coordmigration.ProducerIdentity{}, err
	}
	executableSum := sha256.Sum256(executable)
	producer := coordmigration.ProducerIdentity{
		CommandProtocol:      contractResult.Protocol,
		Product:              "github.com/nstranquist/wip-commit",
		Version:              versionInfo.Version,
		SourceRevision:       versionInfo.Revision,
		SourceState:          versionInfo.SourceState,
		ReleaseTagged:        versionInfo.Channel == "release",
		PortableBundleSHA256: contractResult.PortableBundleSHA256,
		ExecutableSHA256:     "sha256:" + hex.EncodeToString(executableSum[:]),
	}
	if producer.ReleaseTagged {
		producer.ReleaseTag = versionInfo.ReleaseTag
	}
	if err := producer.Validate(); err != nil {
		return coordmigration.ProducerIdentity{}, err
	}
	return producer, nil
}

func readMigrationPlan(stdin io.Reader, path string) (coordmigration.Plan, error) {
	var body []byte
	var err error
	if path == "-" {
		if stdin == nil {
			return coordmigration.Plan{}, errors.New("stdin is unavailable")
		}
		limited := io.LimitReader(stdin, migrationPlanLimit+1)
		body, err = io.ReadAll(limited)
	} else {
		body, err = safeio.ReadRegular(path, migrationPlanLimit)
	}
	if err != nil {
		return coordmigration.Plan{}, err
	}
	if len(body) > migrationPlanLimit {
		return coordmigration.Plan{}, fmt.Errorf("plan exceeds %d bytes", migrationPlanLimit)
	}
	return coordmigration.DecodePlan(body)
}
