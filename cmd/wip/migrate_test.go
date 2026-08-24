package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nstranquist/wip-commit/internal/coordmigration"
)

func TestExperimentalMigrationCommandPreviewApplyAndStatus(t *testing.T) {
	directory := cliTestRepo(t)
	common := cliGit(t, directory, "rev-parse", "--path-format=absolute", "--git-common-dir")
	before := commandTreeDigest(t, common)
	preview := runMigrationCLI(t, []string{"--json", "--repo-dir", directory, "migrate", "--preview"}, "")
	if preview.code != 0 || !preview.envelope.OK || preview.envelope.Action != "migrate" {
		t.Fatalf("preview = %#v stderr=%s", preview.envelope, preview.stderr)
	}
	if after := commandTreeDigest(t, common); after != before {
		t.Fatalf("read-only command preview changed the common directory: before=%s after=%s", before, after)
	}
	var previewData migrationCommandResult
	decodeData(t, preview.envelope.Data, &previewData)
	if previewData.Mode != "preview" || previewData.Plan == nil || previewData.Plan.State != "ready" {
		t.Fatalf("preview data = %#v", previewData)
	}
	planBody, err := json.Marshal(previewData.Plan)
	if err != nil {
		t.Fatal(err)
	}
	applied := runMigrationCLI(t, []string{"--json", "--repo-dir", directory, "migrate", "--apply", "--plan", "-", "--plan-digest", previewData.Plan.PlanDigest, "--yes"}, string(planBody))
	if applied.code != 0 || !applied.envelope.OK {
		t.Fatalf("apply = %#v stderr=%s", applied.envelope, applied.stderr)
	}
	var applyData migrationCommandResult
	decodeData(t, applied.envelope.Data, &applyData)
	if !applyData.Applied || applyData.Status == nil || applyData.Status.State != "complete" {
		t.Fatalf("apply data = %#v", applyData)
	}
	statusBefore := commandTreeDigest(t, common)
	status := runMigrationCLI(t, []string{"--json", "--repo-dir", directory, "migrate", "--status", "--migration-id", previewData.Plan.MigrationID}, "")
	if status.code != 0 || !status.envelope.OK {
		t.Fatalf("status = %#v stderr=%s", status.envelope, status.stderr)
	}
	if statusAfter := commandTreeDigest(t, common); statusAfter != statusBefore {
		t.Fatalf("read-only command status changed the common directory: before=%s after=%s", statusBefore, statusAfter)
	}
	var statusData migrationCommandResult
	decodeData(t, status.envelope.Data, &statusData)
	if statusData.Status == nil || statusData.Status.Receipt == nil || statusData.Status.State != "complete" {
		t.Fatalf("status data = %#v", statusData)
	}
	doctor := runCLI(t, []string{"--json", "--repo-dir", directory, "doctor"}, "")
	doctorData, err := json.Marshal(doctor.envelope.Data)
	if err != nil {
		t.Fatal(err)
	}
	if doctor.code != 0 || !doctor.envelope.OK || !strings.Contains(string(doctorData), `"healthy":true`) {
		t.Fatalf("post-apply canonical doctor = %#v stderr=%s", doctor.envelope, doctor.stderr)
	}
}

func TestMigrationCommandRequiresExplicitModeAndReviewedDigest(t *testing.T) {
	directory := cliTestRepo(t)
	missingMode := runMigrationCLI(t, []string{"--json", "--repo-dir", directory, "migrate"}, "")
	if missingMode.code != 2 || missingMode.envelope.Error == nil || missingMode.envelope.Error.Code != "INVALID_ARGS" {
		t.Fatalf("missing mode = %#v", missingMode)
	}
	preview := runMigrationCLI(t, []string{"--json", "--repo-dir", directory, "migrate", "--preview"}, "")
	var data migrationCommandResult
	decodeData(t, preview.envelope.Data, &data)
	body, _ := json.Marshal(data.Plan)
	before := commandTreeDigest(t, cliGit(t, directory, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	wrong := runMigrationCLI(t, []string{"--json", "--repo-dir", directory, "migrate", "--apply", "--plan", "-", "--plan-digest", "sha256:" + strings.Repeat("0", 64), "--yes"}, string(body))
	if wrong.code != 1 || wrong.envelope.Error == nil || wrong.envelope.Error.Code != "MIGRATION_PLAN_DIGEST_MISMATCH" {
		t.Fatalf("wrong digest = %#v", wrong)
	}
	if after := commandTreeDigest(t, cliGit(t, directory, "rev-parse", "--path-format=absolute", "--git-common-dir")); after != before {
		t.Fatalf("digest refusal changed common state: before=%s after=%s", before, after)
	}
}

func runMigrationCLI(t *testing.T, args []string, input string) cliRun {
	t.Helper()
	for _, name := range []string{"WIP_LANE", "WIP_AGENT", "WIP_SESSION"} {
		t.Setenv(name, "")
	}
	contractResult, err := currentContractResult()
	if err != nil {
		t.Fatal(err)
	}
	producer := coordmigration.ProducerIdentity{
		CommandProtocol: contractResult.Protocol, Product: "github.com/nstranquist/wip-commit",
		Version: "0.2.0-beta.1", SourceRevision: strings.Repeat("a", 40), SourceState: "modified",
		PortableBundleSHA256: contractResult.PortableBundleSHA256,
		ExecutableSHA256:     "sha256:" + strings.Repeat("b", 64),
	}
	var stdout, stderr bytes.Buffer
	application := app{
		stdin: strings.NewReader(input), stdout: &stdout, stderr: &stderr,
		migrationProducer: func() (coordmigration.ProducerIdentity, error) { return producer, nil },
	}
	code := application.run(context.Background(), args)
	result := cliRun{code: code, stdout: stdout.String(), stderr: stderr.String()}
	if strings.Contains(strings.Join(args, " "), "--json") {
		if err := json.Unmarshal(stdout.Bytes(), &result.envelope); err != nil {
			t.Fatalf("decode JSON output %q: %v; stderr=%s", stdout.String(), err, stderr.String())
		}
	}
	return result
}

func commandTreeDigest(t *testing.T, root string) string {
	t.Helper()
	var records []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		record := fmt.Sprintf("%s\x00%s\x00%04o", filepath.ToSlash(relative), info.Mode().Type(), info.Mode().Perm())
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			record += "\x00" + hex.EncodeToString(sum[:])
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(records)
	sum := sha256.Sum256([]byte(strings.Join(records, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}
