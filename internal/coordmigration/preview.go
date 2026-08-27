package coordmigration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/safeio"
	capturestore "github.com/nstranquist/wip-commit/internal/store"
)

type PreviewOptions struct {
	AfterFirstSnapshot func() error
	Now                func() time.Time
	Producer           ProducerIdentity
}

type legacyLane = capturestore.Lane
type legacyLease = capturestore.Lease

func Preview(ctx context.Context, repo gitx.Repo, options PreviewOptions) (Plan, error) {
	if err := options.Producer.Validate(); err != nil {
		return Plan{}, fail.Wrap("MIGRATION_PRODUCER_INVALID", err)
	}
	firstSource, err := manifestTree(repo.CommonDir, "ndev-wip")
	if err != nil {
		return Plan{}, fail.Wrap("MIGRATION_SOURCE_UNSAFE", err)
	}
	firstCanonical, err := manifestTree(repo.CommonDir, "wip")
	if err != nil {
		return Plan{}, fail.Wrap("MIGRATION_DESTINATION_UNSAFE", err)
	}
	firstReconciliation, err := manifestTree(repo.CommonDir, filepath.Join("ndev-commit-lane", "reconcile"))
	if err != nil {
		return Plan{}, fail.Wrap("MIGRATION_SOURCE_UNSAFE", err)
	}
	firstCanonicalData, err := manifestCanonicalData(repo.CommonDir)
	if err != nil {
		return Plan{}, fail.Wrap("MIGRATION_DESTINATION_UNSAFE", err)
	}
	if options.AfterFirstSnapshot != nil {
		if err := options.AfterFirstSnapshot(); err != nil {
			return Plan{}, err
		}
	}
	secondSource, err := manifestTree(repo.CommonDir, "ndev-wip")
	if err != nil {
		return Plan{}, fail.Wrap("MIGRATION_SOURCE_UNSAFE", err)
	}
	secondCanonical, err := manifestTree(repo.CommonDir, "wip")
	if err != nil {
		return Plan{}, fail.Wrap("MIGRATION_DESTINATION_UNSAFE", err)
	}
	secondReconciliation, err := manifestTree(repo.CommonDir, filepath.Join("ndev-commit-lane", "reconcile"))
	if err != nil {
		return Plan{}, fail.Wrap("MIGRATION_SOURCE_UNSAFE", err)
	}
	secondCanonicalData, err := manifestCanonicalData(repo.CommonDir)
	if err != nil {
		return Plan{}, fail.Wrap("MIGRATION_DESTINATION_UNSAFE", err)
	}
	if firstSource.Digest != secondSource.Digest || firstCanonical.Digest != secondCanonical.Digest || firstReconciliation.Digest != secondReconciliation.Digest || firstCanonicalData.Digest != secondCanonicalData.Digest {
		return Plan{}, fail.New("MIGRATION_SOURCE_MOVED", "coordination state changed during the read-only preview; stop writers and retry")
	}

	plan := Plan{
		SchemaVersion:         PlanSchemaVersion,
		MigrationID:           migrationID(firstSource.Digest, firstCanonical.Digest, firstReconciliation.Digest),
		RepositoryCommonDir:   repo.CommonDir,
		State:                 "ready",
		Producer:              options.Producer,
		SourceLegacy:          firstSource,
		ExpectedCanonical:     firstCanonical,
		ExpectedCanonicalData: firstCanonicalData,
		ReconciliationHistory: firstReconciliation,
		LaneImports:           []ImportRecord{},
		LeaseImports:          []ImportRecord{},
		ReviewCopies:          []CopyRecord{},
		ReconciliationCopies:  []CopyRecord{},
		Dispositions:          []RecordDisposition{},
		ExpectedRefs:          []RefExpectation{},
		Conflicts:             []Conflict{},
	}
	now := time.Now().UTC()
	if options.Now != nil {
		now = options.Now().UTC()
	}
	portableLanes := map[string]legacyLane{}
	portableLeases := map[string]legacyLease{}
	if firstCanonical.Exists {
		if _, err := exactCanonicalDomain(repo.CommonDir); err != nil {
			return Plan{}, err
		}
		portableLanes, portableLeases, err = loadPortableRecords(repo, firstCanonical, now)
		if err != nil {
			return Plan{}, fail.Wrap("MIGRATION_DESTINATION_INVALID", err)
		}
	}
	if firstSource.Exists {
		if err := validateLegacyLayout(firstSource); err != nil {
			return Plan{}, fail.Wrap("MIGRATION_SOURCE_INVALID", err)
		}
		if err := planLegacyRecords(ctx, repo, &plan, now, portableLanes, portableLeases); err != nil {
			return Plan{}, err
		}
		if err := planReviewCopies(repo, &plan); err != nil {
			return Plan{}, err
		}
	}
	if err := planReconciliationCopies(repo, &plan); err != nil {
		return Plan{}, err
	}
	if len(plan.Conflicts) > 0 {
		plan.State = "blocked"
	}
	plan.PlannedCanonicalData, err = planCanonicalData(firstCanonicalData, plan)
	if err != nil {
		return Plan{}, fail.Wrap("MIGRATION_PLAN_FAILED", err)
	}
	sort.Slice(plan.LaneImports, func(i, j int) bool { return plan.LaneImports[i].ID < plan.LaneImports[j].ID })
	sort.Slice(plan.LeaseImports, func(i, j int) bool { return plan.LeaseImports[i].ID < plan.LeaseImports[j].ID })
	sort.Slice(plan.ReviewCopies, func(i, j int) bool { return plan.ReviewCopies[i].Destination < plan.ReviewCopies[j].Destination })
	sort.Slice(plan.ReconciliationCopies, func(i, j int) bool {
		return plan.ReconciliationCopies[i].Destination < plan.ReconciliationCopies[j].Destination
	})
	sort.Slice(plan.Dispositions, func(i, j int) bool {
		return plan.Dispositions[i].Kind+"\x00"+plan.Dispositions[i].ID < plan.Dispositions[j].Kind+"\x00"+plan.Dispositions[j].ID
	})
	sort.Slice(plan.ExpectedRefs, func(i, j int) bool { return plan.ExpectedRefs[i].Ref < plan.ExpectedRefs[j].Ref })
	sort.Slice(plan.Conflicts, func(i, j int) bool {
		left := plan.Conflicts[i].Code + "\x00" + plan.Conflicts[i].Destination
		right := plan.Conflicts[j].Code + "\x00" + plan.Conflicts[j].Destination
		return left < right
	})
	plan.PlanDigest, err = planDigest(plan)
	if err != nil {
		return Plan{}, fail.Wrap("MIGRATION_PLAN_FAILED", err)
	}
	encoded, err := normalizedJSON(plan)
	if err != nil {
		return Plan{}, fail.Wrap("MIGRATION_PLAN_FAILED", err)
	}
	if len(encoded) > MaxPlanBytes {
		return Plan{}, fail.Errorf("MIGRATION_PLAN_TOO_LARGE", "migration plan is %d bytes; maximum is %d", len(encoded), MaxPlanBytes)
	}
	return plan, nil
}

func validateLegacyLayout(manifest TreeManifest) error {
	required := map[string]string{
		".":         "directory",
		"v1":        "directory",
		"v1/lanes":  "directory",
		"v1/leases": "directory",
	}
	emptyV2Scaffold := map[string]string{
		"v2":          "directory",
		"v2/lanes":    "directory",
		"v2/leases":   "directory",
		"v2/receipts": "directory",
	}
	seen := map[string]string{}
	for _, entry := range manifest.Entries {
		seen[entry.Path] = entry.Type
		if entry.Path != "." && !strings.HasPrefix(entry.Path, "v1/") && entry.Path != "v1" {
			expectedType, allowed := emptyV2Scaffold[entry.Path]
			if !allowed {
				return fmt.Errorf("legacy root contains unsupported top-level entry %s", entry.Path)
			}
			if entry.Type != expectedType {
				return fmt.Errorf("legacy v2 scaffold entry %s has type %s, want %s", entry.Path, entry.Type, expectedType)
			}
		}
		if strings.HasPrefix(entry.Path, "v1/lanes/") {
			if entry.Type != "file" || filepath.ToSlash(filepath.Dir(entry.Path)) != "v1/lanes" || !strings.HasSuffix(entry.Path, ".json") {
				return fmt.Errorf("legacy lane entry is not a direct JSON record: %s", entry.Path)
			}
		}
		if strings.HasPrefix(entry.Path, "v1/leases/") {
			if entry.Type != "file" || filepath.ToSlash(filepath.Dir(entry.Path)) != "v1/leases" || !strings.HasSuffix(entry.Path, ".json") {
				return fmt.Errorf("legacy lease entry is not a direct JSON record: %s", entry.Path)
			}
		}
	}
	for path, kind := range required {
		if seen[path] != kind {
			return fmt.Errorf("legacy store is missing %s %s", kind, path)
		}
	}
	if _, hasV2 := seen["v2"]; hasV2 {
		for path, kind := range emptyV2Scaffold {
			if seen[path] != kind {
				return fmt.Errorf("legacy v2 scaffold is missing %s %s", kind, path)
			}
		}
	}
	return nil
}

type laneCandidate struct {
	path, fileID, action, code, message string
	lane                                legacyLane
	normalized                          []byte
	refVerified                         bool
}

type leaseCandidate struct {
	path, fileID, action, code, message string
	lease                               legacyLease
	normalized                          []byte
}

func planLegacyRecords(ctx context.Context, repo gitx.Repo, plan *Plan, now time.Time, portableLanes map[string]legacyLane, portableLeases map[string]legacyLease) error {
	lanes := map[string]*laneCandidate{}
	leases := map[string]*leaseCandidate{}
	for _, entry := range plan.SourceLegacy.Entries {
		if entry.Type != "file" {
			continue
		}
		switch {
		case filepath.ToSlash(filepath.Dir(entry.Path)) == "v1/lanes" && strings.HasSuffix(entry.Path, ".json"):
			var lane legacyLane
			if _, err := readJSONFile(filepath.Join(repo.CommonDir, "ndev-wip", filepath.FromSlash(entry.Path)), &lane); err != nil {
				return fail.Wrap("MIGRATION_SOURCE_INVALID", err)
			}
			normalized, err := normalizedJSON(lane)
			if err != nil {
				return err
			}
			fileID := strings.TrimSuffix(filepath.Base(entry.Path), ".json")
			candidate := &laneCandidate{path: entry.Path, fileID: fileID, lane: lane, normalized: normalized, action: "import"}
			if readerErr := capturestore.ValidateImportedLane(repo, lane); readerErr != nil || lane.ID != fileID {
				candidate.code, candidate.message = "DESTINATION_READER_REJECTED", readerError(readerErr, "lane file identity differs from its record")
				candidate.action = terminalLaneAction(lane.State)
			}
			if _, exists := lanes[fileID]; exists {
				return fail.Errorf("MIGRATION_SOURCE_INVALID", "duplicate legacy lane basename %q", fileID)
			}
			lanes[fileID] = candidate
		case filepath.ToSlash(filepath.Dir(entry.Path)) == "v1/leases" && strings.HasSuffix(entry.Path, ".json"):
			var lease legacyLease
			if _, err := readJSONFile(filepath.Join(repo.CommonDir, "ndev-wip", filepath.FromSlash(entry.Path)), &lease); err != nil {
				return fail.Wrap("MIGRATION_SOURCE_INVALID", err)
			}
			normalized, err := normalizedJSON(lease)
			if err != nil {
				return err
			}
			fileID := strings.TrimSuffix(filepath.Base(entry.Path), ".json")
			candidate := &leaseCandidate{path: entry.Path, fileID: fileID, lease: lease, normalized: normalized, action: "import"}
			if readerErr := capturestore.ValidateImportedLease(repo, lease); readerErr != nil || lease.ID != fileID {
				candidate.code, candidate.message = "DESTINATION_READER_REJECTED", readerError(readerErr, "lease file identity differs from its record")
				candidate.action = terminalLeaseAction(lease.State)
			}
			if _, exists := leases[fileID]; exists {
				return fail.Errorf("MIGRATION_SOURCE_INVALID", "duplicate legacy lease basename %q", fileID)
			}
			leases[fileID] = candidate
		}
	}

	// A lane record is not destination-readable in practice when its durable
	// cursor cannot be resolved. Classify this before relationship closure so a
	// terminal lane that becomes archive-only also removes dependent terminal
	// leases from the canonical import set.
	for _, lane := range lanes {
		if lane.action != "import" {
			continue
		}
		actual, err := repo.Text(ctx, nil, "rev-parse", "--verify", lane.lane.Ref+"^{commit}")
		if err == nil && actual == lane.lane.CurrentSHA {
			lane.refVerified = true
			continue
		}
		lane.code, lane.message = "REF_MISMATCH", "lane ref is absent or differs from its stored cursor"
		if isTerminalLane(lane.lane.State) {
			lane.action = "archive-only"
			lane.code, lane.message = "HISTORICAL_REF_MISSING", "terminal lane ref is absent or differs from its stored cursor"
		} else {
			lane.action = "conflict"
		}
	}

	// Excluding one terminal record can make its terminal counterpart invalid.
	// Reach a fixed point before any destination path is planned.
	for changed := true; changed; {
		changed = false
		for _, lease := range leases {
			if lease.action != "import" {
				continue
			}
			lane := lanes[lease.lease.LaneID]
			owner, ownerFound := portableLanes[lease.lease.LaneID]
			if lane != nil && lane.action == "import" {
				owner, ownerFound = lane.lane, true
			}
			invalid := !ownerFound || owner.Agent != lease.lease.Agent || owner.Session != lease.lease.Session || !containsString(owner.LeaseIDs, lease.lease.ID) || isTerminalLane(owner.State) && lease.lease.State == "active"
			if !invalid {
				continue
			}
			if lease.lease.State == "released" {
				lease.action, lease.code, lease.message, changed = "archive-only", "INCOMPATIBLE_HISTORICAL_RELATION", "terminal lease cannot form a valid destination lane relationship", true
			} else {
				lease.action, lease.code, lease.message = "conflict", "ACTIVE_LEASE_RELATION_INVALID", "active lease does not have an importable owning lane with an exact reverse reference"
			}
		}
		for _, lane := range lanes {
			if lane.action != "import" {
				continue
			}
			for _, leaseID := range lane.lane.LeaseIDs {
				lease := leases[leaseID]
				owned, ownedFound := portableLeases[leaseID]
				if lease != nil && lease.action == "import" {
					owned, ownedFound = lease.lease, true
				}
				valid := ownedFound && owned.LaneID == lane.lane.ID && owned.Agent == lane.lane.Agent && owned.Session == lane.lane.Session
				if valid && !invalidLaneLeaseState(lane.lane.State, owned.State) {
					continue
				}
				if isTerminalLane(lane.lane.State) {
					lane.action, lane.code, lane.message, changed = "archive-only", "INCOMPATIBLE_HISTORICAL_RELATION", "terminal lane references a lease that cannot be imported", true
				} else {
					lane.action, lane.code, lane.message = "conflict", "ACTIVE_LANE_LEASE_INVALID", "active lane references a lease that cannot be imported"
				}
				break
			}
		}
	}

	for _, lane := range lanes {
		source := filepath.ToSlash(filepath.Join("ndev-wip", lane.path))
		operation := ImportRecord{Kind: "lane", ID: lane.fileID, Source: source, Action: lane.action, SourceSHA256: digest(lane.normalized), Size: int64(len(lane.normalized)), Ref: lane.lane.Ref, CurrentSHA: lane.lane.CurrentSHA}
		if lane.action == "import" {
			operation.Destination = filepath.ToSlash(filepath.Join("wip", "v1", "lanes", lane.fileID+".json"))
			compared, conflict, err := compareRecord(repo.CommonDir, "lane", lane.fileID, lane.path, operation.Destination, operation.SourceSHA256)
			if err != nil {
				return fail.Wrap("MIGRATION_DESTINATION_UNSAFE", err)
			}
			compared.Ref, compared.CurrentSHA = lane.lane.Ref, lane.lane.CurrentSHA
			compared.Size = int64(len(lane.normalized))
			operation = compared
			if conflict != nil {
				plan.Conflicts = append(plan.Conflicts, *conflict)
			}
			if lane.refVerified && operation.Action != "conflict" {
				plan.ExpectedRefs = append(plan.ExpectedRefs, RefExpectation{Ref: lane.lane.Ref, SHA: lane.lane.CurrentSHA})
			}
		}
		plan.LaneImports = append(plan.LaneImports, operation)
		appendDisposition(plan, "lane", lane.fileID, source, operation.Action, lane.code, lane.message)
		if operation.Action == "conflict" && lane.code != "" {
			plan.Conflicts = append(plan.Conflicts, Conflict{Code: lane.code, Kind: "lane", ID: lane.fileID, Source: source, Message: lane.message})
		}
	}
	for _, lease := range leases {
		source := filepath.ToSlash(filepath.Join("ndev-wip", lease.path))
		operation := ImportRecord{Kind: "lease", ID: lease.fileID, Source: source, Action: lease.action, SourceSHA256: digest(lease.normalized), Size: int64(len(lease.normalized))}
		if lease.action == "import" {
			operation.Destination = filepath.ToSlash(filepath.Join("wip", "v1", "leases", lease.fileID+".json"))
			compared, conflict, err := compareRecord(repo.CommonDir, "lease", lease.fileID, lease.path, operation.Destination, operation.SourceSHA256)
			if err != nil {
				return fail.Wrap("MIGRATION_DESTINATION_UNSAFE", err)
			}
			operation = compared
			operation.Size = int64(len(lease.normalized))
			if conflict != nil {
				plan.Conflicts = append(plan.Conflicts, *conflict)
			}
			if lease.lease.State == "active" && lease.lease.ExpiresAt != nil && !now.Before(*lease.lease.ExpiresAt) {
				appendDisposition(plan, "lease", lease.fileID, source, "import", "EXPIRED_ACTIVE_LEASE", "valid expired lease remains stored but does not grant an active path claim")
			}
		}
		plan.LeaseImports = append(plan.LeaseImports, operation)
		appendDisposition(plan, "lease", lease.fileID, source, operation.Action, lease.code, lease.message)
		if operation.Action == "conflict" && lease.code != "" {
			plan.Conflicts = append(plan.Conflicts, Conflict{Code: lease.code, Kind: "lease", ID: lease.fileID, Source: source, Message: lease.message})
		}
	}
	if err := validatePlannedDestination(plan, lanes, leases, portableLanes, portableLeases, now); err != nil {
		return err
	}
	return nil
}

func invalidLaneLeaseState(laneState, leaseState string) bool {
	return (laneState == "active" || laneState == "creating") && leaseState == "released" || isTerminalLane(laneState) && leaseState == "active"
}

// validatePlannedDestination applies the relationship and active-scope checks
// to the exact merged record set that a ready plan would publish. This guards
// cross-store ID references that are individually reader-valid but invalid
// together.
func validatePlannedDestination(plan *Plan, candidates map[string]*laneCandidate, leaseCandidates map[string]*leaseCandidate, portableLanes map[string]legacyLane, portableLeases map[string]legacyLease, now time.Time) error {
	if len(plan.Conflicts) != 0 {
		return nil
	}
	lanes := make(map[string]legacyLane, len(portableLanes)+len(candidates))
	leases := make(map[string]legacyLease, len(portableLeases)+len(leaseCandidates))
	for id, lane := range portableLanes {
		lanes[id] = lane
	}
	for id, lease := range portableLeases {
		leases[id] = lease
	}
	for _, record := range plan.LaneImports {
		if record.Action == "import" {
			lanes[record.ID] = candidates[record.ID].lane
		}
	}
	for _, record := range plan.LeaseImports {
		if record.Action == "import" {
			leases[record.ID] = leaseCandidates[record.ID].lease
		}
	}
	for id, lease := range leases {
		lane, ok := lanes[lease.LaneID]
		if !ok || lane.Agent != lease.Agent || lane.Session != lease.Session || !containsString(lane.LeaseIDs, id) || invalidLaneLeaseState(lane.State, lease.State) {
			plan.Conflicts = append(plan.Conflicts, Conflict{Code: "DESTINATION_RELATION_INVALID", Kind: "lease", ID: id, Message: "planned canonical lease does not have an exact valid lane relationship"})
		}
	}
	for id, lane := range lanes {
		for _, leaseID := range lane.LeaseIDs {
			lease, ok := leases[leaseID]
			if !ok || lease.LaneID != id || lease.Agent != lane.Agent || lease.Session != lane.Session || invalidLaneLeaseState(lane.State, lease.State) {
				plan.Conflicts = append(plan.Conflicts, Conflict{Code: "DESTINATION_RELATION_INVALID", Kind: "lane", ID: id, Message: "planned canonical lane references a missing, cross-lane, or state-incompatible lease"})
				break
			}
		}
	}
	mergedLeases := make([]capturestore.Lease, 0, len(leases))
	for _, lease := range leases {
		mergedLeases = append(mergedLeases, lease)
	}
	if conflict := capturestore.FindActiveLeaseConflict(mergedLeases, now); conflict != nil {
		plan.Conflicts = append(plan.Conflicts, Conflict{Code: "PATH_LEASE_CONFLICT", Kind: "lease", ID: conflict.RightLease, Message: fmt.Sprintf("planned active path %q in lane %s overlaps path %q in lane %s", conflict.RightPath, conflict.RightLane, conflict.LeftPath, conflict.LeftLane)})
	}
	return nil
}

func terminalLaneAction(state string) string {
	if isTerminalLane(state) {
		return "archive-only"
	}
	return "conflict"
}

func terminalLeaseAction(state string) string {
	if state == "released" {
		return "archive-only"
	}
	return "conflict"
}

func isTerminalLane(state string) bool { return state == "released" || state == "aborted" }

func readerError(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func appendDisposition(plan *Plan, kind, id, source, action, code, message string) {
	if code == "" {
		return
	}
	plan.Dispositions = append(plan.Dispositions, RecordDisposition{Kind: kind, ID: id, Source: source, Action: action, Code: code, Message: message})
}

func loadPortableRecords(repo gitx.Repo, manifest TreeManifest, now time.Time) (map[string]legacyLane, map[string]legacyLease, error) {
	lanes := map[string]legacyLane{}
	leases := map[string]legacyLease{}
	for _, entry := range manifest.Entries {
		if entry.Type != "file" {
			continue
		}
		switch {
		case strings.HasPrefix(entry.Path, "v1/lanes/"):
			if filepath.ToSlash(filepath.Dir(entry.Path)) != "v1/lanes" || !strings.HasSuffix(entry.Path, ".json") {
				return nil, nil, fmt.Errorf("portable lane entry is not a direct JSON record: %s", entry.Path)
			}
			var lane legacyLane
			if _, err := readJSONFile(filepath.Join(repo.CommonDir, "wip", filepath.FromSlash(entry.Path)), &lane); err != nil {
				return nil, nil, err
			}
			fileID := strings.TrimSuffix(filepath.Base(entry.Path), ".json")
			if lane.ID != fileID {
				return nil, nil, fmt.Errorf("portable lane file identity differs from its record: %s", entry.Path)
			}
			if err := capturestore.ValidateImportedLane(repo, lane); err != nil {
				return nil, nil, err
			}
			lanes[lane.ID] = lane
		case strings.HasPrefix(entry.Path, "v1/leases/"):
			if filepath.ToSlash(filepath.Dir(entry.Path)) != "v1/leases" || !strings.HasSuffix(entry.Path, ".json") {
				return nil, nil, fmt.Errorf("portable lease entry is not a direct JSON record: %s", entry.Path)
			}
			var lease legacyLease
			if _, err := readJSONFile(filepath.Join(repo.CommonDir, "wip", filepath.FromSlash(entry.Path)), &lease); err != nil {
				return nil, nil, err
			}
			fileID := strings.TrimSuffix(filepath.Base(entry.Path), ".json")
			if lease.ID != fileID {
				return nil, nil, fmt.Errorf("portable lease file identity differs from its record: %s", entry.Path)
			}
			if err := capturestore.ValidateImportedLease(repo, lease); err != nil {
				return nil, nil, err
			}
			leases[lease.ID] = lease
		}
	}
	for id, lease := range leases {
		lane, ok := lanes[lease.LaneID]
		if !ok || lane.Agent != lease.Agent || lane.Session != lease.Session || !containsString(lane.LeaseIDs, id) || invalidLaneLeaseState(lane.State, lease.State) {
			return nil, nil, fmt.Errorf("portable lease %s has an invalid lane relationship", id)
		}
	}
	for id, lane := range lanes {
		for _, leaseID := range lane.LeaseIDs {
			if _, ok := leases[leaseID]; !ok {
				return nil, nil, fmt.Errorf("portable lane %s references missing lease %s", id, leaseID)
			}
		}
	}
	allLeases := make([]capturestore.Lease, 0, len(leases))
	for _, lease := range leases {
		allLeases = append(allLeases, lease)
	}
	if conflict := capturestore.FindActiveLeaseConflict(allLeases, now); conflict != nil {
		return nil, nil, fmt.Errorf("portable active leases %s and %s overlap", conflict.LeftLease, conflict.RightLease)
	}
	return lanes, leases, nil
}

func compareRecord(commonDir, kind, id, source, destination, sourceDigest string) (ImportRecord, *Conflict, error) {
	operation := ImportRecord{Kind: kind, ID: id, Source: filepath.ToSlash(filepath.Join("ndev-wip", source)), Destination: destination, Action: "import", SourceSHA256: sourceDigest}
	path := filepath.Join(commonDir, filepath.FromSlash(destination))
	body, err := safeio.ReadRegular(path, maxRecordBytes)
	if errors.Is(err, os.ErrNotExist) {
		return operation, nil, nil
	}
	if err != nil {
		return operation, nil, err
	}
	operation.DestinationSHA256 = digest(body)
	if operation.DestinationSHA256 == sourceDigest {
		operation.Action = "match"
		return operation, nil, nil
	}
	operation.Action = "conflict"
	conflict := &Conflict{Code: "SHADOW_MISMATCH", Kind: kind, ID: id, Source: operation.Source, Destination: destination, Message: "the same record ID has different normalized bytes in the legacy and portable stores"}
	return operation, conflict, nil
}

func planReviewCopies(repo gitx.Repo, plan *Plan) error {
	for _, entry := range plan.SourceLegacy.Entries {
		if entry.Type != "file" || !isReviewFile(entry.Path) {
			continue
		}
		suffix := strings.TrimPrefix(entry.Path, "v1/")
		destination := filepath.ToSlash(filepath.Join("wip", "review", "v1", filepath.FromSlash(suffix)))
		operation, conflict, err := compareCopy(repo.CommonDir, "legacy-review", filepath.ToSlash(filepath.Join("ndev-wip", entry.Path)), destination, entry)
		if err != nil {
			return fail.Wrap("MIGRATION_DESTINATION_UNSAFE", err)
		}
		plan.ReviewCopies = append(plan.ReviewCopies, operation)
		if conflict != nil {
			plan.Conflicts = append(plan.Conflicts, *conflict)
		}
	}
	return nil
}

func planReconciliationCopies(repo gitx.Repo, plan *Plan) error {
	for _, entry := range plan.ReconciliationHistory.Entries {
		if entry.Type != "file" {
			continue
		}
		source := filepath.ToSlash(filepath.Join("ndev-commit-lane", "reconcile", filepath.FromSlash(entry.Path)))
		destination := filepath.ToSlash(filepath.Join("wip", "review", "v1", "reconciliation", filepath.FromSlash(entry.Path)))
		operation, conflict, err := compareCopy(repo.CommonDir, "reconciliation", source, destination, entry)
		if err != nil {
			return fail.Wrap("MIGRATION_DESTINATION_UNSAFE", err)
		}
		plan.ReconciliationCopies = append(plan.ReconciliationCopies, operation)
		if conflict != nil {
			plan.Conflicts = append(plan.Conflicts, *conflict)
		}
	}
	return nil
}

func compareCopy(commonDir, kind, source, destination string, entry ManifestEntry) (CopyRecord, *Conflict, error) {
	operation := CopyRecord{Kind: kind, Source: source, Destination: destination, Action: "copy", SourceSHA256: entry.SHA256, Size: entry.Size, Mode: entry.Mode}
	path := filepath.Join(commonDir, filepath.FromSlash(destination))
	body, err := safeio.ReadRegular(path, maxManifestBytes)
	if errors.Is(err, os.ErrNotExist) {
		return operation, nil, nil
	}
	if err != nil {
		return operation, nil, err
	}
	operation.DestinationSHA256 = digest(body)
	info, err := os.Lstat(path)
	if err != nil {
		return operation, nil, err
	}
	if operation.DestinationSHA256 == operation.SourceSHA256 && modeString(info.Mode().Perm()) == operation.Mode {
		operation.Action = "match"
		return operation, nil, nil
	}
	operation.Action = "conflict"
	conflict := &Conflict{Code: "SHADOW_MISMATCH", Kind: kind, Source: source, Destination: destination, Message: "review history destination differs from the legacy source"}
	return operation, conflict, nil
}

func isReviewFile(path string) bool {
	return path == "v1/queue.json" || strings.HasPrefix(path, "v1/landing-intents/") || strings.HasPrefix(path, "v1/archive/")
}

func DecodePlan(body []byte) (Plan, error) {
	if len(body) > MaxPlanBytes {
		return Plan{}, fmt.Errorf("plan exceeds %d bytes", MaxPlanBytes)
	}
	var plan Plan
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Plan{}, errors.New("plan contains multiple JSON values")
		}
		return Plan{}, err
	}
	return plan, nil
}
