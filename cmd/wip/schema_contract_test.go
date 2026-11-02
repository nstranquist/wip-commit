package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCommandOutputsMatchPublishedJSONSchemas(t *testing.T) {
	directory := cliTestRepo(t)
	writeCLI(t, directory, "core.txt", "new core\n")
	cliGit(t, directory, "add", "core.txt")
	initializeCLI(t, directory, "schema-contract", "core.txt")

	planPath := filepath.Join(t.TempDir(), "plan.json")
	drafted := runCLI(t, []string{"--json", "--repo-dir", directory, "plan", "--lane", "schema-contract", "--out", planPath}, "")
	if drafted.code != 0 {
		t.Fatalf("draft plan = %#v stderr=%s", drafted.envelope, drafted.stderr)
	}
	validateSchemaValue(t, "PLAN.schema.json", drafted.envelope.Data)
	var reviewed planProposal
	decodeData(t, drafted.envelope.Data, &reviewed)
	reviewed.State = "reviewed"
	for index := range reviewed.Groups {
		reviewed.Groups[index].Message = "fix(schema): validate published command contracts"
	}
	validateSchemaValue(t, "PLAN.schema.json", reviewed)
	body, err := json.MarshalIndent(reviewed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	status := runCLI(t, []string{"--json", "--repo-dir", directory, "status", "--lane", "schema-contract", "--observe=true"}, "")
	if status.code != 0 {
		t.Fatalf("status = %#v stderr=%s", status.envelope, status.stderr)
	}
	validateSchemaValue(t, "STATUS.schema.json", status.envelope.Data)

	captured := runCLI(t, []string{"--json", "--repo-dir", directory, "capture", "--lane", "schema-contract", "--plan", planPath}, "")
	if captured.code != 0 {
		t.Fatalf("capture = %#v stderr=%s", captured.envelope, captured.stderr)
	}
	var result guidedCaptureResult
	decodeData(t, captured.envelope.Data, &result)
	validateSchemaValue(t, "HANDOFF.schema.json", result.Handoff)

	legacy := []byte(`[{"message":"fix(schema): validate legacy plan","files":["core.txt"]}]`)
	validateSchemaJSON(t, "PLAN.legacy-v1.schema.json", legacy)

	invalid, err := json.Marshal(map[string]any{
		"schema_version": "1.0.0",
		"unexpected":     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := schemaValidationError(t, "HANDOFF.schema.json", invalid); err == nil {
		t.Fatal("handoff schema accepted an incomplete object with an unknown field")
	}
}

func validateSchemaValue(t *testing.T, schemaName string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	validateSchemaJSON(t, schemaName, body)
}

func validateSchemaJSON(t *testing.T, schemaName string, body []byte) {
	t.Helper()
	if err := schemaValidationError(t, schemaName, body); err != nil {
		t.Fatalf("%s rejected command data: %v\n%s", schemaName, err, body)
	}
}

func schemaValidationError(t *testing.T, schemaName string, body []byte) error {
	t.Helper()
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode schema instance: %v", err)
	}
	schemaPath, err := filepath.Abs(filepath.Join("..", "..", "docs", schemaName))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	schema, err := compiler.Compile(schemaPath)
	if err != nil {
		t.Fatalf("compile %s: %v", schemaName, err)
	}
	return schema.Validate(instance)
}
