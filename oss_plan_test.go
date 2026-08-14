package wipcommit

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type ossPlan struct {
	SchemaVersion     int               `json:"schema_version"`
	PlanID            string            `json:"plan_id"`
	PlanStatus        string            `json:"plan_status"`
	TargetVersion     string            `json:"target_version"`
	LastReviewed      string            `json:"last_reviewed"`
	StatusDefinitions map[string]string `json:"status_definitions"`
	Requirements      []ossRequirement  `json:"requirements"`
}

type ossRequirement struct {
	ID         string        `json:"id"`
	Gate       string        `json:"gate"`
	Title      string        `json:"title"`
	Status     string        `json:"status"`
	Owner      string        `json:"owner"`
	HumanGate  bool          `json:"human_gate"`
	Acceptance string        `json:"acceptance"`
	Evidence   []ossEvidence `json:"evidence"`
}

type ossEvidence struct {
	Kind       string `json:"kind"`
	Value      string `json:"value"`
	ObservedOn string `json:"observed_on"`
}

func TestOSSPublicBetaRequirements(t *testing.T) {
	file, err := os.Open("docs/OSS-PUBLIC-BETA.requirements.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var plan ossPlan
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		t.Fatalf("decode JSON-compatible YAML: %v", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != 1 || plan.PlanID == "" || plan.TargetVersion == "" {
		t.Fatalf("invalid plan identity: %#v", plan)
	}
	assertOSSVersionReferences(t, plan.TargetVersion)
	if _, err := time.Parse("2006-01-02", plan.LastReviewed); err != nil {
		t.Fatalf("last_reviewed: %v", err)
	}
	allowedPlanStatus := map[string]bool{"proposed": true, "accepted": true, "completed": true}
	if !allowedPlanStatus[plan.PlanStatus] {
		t.Fatalf("invalid plan status %q", plan.PlanStatus)
	}
	allowedStatus := map[string]bool{
		"verified":          true,
		"prepared":          true,
		"planned":           true,
		"human-gated":       true,
		"external-evidence": true,
		"deferred":          true,
	}
	for status := range allowedStatus {
		if plan.StatusDefinitions[status] == "" {
			t.Errorf("missing definition for status %q", status)
		}
	}

	idPattern := regexp.MustCompile(`^OSS-([0-9]{3})$`)
	seen := map[string]bool{}
	lastNumber := 0
	for _, requirement := range plan.Requirements {
		match := idPattern.FindStringSubmatch(requirement.ID)
		if match == nil {
			t.Errorf("invalid requirement id %q", requirement.ID)
			continue
		}
		number, _ := strconv.Atoi(match[1])
		if number <= lastNumber {
			t.Errorf("requirements are not in increasing order at %s", requirement.ID)
		}
		lastNumber = number
		if seen[requirement.ID] {
			t.Errorf("duplicate requirement %s", requirement.ID)
		}
		seen[requirement.ID] = true
		if requirement.Gate != "public-beta" && requirement.Gate != "stable" {
			t.Errorf("%s has invalid gate %q", requirement.ID, requirement.Gate)
		}
		if requirement.Title == "" || requirement.Owner == "" || requirement.Acceptance == "" {
			t.Errorf("%s has an incomplete contract", requirement.ID)
		}
		if !allowedStatus[requirement.Status] {
			t.Errorf("%s has invalid status %q", requirement.ID, requirement.Status)
		}
		gated := requirement.Status == "human-gated" || requirement.Status == "external-evidence"
		if gated != requirement.HumanGate {
			t.Errorf("%s status and human_gate disagree", requirement.ID)
		}
		if len(requirement.Evidence) == 0 {
			t.Errorf("%s has no evidence or gap record", requirement.ID)
		}
		for index, evidence := range requirement.Evidence {
			if evidence.Kind == "" || evidence.Value == "" {
				t.Errorf("%s evidence %d is incomplete", requirement.ID, index)
			}
			if _, err := time.Parse("2006-01-02", evidence.ObservedOn); err != nil {
				t.Errorf("%s evidence %d observed_on: %v", requirement.ID, index, err)
			}
		}
	}
	if len(plan.Requirements) == 0 {
		t.Fatal("plan has no requirements")
	}
}

func assertOSSVersionReferences(t *testing.T, target string) {
	t.Helper()
	source, err := os.ReadFile("cmd/wip/main.go")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`var version = "([^"]+)"`).FindSubmatch(source)
	if len(match) != 2 {
		t.Fatal("cmd/wip/main.go has no static version")
	}
	if wanted := "v" + string(match[1]); target != wanted {
		t.Fatalf("plan target %q does not match command version %q", target, wanted)
	}
	for _, check := range []struct {
		path string
		text string
	}{
		{path: "README.md", text: target},
		{path: "CHANGELOG.md", text: "[" + strings.TrimPrefix(target, "v") + "]"},
	} {
		body, err := os.ReadFile(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), check.text) {
			t.Errorf("%s does not contain %q", check.path, check.text)
		}
	}
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("requirements file contains more than one JSON value")
		}
		return err
	}
	return nil
}
