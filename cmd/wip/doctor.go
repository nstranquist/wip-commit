package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/engine"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/store"
)

const maxDoctorEntries = 10_000

type doctorFinding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

type doctorReport struct {
	SchemaVersion     string          `json:"schema_version"`
	State             string          `json:"state"`
	Healthy           bool            `json:"healthy"`
	StateRoot         string          `json:"state_root"`
	Domain            string          `json:"domain"`
	StateVersion      int             `json:"state_version"`
	Counts            map[string]int  `json:"counts"`
	ActiveLanes       []string        `json:"active_lanes"`
	ExpiredLanes      []string        `json:"expired_lease_lanes"`
	PendingInit       []string        `json:"pending_init_intents"`
	PendingCapture    []string        `json:"pending_capture_intents"`
	PendingArchive    []string        `json:"pending_archive_receipts"`
	ArchiveCandidates []string        `json:"archive_candidates"`
	Findings          []doctorFinding `json:"findings"`
}

func (application app) runDoctor(ctx context.Context, repo gitx.Repo, args []string) int {
	set := application.flagSet("doctor")
	if err := set.Parse(args); err != nil {
		return application.failure("doctor", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("doctor", err, nil, 2)
	}
	laneStore, initialized, err := store.Inspect(repo)
	if err != nil {
		return application.failure("doctor", err, nil, 1)
	}
	report := doctorReport{
		SchemaVersion: "1.0.0", State: "not-initialized", Healthy: true,
		StateRoot: laneStore.Root, Domain: "github.com/nstranquist/wip-commit", StateVersion: store.SchemaVersion,
		Counts: map[string]int{}, ActiveLanes: []string{}, ExpiredLanes: []string{}, PendingInit: []string{}, PendingCapture: []string{}, PendingArchive: []string{}, ArchiveCandidates: []string{}, Findings: []doctorFinding{},
	}
	if initialized {
		report.State = "initialized"
		auditDoctor(ctx, laneStore, &report)
	}
	sort.Strings(report.ActiveLanes)
	sort.Strings(report.ExpiredLanes)
	sort.Strings(report.PendingInit)
	sort.Strings(report.PendingCapture)
	sort.Strings(report.PendingArchive)
	sort.Strings(report.ArchiveCandidates)
	sort.Slice(report.Findings, func(left, right int) bool {
		if report.Findings[left].Severity != report.Findings[right].Severity {
			return report.Findings[left].Severity < report.Findings[right].Severity
		}
		if report.Findings[left].Path != report.Findings[right].Path {
			return report.Findings[left].Path < report.Findings[right].Path
		}
		return report.Findings[left].Code < report.Findings[right].Code
	})
	for _, finding := range report.Findings {
		if finding.Severity == "error" || finding.Severity == "recovery" {
			report.Healthy = false
		}
	}
	human := formatDoctorReport(report)
	code := application.success("doctor", report, human)
	if !report.Healthy {
		return 1
	}
	return code
}

func auditDoctor(ctx context.Context, laneStore store.Store, report *doctorReport) {
	laneIDs := doctorRecordIDs(laneStore.Root, "lanes", report)
	leasing := map[string][]store.Lease{}
	var allLeases []store.Lease
	lanes := map[string]store.Lane{}
	for _, id := range laneIDs {
		lane, err := laneStore.Load(id)
		if err != nil {
			addDoctorError(report, filepath.Join("lanes", id+".json"), err)
			continue
		}
		lanes[id] = lane
		report.Counts["lanes"]++
		switch lane.State {
		case "active":
			report.ActiveLanes = append(report.ActiveLanes, id)
		case "creating":
			report.Findings = append(report.Findings, doctorFinding{Severity: "recovery", Code: "LANE_CREATE_RECOVERY_REQUIRED", Path: filepath.Join("lanes", id+".json"), Message: "lane creation is incomplete; rerun the exact lane creation or initialization"})
		case "released", "aborted":
			report.ArchiveCandidates = append(report.ArchiveCandidates, id)
		}
		actual, refErr := laneStore.Repo.Text(ctx, nil, "rev-parse", "--verify", lane.Ref+"^{commit}")
		if refErr != nil || actual != lane.CurrentSHA {
			report.Findings = append(report.Findings, doctorFinding{Severity: "error", Code: "LANE_REF_MISMATCH", Path: lane.Ref, Message: "lane ref is missing or does not match the durable cursor"})
		}
	}
	leaseIDs := doctorRecordIDs(laneStore.Root, "leases", report)
	now := time.Now().UTC()
	for _, id := range leaseIDs {
		lease, err := laneStore.LoadLease(id)
		if err != nil {
			addDoctorError(report, filepath.Join("leases", id+".json"), err)
			continue
		}
		report.Counts["leases"]++
		allLeases = append(allLeases, lease)
		leasing[lease.LaneID] = append(leasing[lease.LaneID], lease)
		lane, ok := lanes[lease.LaneID]
		if !ok {
			report.Findings = append(report.Findings, doctorFinding{Severity: "error", Code: "ORPHAN_LEASE", Path: filepath.Join("leases", id+".json"), Message: "lease references a missing or invalid lane"})
		} else {
			if lease.Agent != lane.Agent || lease.Session != lane.Session {
				report.Findings = append(report.Findings, doctorFinding{Severity: "error", Code: "LEASE_OWNER_MISMATCH", Path: filepath.Join("leases", id+".json"), Message: "lease owner does not match its lane"})
			}
			if !stringContains(lane.LeaseIDs, lease.ID) {
				report.Findings = append(report.Findings, doctorFinding{Severity: "error", Code: "LEASE_REFERENCE_UNLISTED", Path: filepath.Join("leases", id+".json"), Message: "lease is not listed by its lane"})
			}
			if lease.State == "active" && (lane.State == "released" || lane.State == "aborted") {
				report.Findings = append(report.Findings, doctorFinding{Severity: "error", Code: "RELEASED_LANE_ACTIVE_LEASE", Path: filepath.Join("leases", id+".json"), Message: "released or aborted lane still has an active stored lease"})
			}
			if lease.State == "released" && (lane.State == "active" || lane.State == "creating") {
				report.Findings = append(report.Findings, doctorFinding{Severity: "recovery", Code: "LANE_RELEASE_RECOVERY_REQUIRED", Path: filepath.Join("leases", id+".json"), Message: "active or creating lane has a released lease; inspect and finish release or claim a new exact lease"})
			}
		}
		if lease.State == "active" && lease.ExpiresAt != nil && !now.Before(*lease.ExpiresAt) {
			report.ExpiredLanes = append(report.ExpiredLanes, lease.LaneID)
			report.Findings = append(report.Findings, doctorFinding{Severity: "warning", Code: "LEASE_EXPIRED", Path: filepath.Join("leases", id+".json"), Message: "stored active lease has expired and must be claimed again"})
		}
	}
	if conflict := store.FindActiveLeaseConflict(allLeases, now); conflict != nil {
		report.Findings = append(report.Findings, doctorFinding{Severity: "error", Code: "PATH_LEASE_CONFLICT", Path: filepath.Join("leases", conflict.RightLease+".json"), Message: fmt.Sprintf("active path %q in lane %s overlaps path %q in lane %s", conflict.RightPath, conflict.RightLane, conflict.LeftPath, conflict.LeftLane)})
	}
	for id, lane := range lanes {
		known := map[string]bool{}
		for _, lease := range leasing[id] {
			known[lease.ID] = true
		}
		for _, leaseID := range lane.LeaseIDs {
			if !known[leaseID] {
				report.Findings = append(report.Findings, doctorFinding{Severity: "error", Code: "LEASE_REFERENCE_MISSING", Path: filepath.Join("lanes", id+".json"), Message: "lane references missing lease " + leaseID})
			}
		}
	}
	for _, id := range doctorRecordIDs(laneStore.Root, "profiles", report) {
		if _, err := loadProfile(laneStore, id); err != nil {
			addDoctorError(report, filepath.Join("profiles", id+".json"), err)
			continue
		}
		report.Counts["profiles"]++
	}
	for _, id := range doctorRecordIDs(laneStore.Root, "intents", report) {
		intent, _, err := engine.LoadIntent(laneStore.Repo, id, "")
		if err != nil {
			addDoctorError(report, filepath.Join("intents", id+".json"), err)
			continue
		}
		report.Counts["capture_intents"]++
		if intent.State != "complete" {
			report.PendingCapture = append(report.PendingCapture, id)
			report.Findings = append(report.Findings, doctorFinding{Severity: "recovery", Code: "CAPTURE_RECONCILE_REQUIRED", Path: filepath.Join("intents", id+".json"), Message: "capture intent is " + intent.State + "; inspect the ref and reconcile with its exact digest"})
		}
	}
	for _, id := range doctorOptionalRecordIDs(laneStore.Root, "init-intents", report) {
		intent, err := loadInitIntent(filepath.Join(laneStore.Root, "init-intents", id+".json"))
		if err != nil {
			addDoctorError(report, filepath.Join("init-intents", id+".json"), err)
			continue
		}
		report.Counts["init_intents"]++
		if intent.State != "complete" {
			report.PendingInit = append(report.PendingInit, id)
			report.Findings = append(report.Findings, doctorFinding{Severity: "recovery", Code: "INIT_RESUME_REQUIRED", Path: filepath.Join("init-intents", id+".json"), Message: "initialization is pending; rerun the exact setup recorded by this intent"})
		}
	}
	archivePath := filepath.Join(laneStore.Root, "archive")
	if entries, err := doctorDirectoryEntries(archivePath); err == nil {
		for _, entry := range entries {
			path := filepath.Join("archive", entry.Name())
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				report.Findings = append(report.Findings, doctorFinding{Severity: "error", Code: "UNEXPECTED_STATE_ENTRY", Path: path, Message: "archive batch is not a regular directory"})
				continue
			}
			if err := store.ValidateID(entry.Name(), "archive"); err != nil || !strings.HasPrefix(entry.Name(), "archive-") {
				if err == nil {
					err = fail.New("INVALID_ID", "archive directory does not use the archive- prefix")
				}
				addDoctorError(report, path, err)
				continue
			}
			receipt, err := laneStore.LoadArchive(entry.Name())
			if err != nil {
				addDoctorError(report, filepath.Join(path, "receipt.json"), err)
				continue
			}
			if err := laneStore.ValidateArchiveFiles(receipt); err != nil {
				addDoctorError(report, path, err)
				continue
			}
			report.Counts["archive_batches"]++
			if receipt.State == "prepared" || receipt.State == "restoring" {
				report.PendingArchive = append(report.PendingArchive, receipt.ID)
				message := "archive receipt is prepared; resume or restore this exact receipt"
				if receipt.State == "restoring" {
					message = "archive receipt is restoring; rerun restore for this exact receipt"
				}
				report.Findings = append(report.Findings, doctorFinding{Severity: "recovery", Code: "ARCHIVE_RECOVERY_REQUIRED", Path: filepath.Join(path, "receipt.json"), Message: message})
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		addDoctorError(report, "archive", err)
	}
}

func stringContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func doctorOptionalRecordIDs(root, directory string, report *doctorReport) []string {
	if _, err := os.Lstat(filepath.Join(root, directory)); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return doctorRecordIDs(root, directory, report)
}

func doctorRecordIDs(root, directory string, report *doctorReport) []string {
	entries, err := doctorDirectoryEntries(filepath.Join(root, directory))
	if err != nil {
		addDoctorError(report, directory, err)
		return nil
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".json") {
			report.Findings = append(report.Findings, doctorFinding{Severity: "error", Code: "UNEXPECTED_STATE_ENTRY", Path: filepath.Join(directory, entry.Name()), Message: "state record is not a regular JSON file"})
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if err := store.ValidateID(id, directory+" record"); err != nil {
			addDoctorError(report, filepath.Join(directory, entry.Name()), err)
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func doctorDirectoryEntries(path string) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is not a regular directory", path)
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(maxDoctorEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxDoctorEntries {
		return nil, fmt.Errorf("%s exceeds %d entries", path, maxDoctorEntries)
	}
	return entries, nil
}

func addDoctorError(report *doctorReport, path string, err error) {
	code := fail.Code(err)
	if code == "INTERNAL_ERROR" {
		code = "STORE_FAILED"
	}
	report.Findings = append(report.Findings, doctorFinding{Severity: "error", Code: code, Path: path, Message: err.Error()})
}

func formatDoctorReport(report doctorReport) string {
	status := "healthy"
	if !report.Healthy {
		status = "needs attention"
	}
	lines := []string{fmt.Sprintf("wip state: %s (%s)", report.State, status), "state root: " + report.StateRoot}
	keys := make([]string, 0, len(report.Counts))
	for key := range report.Counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s: %d", strings.ReplaceAll(key, "_", " "), report.Counts[key]))
	}
	for _, finding := range report.Findings {
		location := finding.Path
		if location != "" {
			location = " " + location
		}
		lines = append(lines, fmt.Sprintf("%s %s%s: %s", finding.Severity, finding.Code, location, finding.Message))
	}
	return strings.Join(lines, "\n")
}
