package coordmigration

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/nstranquist/wip-commit/internal/contract"
)

var (
	captureDirectories = []string{".", "archive", "init-intents", "intents", "lanes", "leases", "locks", "profiles"}
	reviewDirectories  = []string{".", "archive", "handoffs", "landing-intents", "locks", "plans", "reconciliation"}
)

const captureRegistryLock = "locks/leases.lock"

func manifestCanonicalData(commonDir string) (CanonicalManifest, error) {
	capture, err := manifestTree(commonDir, filepath.ToSlash(filepath.Join("wip", "v1")))
	if err != nil {
		return CanonicalManifest{}, err
	}
	review, err := manifestTree(commonDir, filepath.ToSlash(filepath.Join("wip", "review", "v1")))
	if err != nil {
		return CanonicalManifest{}, err
	}
	return combineCanonical(capture, review), nil
}

func combineCanonical(capture, review TreeManifest) CanonicalManifest {
	return CanonicalManifest{
		Capture: capture,
		Review:  review,
		Digest:  digest([]byte(capture.Digest + "\x00" + review.Digest)),
	}
}

func planCanonicalData(expected CanonicalManifest, plan Plan) (CanonicalManifest, error) {
	capture, err := plannedTree(expected.Capture, captureDirectories)
	if err != nil {
		return CanonicalManifest{}, err
	}
	if err := planCaptureRegistryLock(&capture); err != nil {
		return CanonicalManifest{}, err
	}
	review, err := plannedTree(expected.Review, reviewDirectories)
	if err != nil {
		return CanonicalManifest{}, err
	}
	for _, record := range append(append([]ImportRecord{}, plan.LaneImports...), plan.LeaseImports...) {
		if record.Action != "import" && record.Action != "match" {
			continue
		}
		if err := putPlannedFile(&capture, record.Destination, "wip/v1", record.Size, record.SourceSHA256, "0600"); err != nil {
			return CanonicalManifest{}, err
		}
	}
	for _, record := range append(append([]CopyRecord{}, plan.ReviewCopies...), plan.ReconciliationCopies...) {
		if record.Action != "copy" && record.Action != "match" {
			continue
		}
		if err := putPlannedFile(&review, record.Destination, "wip/review/v1", record.Size, record.SourceSHA256, record.Mode); err != nil {
			return CanonicalManifest{}, err
		}
	}
	if err := finishManifest(&capture); err != nil {
		return CanonicalManifest{}, err
	}
	if err := finishManifest(&review); err != nil {
		return CanonicalManifest{}, err
	}
	return combineCanonical(capture, review), nil
}

// planCaptureRegistryLock adds the stable empty lock artifact when the
// canonical prestate has no registry lock. Existing lock bytes remain exact.
func planCaptureRegistryLock(manifest *TreeManifest) error {
	for _, entry := range manifest.Entries {
		if entry.Path != captureRegistryLock {
			continue
		}
		if entry.Type != "file" {
			return fmt.Errorf("canonical path %s is not a file", captureRegistryLock)
		}
		return nil
	}
	return putPlannedFile(manifest, "wip/v1/"+captureRegistryLock, "wip/v1", 0, digest(nil), "0600")
}

func plannedTree(expected TreeManifest, required []string) (TreeManifest, error) {
	result := expected
	result.Entries = append([]ManifestEntry{}, expected.Entries...)
	result.Exists = true
	entries := make(map[string]ManifestEntry, len(result.Entries)+len(required))
	for _, entry := range result.Entries {
		entries[entry.Path] = entry
	}
	for _, path := range required {
		if entry, found := entries[path]; found {
			if entry.Type != "directory" {
				return TreeManifest{}, fmt.Errorf("canonical path %s is not a directory", path)
			}
			continue
		}
		entries[path] = ManifestEntry{Path: path, Type: "directory", Mode: "0700"}
	}
	result.Entries = result.Entries[:0]
	for _, entry := range entries {
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

func putPlannedFile(manifest *TreeManifest, destination, prefix string, size int64, sha, mode string) error {
	relative, err := destinationUnder(destination, prefix)
	if err != nil {
		return err
	}
	entries := make(map[string]ManifestEntry, len(manifest.Entries)+4)
	for _, entry := range manifest.Entries {
		entries[entry.Path] = entry
	}
	for parent := filepath.ToSlash(filepath.Dir(relative)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
		if existing, found := entries[parent]; found && existing.Type != "directory" {
			return fmt.Errorf("planned parent %s is not a directory", parent)
		}
		if _, found := entries[parent]; !found {
			entries[parent] = ManifestEntry{Path: parent, Type: "directory", Mode: "0700"}
		}
	}
	entry := ManifestEntry{Path: relative, Type: "file", Mode: mode, Size: size, SHA256: sha}
	if existing, found := entries[relative]; found && existing != entry {
		return fmt.Errorf("planned destination collision at %s", destination)
	}
	entries[relative] = entry
	manifest.Entries = manifest.Entries[:0]
	for _, item := range entries {
		manifest.Entries = append(manifest.Entries, item)
	}
	return nil
}

func finishManifest(manifest *TreeManifest) error {
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].Path < manifest.Entries[j].Path })
	var err error
	manifest.Digest, err = manifestDigest(*manifest)
	return err
}

func destinationUnder(destination, prefix string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(destination)))
	prefix = filepath.ToSlash(filepath.Clean(filepath.FromSlash(prefix)))
	if clean == prefix || !lenPrefix(clean, prefix+"/") {
		return "", fmt.Errorf("destination %s is outside %s", destination, prefix)
	}
	relative := clean[len(prefix)+1:]
	if relative == "" || relative == "." || filepath.IsAbs(relative) || relative == ".." || lenPrefix(relative, "../") {
		return "", fmt.Errorf("destination %s is not a confined file path", destination)
	}
	return relative, nil
}

func lenPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}

func verifyCanonicalTransition(repoCommon string, plan Plan, journal Journal, requirePost bool) error {
	current, err := manifestCanonicalData(repoCommon)
	if err != nil {
		return failCanonical(err)
	}
	if requirePost {
		if current.Digest != plan.PlannedCanonicalData.Digest {
			return failCanonical(errors.New("canonical data does not match the reviewed post-publication manifest"))
		}
	} else {
		authorized := publishedPathSet(journal)
		if err := verifyTreeTransition(plan.ExpectedCanonicalData.Capture, plan.PlannedCanonicalData.Capture, current.Capture, "wip/v1", authorized); err != nil {
			return failCanonical(err)
		}
		if err := verifyTreeTransition(plan.ExpectedCanonicalData.Review, plan.PlannedCanonicalData.Review, current.Review, "wip/review/v1", authorized); err != nil {
			return failCanonical(err)
		}
	}
	if err := verifyCanonicalEnvelope(repoCommon, plan, journal); err != nil {
		return failCanonical(err)
	}
	return nil
}

func publishedPathSet(journal Journal) map[string]bool {
	authorized := make(map[string]bool, len(journal.PublishedPaths))
	for _, path := range journal.PublishedPaths {
		authorized[filepath.ToSlash(path)] = true
	}
	return authorized
}

// verifyCanonicalEnvelope protects everything under .git/wip, not only the
// two writable data roots. A resume accepts the exact reviewed prestate plus
// journal-authorized outputs whose bytes and modes are known from the plan.
// This rejects an extra file even when a stopped writer adds it outside v1.
func verifyCanonicalEnvelope(commonDir string, plan Plan, journal Journal) error {
	current, err := manifestTree(commonDir, "wip")
	if err != nil {
		return err
	}
	before := fullManifestEntries(plan.ExpectedCanonical, "wip")
	got := fullManifestEntries(current, "wip")
	allowed, err := migrationOutputEntries(commonDir, plan, current)
	if err != nil {
		return err
	}
	authorized := publishedPathSet(journal)
	for path := range authorized {
		if _, ok := allowed[path]; !ok {
			return fmt.Errorf("journal authorizes an output that is not in the reviewed migration: %s", path)
		}
	}
	for path, entry := range got {
		if expected, ok := before[path]; ok && equivalentManifestEntry(expected, entry) {
			continue
		}
		// The exact data-root transition was verified separately above. Keep
		// the envelope check responsible for every parent and sibling path.
		if canonicalDataPath(path) {
			continue
		}
		variants, ok := allowed[path]
		if !ok || !authorized[path] {
			return fmt.Errorf("canonical path changed without journal authority: %s", path)
		}
		matched := false
		for _, expected := range variants {
			if equivalentManifestEntry(expected, entry) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("canonical path does not match an exact migration output: %s", path)
		}
	}
	for path, expected := range before {
		if actual, ok := got[path]; !ok || !equivalentManifestEntry(expected, actual) && !canonicalDataPath(path) {
			return fmt.Errorf("canonical prestate path disappeared or changed: %s", path)
		}
	}
	return nil
}

func migrationOutputEntries(commonDir string, plan Plan, current TreeManifest) (map[string][]ManifestEntry, error) {
	allowed := make(map[string][]ManifestEntry)
	addDirectory := func(path string, mode string) {
		allowed[path] = append(allowed[path], ManifestEntry{Type: "directory", Mode: mode})
	}
	for _, path := range []string{
		"wip", "wip/v1", "wip/v1/archive", "wip/v1/init-intents", "wip/v1/intents", "wip/v1/lanes", "wip/v1/leases", "wip/v1/locks", "wip/v1/profiles",
		"wip/review", "wip/review/v1", "wip/review/v1/archive", "wip/review/v1/handoffs", "wip/review/v1/landing-intents", "wip/review/v1/locks", "wip/review/v1/plans", "wip/review/v1/reconciliation",
		"wip/archive", "wip/archive/legacy-ndev-wip",
		"wip/migration", "wip/migration/v1", filepath.ToSlash(filepath.Join("wip", "migration", "v1", plan.MigrationID)),
	} {
		addDirectory(path, "0700")
	}
	addManifestOutputs(allowed, plan.PlannedCanonicalData.Capture, "wip/v1")
	addManifestOutputs(allowed, plan.PlannedCanonicalData.Review, "wip/review/v1")

	domain := domainRecord{SchemaVersion: 2, Domain: DomainID, StateVersion: 1, MinimumReaderProtocol: contract.Protocol, RequiredCapabilities: contract.StateReaderCapabilities()}
	domainBody, err := normalizedJSON(domain)
	if err != nil {
		return nil, err
	}
	addFileOutput(allowed, "wip/domain.json", domainBody, "0600")
	planBody, err := normalizedJSON(plan)
	if err != nil {
		return nil, err
	}
	planPath := filepath.ToSlash(filepath.Join("wip", "migration", "v1", plan.MigrationID, "plan.json"))
	addFileOutput(allowed, planPath, planBody, "0400")

	if plan.SourceLegacy.Exists {
		archiveBase := filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID))
		addDirectory("wip/archive", "0700")
		addDirectory("wip/archive/legacy-ndev-wip", "0700")
		addDirectory(archiveBase, "0700")
		for _, entry := range plan.SourceLegacy.Entries {
			path := archiveBase + "/source"
			if entry.Path != "." {
				path += "/" + entry.Path
			}
			allowed[path] = append(allowed[path], entry)
			sealed := entry
			if entry.Type == "directory" {
				sealed.Mode = "0500"
			} else {
				sealed.Mode = "0400"
			}
			allowed[path] = append(allowed[path], sealed)
		}
		// The seal is a valid journal output, but its exact bytes are known
		// only after the archive is complete. Keep an explicit empty variant
		// set until a present seal is validated below.
		allowed[archiveBase+"/seal.json"] = nil
	}
	receiptPath := migrationReceiptRelativePath(plan.MigrationID)
	rollbackPath := filepath.ToSlash(filepath.Join("wip", "migration", "v1", plan.MigrationID, "rollback.json"))
	allowed[receiptPath] = nil
	allowed[rollbackPath] = nil
	currentEntries := fullManifestEntries(current, "wip")
	for _, dynamic := range []struct {
		path     string
		validate func() error
	}{
		{filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID, "seal.json")), func() error { return validateTransitionSeal(commonDir, plan) }},
		{receiptPath, func() error { return validateTransitionReceipt(commonDir, plan) }},
		{rollbackPath, func() error { return validateTransitionRollback(commonDir, plan) }},
	} {
		if entry, exists := currentEntries[dynamic.path]; exists {
			if err := dynamic.validate(); err != nil {
				return nil, err
			}
			allowed[dynamic.path] = append(allowed[dynamic.path], entry)
		}
	}
	return allowed, nil
}

func addManifestOutputs(allowed map[string][]ManifestEntry, manifest TreeManifest, prefix string) {
	for _, entry := range manifest.Entries {
		path := prefix
		if entry.Path != "." {
			path += "/" + entry.Path
		}
		allowed[path] = append(allowed[path], entry)
	}
}

func addFileOutput(allowed map[string][]ManifestEntry, path string, body []byte, mode string) {
	allowed[path] = append(allowed[path], ManifestEntry{Type: "file", Mode: mode, Size: int64(len(body)), SHA256: digest(body)})
}

func fullManifestEntries(manifest TreeManifest, prefix string) map[string]ManifestEntry {
	result := make(map[string]ManifestEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		path := prefix
		if entry.Path != "." {
			path += "/" + entry.Path
		}
		result[path] = entry
	}
	return result
}

func equivalentManifestEntry(left, right ManifestEntry) bool {
	return left.Type == right.Type && left.Mode == right.Mode && left.Size == right.Size && left.SHA256 == right.SHA256
}

func canonicalDataPath(path string) bool {
	return path == "wip/v1" || lenPrefix(path, "wip/v1/") || path == "wip/review/v1" || lenPrefix(path, "wip/review/v1/")
}

func migrationReceiptRelativePath(migrationID string) string {
	return filepath.ToSlash(filepath.Join("wip", "migration", "v1", migrationID, "receipt.json"))
}

func validateTransitionSeal(commonDir string, plan Plan) error {
	path := filepath.Join(archiveMigrationRoot(commonDir, plan.MigrationID), "seal.json")
	var seal Seal
	if _, err := readJSONFile(path, &seal); err != nil {
		return err
	}
	archive, err := manifestTree(commonDir, filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID, "source")))
	if err != nil {
		return err
	}
	if seal.SchemaVersion != SealSchemaVersion || seal.MigrationID != plan.MigrationID || seal.PlanDigest != plan.PlanDigest || seal.SourceManifest != plan.SourceLegacy.Digest || seal.ArchiveManifest != archive.Digest || seal.ArchiveRelativeDir != filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID, "source")) || seal.SealedAt.IsZero() {
		return errors.New("archive seal is not an exact output of the reviewed migration")
	}
	return nil
}

func validateTransitionReceipt(commonDir string, plan Plan) error {
	var receipt Receipt
	if _, err := readJSONFile(migrationReceiptPath(commonDir, plan.MigrationID), &receipt); err != nil {
		return err
	}
	if err := validateReceipt(receipt, plan.MigrationID); err != nil {
		return err
	}
	if receipt.PlanDigest != plan.PlanDigest || receipt.SourceManifest != plan.SourceLegacy.Digest || receipt.CanonicalManifest != plan.PlannedCanonicalData.Digest || receipt.ReconciliationHistory != plan.ReconciliationHistory.Digest || receipt.CompletedAt.IsZero() {
		return errors.New("completion receipt is not an exact output of the reviewed migration")
	}
	if plan.SourceLegacy.Exists {
		archive, err := manifestTree(commonDir, filepath.ToSlash(filepath.Join("wip", "archive", "legacy-ndev-wip", plan.MigrationID, "source")))
		if err != nil || receipt.ArchiveManifest != archive.Digest {
			return errors.New("completion receipt does not bind the exact legacy archive")
		}
	} else if receipt.ArchiveManifest != "" {
		return errors.New("completion receipt unexpectedly names a legacy archive")
	}
	return nil
}

func validateTransitionRollback(commonDir string, plan Plan) error {
	path := filepath.Join(commonDir, "wip", "migration", "v1", plan.MigrationID, "rollback.json")
	var marker RollbackMarker
	if _, err := readJSONFile(path, &marker); err != nil {
		return err
	}
	if marker.SchemaVersion != RollbackSchemaVersion || marker.MigrationID != plan.MigrationID || marker.PlanDigest != plan.PlanDigest || marker.State != "rolled-back" || marker.UpdatedAt.IsZero() {
		return errors.New("rollback marker is not an exact output of the reviewed migration")
	}
	return nil
}

func verifyTreeTransition(expected, planned, current TreeManifest, prefix string, authorized map[string]bool) error {
	wantPre := entriesByPath(expected.Entries)
	wantPost := entriesByPath(planned.Entries)
	got := entriesByPath(current.Entries)
	for path, actual := range got {
		if before, ok := wantPre[path]; ok && before == actual {
			continue
		}
		full := filepath.ToSlash(filepath.Join(prefix, filepath.FromSlash(path)))
		if !authorized[full] {
			return fmt.Errorf("canonical path changed without journal authority: %s", full)
		}
		if after, ok := wantPost[path]; !ok || after != actual {
			return fmt.Errorf("canonical path does not match its planned output: %s", full)
		}
	}
	for path, before := range wantPre {
		if _, ok := got[path]; ok {
			continue
		}
		full := filepath.ToSlash(filepath.Join(prefix, filepath.FromSlash(path)))
		if !authorized[full] || wantPost[path] == before {
			return fmt.Errorf("canonical prestate path disappeared: %s", full)
		}
	}
	return nil
}

func entriesByPath(entries []ManifestEntry) map[string]ManifestEntry {
	result := make(map[string]ManifestEntry, len(entries))
	for _, entry := range entries {
		result[entry.Path] = entry
	}
	return result
}

func failCanonical(err error) error {
	return fmt.Errorf("canonical manifest verification failed: %w", err)
}

func verifyExactCanonicalPrestate(commonDir string, plan Plan) error {
	current, err := manifestTree(commonDir, "wip")
	if err != nil {
		return err
	}
	if current.Digest != plan.ExpectedCanonical.Digest {
		return errors.New("canonical store changed after the reviewed preview")
	}
	return nil
}
