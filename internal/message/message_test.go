package message

import (
	"strings"
	"testing"

	"github.com/nstranquist/wip-commit/internal/fail"
)

func TestValidateSafety(t *testing.T) {
	for _, valid := range []string{"plain subject", "subject\n\nbody", "fix(core): conventional remains valid"} {
		if err := ValidateSafety(valid); err != nil {
			t.Fatalf("ValidateSafety(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"", " subject", "subject\n", "subject\x00body", strings.Repeat("x", MaxBytes+1)} {
		if err := ValidateSafety(invalid); fail.Code(err) != "INVALID_COMMIT_MESSAGE" {
			t.Errorf("ValidateSafety(%q) = %v (%s)", invalid, err, fail.Code(err))
		}
	}
}
