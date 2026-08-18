package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nstranquist/wip-commit/internal/fail"
)

func TestNamedMessagePolicies(t *testing.T) {
	conventional := Default()
	if err := Validate(conventional, "fix(core): preserve exact capture", false); err != nil {
		t.Fatal(err)
	}
	if err := Validate(conventional, "plain local checkpoint", false); fail.Code(err) != "MESSAGE_POLICY_REJECTED" {
		t.Fatalf("conventional rejection = %v (%s)", err, fail.Code(err))
	}
	safety, err := Resolve(SafetyOnlyV1)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(safety, "plain local checkpoint", false); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMessagePolicyIsStrictAndDefaults(t *testing.T) {
	directory := t.TempDir()
	if got, err := Load(directory, ""); err != nil || got != Default() {
		t.Fatalf("default = %#v, %v", got, err)
	}
	path := filepath.Join(directory, ConfigName)
	if err := os.WriteFile(path, []byte(`{"schema_version":"1.0.0","message_policy":"safety-only-v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(directory, "")
	if err != nil || got.ID != SafetyOnlyV1 {
		t.Fatalf("configured = %#v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":"1.0.0","message_policy":"safety-only-v1","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(directory, ""); fail.Code(err) != "MESSAGE_POLICY_READ_FAILED" {
		t.Fatalf("strict error = %v (%s)", err, fail.Code(err))
	}
}
