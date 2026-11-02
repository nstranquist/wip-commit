package wipcommit

import (
	"encoding/json"
	"os"
	"testing"
)

func TestCaptureSchemasAreStrictJSONContracts(t *testing.T) {
	tests := []struct {
		path     string
		typeName string
		required []string
	}{
		{
			path:     "docs/PLAN.schema.json",
			typeName: "object",
			required: []string{"schema_version", "state", "lane", "target_ref", "lane_tip", "source_head", "source_mode", "untracked_policy", "source_digest", "source_scopes", "lease_digest", "message_policy", "source_paths", "groups"},
		},
		{
			path:     "docs/PLAN.legacy-v1.schema.json",
			typeName: "array",
		},
		{
			path:     "docs/STATUS.schema.json",
			typeName: "object",
			required: []string{"schema_version", "lane", "leases", "message_policy", "active_paths", "leased_but_not_staged", "empty_lease_scopes", "capture_candidates", "captured_but_still_staged", "staged_leased", "foreign_staged", "expired_lease_ids", "lease_remaining_ms", "renewal", "ref_health"},
		},
		{
			path:     "docs/HANDOFF.schema.json",
			typeName: "object",
			required: []string{"schema_version", "handoff_digest", "capture_only", "landing_authorized", "review_eligible", "lane_ref", "lane_tip", "lane_base_commit", "final_tree", "tip_plan_id", "tip_capture_intent_digest", "segments"},
		},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			body, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			var schema map[string]any
			if err := json.Unmarshal(body, &schema); err != nil {
				t.Fatalf("schema is not JSON: %v", err)
			}
			if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
				t.Errorf("unexpected JSON Schema dialect: %v", schema["$schema"])
			}
			if schema["type"] != test.typeName {
				t.Errorf("type = %v, want %s", schema["type"], test.typeName)
			}
			if strict, ok := schema["additionalProperties"]; test.typeName == "object" && (!ok || strict != false) {
				t.Errorf("top-level object must reject additional properties")
			}
			required := stringSet(schema["required"])
			for _, field := range test.required {
				if !required[field] {
					t.Errorf("required field %q is missing", field)
				}
			}
		})
	}
}

func stringSet(value any) map[string]bool {
	result := map[string]bool{}
	values, _ := value.([]any)
	for _, value := range values {
		if text, ok := value.(string); ok {
			result[text] = true
		}
	}
	return result
}
