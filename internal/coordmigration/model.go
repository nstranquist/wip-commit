// Package coordmigration migrates the retired NDev WIP store into the
// standalone coordination domain. It never grants landing authority to the
// standalone command.
package coordmigration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/contract"
)

const (
	PlanSchemaVersion     = "wip.coordination-migration-plan/1.2.0"
	StatusSchemaVersion   = "wip.coordination-migration-status/1.0.0"
	JournalSchemaVersion  = "wip.coordination-migration-journal/1.1.0"
	RollbackSchemaVersion = "wip.coordination-migration-rollback/1.0.0"
	ReceiptSchemaVersion  = "wip.coordination-migration-receipt/1.0.0"
	SealSchemaVersion     = "wip.legacy-ndev-wip-seal/1.0.0"
	DomainID              = "github.com/nstranquist/wip-commit"
	maxManifestEntries    = 20_000
	maxManifestBytes      = 128 << 20
	maxRecordBytes        = 1 << 20
	// MaxPlanBytes is the one bound used by preview, persisted-plan reload, and
	// both command adapters. Keeping one public bound prevents a plan that can be
	// produced but cannot later be reviewed, applied, resumed, or inspected.
	MaxPlanBytes = 16 << 20
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var (
	migrationIDPattern = regexp.MustCompile(`^migration-[0-9a-f]{24}$`)
	revisionPattern    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	semverPattern      = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
)

const migrationProducerProduct = "github.com/nstranquist/wip-commit"

// ProducerIdentity binds a reviewed plan to the exact standalone executable,
// source revision, command protocol, and embedded portable instruction bundle
// that produced it. Apply and resume must present the same identity.
type ProducerIdentity struct {
	CommandProtocol      string `json:"command_protocol"`
	Product              string `json:"product"`
	Version              string `json:"version"`
	SourceRevision       string `json:"source_revision"`
	SourceState          string `json:"source_state"`
	ReleaseTagged        bool   `json:"release_tagged"`
	ReleaseTag           string `json:"release_tag,omitempty"`
	PortableBundleSHA256 string `json:"portable_bundle_sha256"`
	ExecutableSHA256     string `json:"executable_sha256"`
}

func (producer ProducerIdentity) Validate() error {
	if producer.CommandProtocol != contract.Protocol {
		return fmt.Errorf("producer command protocol %q is not %q", producer.CommandProtocol, contract.Protocol)
	}
	if producer.Product != migrationProducerProduct {
		return fmt.Errorf("producer product %q is not %q", producer.Product, migrationProducerProduct)
	}
	if !semverPattern.MatchString(producer.Version) {
		return fmt.Errorf("producer version %q is not SemVer", producer.Version)
	}
	if !revisionPattern.MatchString(producer.SourceRevision) {
		return fmt.Errorf("producer source revision is not an exact Git object id")
	}
	if producer.SourceState != "clean" && producer.SourceState != "modified" {
		return fmt.Errorf("producer source state %q is not clean or modified", producer.SourceState)
	}
	if producer.ReleaseTagged {
		if producer.SourceState != "clean" || producer.ReleaseTag != "v"+producer.Version {
			return fmt.Errorf("tagged producer must be clean and use the exact version tag")
		}
	} else if producer.ReleaseTag != "" {
		return fmt.Errorf("untagged producer cannot name a release tag")
	}
	if !digestPattern.MatchString(producer.PortableBundleSHA256) {
		return fmt.Errorf("producer portable bundle digest is invalid")
	}
	if !digestPattern.MatchString(producer.ExecutableSHA256) {
		return fmt.Errorf("producer executable digest is invalid")
	}
	return nil
}

type ManifestEntry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Mode   string `json:"mode"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type TreeManifest struct {
	LogicalRoot string          `json:"logical_root"`
	Exists      bool            `json:"exists"`
	Entries     []ManifestEntry `json:"entries"`
	Digest      string          `json:"digest"`
}

// CanonicalManifest binds the two writable canonical data roots. Migration
// metadata and the sealed source archive are intentionally outside this
// digest, so the value is stable from record publication through completion.
type CanonicalManifest struct {
	Capture TreeManifest `json:"capture"`
	Review  TreeManifest `json:"review"`
	Digest  string       `json:"digest"`
}

type ImportRecord struct {
	Kind              string `json:"kind"`
	ID                string `json:"id"`
	Source            string `json:"source"`
	Destination       string `json:"destination"`
	Action            string `json:"action"`
	SourceSHA256      string `json:"source_sha256"`
	DestinationSHA256 string `json:"destination_sha256,omitempty"`
	Size              int64  `json:"size"`
	Ref               string `json:"ref,omitempty"`
	CurrentSHA        string `json:"current_sha,omitempty"`
}

type CopyRecord struct {
	Kind              string `json:"kind"`
	Source            string `json:"source"`
	Destination       string `json:"destination"`
	Action            string `json:"action"`
	SourceSHA256      string `json:"source_sha256"`
	DestinationSHA256 string `json:"destination_sha256,omitempty"`
	Size              int64  `json:"size"`
	Mode              string `json:"mode"`
}

type RefExpectation struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type RecordDisposition struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Source  string `json:"source"`
	Action  string `json:"action"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Conflict struct {
	Code        string `json:"code"`
	Kind        string `json:"kind"`
	ID          string `json:"id,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination,omitempty"`
	Message     string `json:"message"`
}

type Plan struct {
	SchemaVersion         string              `json:"schema_version"`
	MigrationID           string              `json:"migration_id"`
	RepositoryCommonDir   string              `json:"repository_common_dir"`
	State                 string              `json:"state"`
	Producer              ProducerIdentity    `json:"producer"`
	SourceLegacy          TreeManifest        `json:"source_legacy"`
	ExpectedCanonical     TreeManifest        `json:"expected_canonical"`
	ExpectedCanonicalData CanonicalManifest   `json:"expected_canonical_data"`
	PlannedCanonicalData  CanonicalManifest   `json:"planned_canonical_data"`
	ReconciliationHistory TreeManifest        `json:"reconciliation_history"`
	LaneImports           []ImportRecord      `json:"lane_imports"`
	LeaseImports          []ImportRecord      `json:"lease_imports"`
	ReviewCopies          []CopyRecord        `json:"review_copies"`
	ReconciliationCopies  []CopyRecord        `json:"reconciliation_copies"`
	Dispositions          []RecordDisposition `json:"dispositions"`
	ExpectedRefs          []RefExpectation    `json:"expected_refs"`
	Conflicts             []Conflict          `json:"conflicts"`
	PlanDigest            string              `json:"plan_digest"`
}

type Journal struct {
	SchemaVersion           string    `json:"schema_version"`
	MigrationID             string    `json:"migration_id"`
	PlanDigest              string    `json:"plan_digest"`
	ExpectedCanonicalDigest string    `json:"expected_canonical_digest"`
	PlannedCanonicalDigest  string    `json:"planned_canonical_digest"`
	PublishedPaths          []string  `json:"published_paths"`
	Phase                   string    `json:"phase"`
	UpdatedAt               time.Time `json:"updated_at"`
	LastError               string    `json:"last_error,omitempty"`
}

type WriterFence struct {
	SchemaVersion string    `json:"schema_version"`
	MigrationID   string    `json:"migration_id"`
	PlanDigest    string    `json:"plan_digest"`
	CreatedAt     time.Time `json:"created_at"`
}

type RollbackMarker struct {
	SchemaVersion string    `json:"schema_version"`
	MigrationID   string    `json:"migration_id"`
	PlanDigest    string    `json:"plan_digest"`
	State         string    `json:"state"`
	Reason        string    `json:"reason"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Seal struct {
	SchemaVersion      string    `json:"schema_version"`
	MigrationID        string    `json:"migration_id"`
	PlanDigest         string    `json:"plan_digest"`
	SourceManifest     string    `json:"source_manifest_digest"`
	ArchiveManifest    string    `json:"archive_manifest_digest"`
	ArchiveRelativeDir string    `json:"archive_relative_dir"`
	SealedAt           time.Time `json:"sealed_at"`
}

type Receipt struct {
	SchemaVersion         string    `json:"schema_version"`
	MigrationID           string    `json:"migration_id"`
	PlanDigest            string    `json:"plan_digest"`
	SourceManifest        string    `json:"source_manifest_digest"`
	CanonicalManifest     string    `json:"canonical_manifest_digest"`
	ArchiveManifest       string    `json:"archive_manifest_digest,omitempty"`
	ReconciliationHistory string    `json:"reconciliation_history_digest"`
	CompletedAt           time.Time `json:"completed_at"`
	ReceiptDigest         string    `json:"receipt_digest"`
}

type Status struct {
	SchemaVersion string          `json:"schema_version"`
	State         string          `json:"state"`
	MigrationID   string          `json:"migration_id,omitempty"`
	PlanDigest    string          `json:"plan_digest,omitempty"`
	Phase         string          `json:"phase,omitempty"`
	LegacyExists  bool            `json:"legacy_exists"`
	Canonical     bool            `json:"canonical_domain"`
	Review        bool            `json:"review_store"`
	Archive       bool            `json:"sealed_archive"`
	Rollback      *RollbackMarker `json:"rollback,omitempty"`
	Receipt       *Receipt        `json:"receipt,omitempty"`
}

type Result struct {
	SchemaVersion string   `json:"schema_version"`
	Mode          string   `json:"mode"`
	Applied       bool     `json:"applied"`
	Resumed       bool     `json:"resumed"`
	RolledBack    bool     `json:"rolled_back"`
	Plan          Plan     `json:"plan"`
	Status        Status   `json:"status"`
	Receipt       *Receipt `json:"receipt,omitempty"`
}

func (plan Plan) ValidateDigest(expected string) error {
	if err := plan.Producer.Validate(); err != nil {
		return fmt.Errorf("plan producer identity is invalid: %w", err)
	}
	if !migrationIDPattern.MatchString(plan.MigrationID) {
		return fmt.Errorf("plan migration identity is invalid")
	}
	wantedMigrationID := migrationID(plan.SourceLegacy.Digest, plan.ExpectedCanonical.Digest, plan.ReconciliationHistory.Digest)
	if plan.MigrationID != wantedMigrationID {
		return fmt.Errorf("plan migration identity does not match its bound manifests")
	}
	if !digestPattern.MatchString(expected) || plan.PlanDigest != expected {
		return fmt.Errorf("plan digest does not match the exact reviewed digest")
	}
	actual, err := planDigest(plan)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("plan content digest is %s, expected %s", actual, expected)
	}
	return nil
}

func planDigest(plan Plan) (string, error) {
	plan.PlanDigest = ""
	body, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return digest(body), nil
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func migrationID(source, canonical, reconciliation string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{source, canonical, reconciliation}, "\x00")))
	return "migration-" + hex.EncodeToString(sum[:12])
}
