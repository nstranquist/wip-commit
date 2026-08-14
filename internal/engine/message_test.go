package engine

import "testing"

func TestValidateMessagePolicy(t *testing.T) {
	valid := []string{
		"feat(cli): add interactive lane setup",
		"fix: preserve staged subsets",
		"docs(guide): explain recovery\n\nDescribe the recovery receipt.",
	}
	for _, message := range valid {
		if err := ValidateMessage(message, false); err != nil {
			t.Errorf("ValidateMessage(%q) = %v", message, err)
		}
	}
	invalid := []string{
		"update",
		"chore: update",
		"fix: fix",
		"feat: add setup.",
		" feat: add setup",
		"feat:  add setup",
		"feat: add setup\nbody without separator",
		"wip: temporary checkpoint",
	}
	for _, message := range invalid {
		if err := ValidateMessage(message, false); err == nil {
			t.Errorf("ValidateMessage(%q) unexpectedly passed", message)
		}
	}
	if err := ValidateMessage("wip: checkpoint parser rewrite", true); err != nil {
		t.Fatalf("authorized wip prefix: %v", err)
	}
}
