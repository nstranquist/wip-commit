package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nstranquist/wip-commit/internal/fail"
)

func TestDomainUpgradeIsExactAndIdempotent(t *testing.T) {
	repo := testRepo(t)
	if _, err := Open(repo); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repo.CommonDir, "wip", "domain.json")
	legacy, err := json.MarshalIndent(domainRecord{SchemaVersion: legacyDomainSchemaVersion, Domain: domainID, StateVersion: SchemaVersion}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, append(legacy, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := InspectDomainUpgrade(repo)
	if err != nil || !plan.UpgradeRequired || plan.AlreadyCurrent || plan.CurrentSHA256 == plan.DesiredSHA256 {
		t.Fatalf("legacy plan = %#v err=%v", plan, err)
	}
	if _, err := ApplyDomainUpgrade(repo, "sha256:"+string(make([]byte, 64))); fail.Code(err) != "UPGRADE_PLAN_MOVED" {
		t.Fatalf("moved plan error = %v (%s)", err, fail.Code(err))
	}
	applied, err := ApplyDomainUpgrade(repo, plan.CurrentSHA256)
	if err != nil || !applied.AlreadyCurrent || applied.CurrentSHA256 != plan.DesiredSHA256 {
		t.Fatalf("applied plan = %#v err=%v", applied, err)
	}
	opened, err := Open(repo)
	if err != nil || opened.Domain.UpgradeRequired || opened.Domain.SchemaVersion != domainSchemaVersion {
		t.Fatalf("upgraded store = %#v err=%v", opened.Domain, err)
	}
	if replay, err := ApplyDomainUpgrade(repo, plan.DesiredSHA256); err != nil || !replay.AlreadyCurrent {
		t.Fatalf("idempotent apply = %#v err=%v", replay, err)
	}
}

func TestDomainUpgradeRequiresExistingMarker(t *testing.T) {
	repo := testRepo(t)
	if _, err := InspectDomainUpgrade(repo); fail.Code(err) != "STATE_NOT_INITIALIZED" {
		t.Fatalf("missing marker error = %v (%s)", err, fail.Code(err))
	}
}
