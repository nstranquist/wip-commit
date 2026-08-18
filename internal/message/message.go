package message

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nstranquist/wip-commit/internal/fail"
)

const MaxBytes = 64 << 10

// ValidateSafety enforces only the invariants needed to store and reproduce a
// Git commit message safely. Repository naming conventions belong to policy.
func ValidateSafety(value string) error {
	if len(value) > MaxBytes {
		return fail.Errorf("INVALID_COMMIT_MESSAGE", "commit message exceeds %d bytes", MaxBytes)
	}
	if !utf8.ValidString(value) {
		return fail.New("INVALID_COMMIT_MESSAGE", "commit message must be valid UTF-8")
	}
	for _, character := range value {
		if character == '\r' || character == '\x00' || (unicode.IsControl(character) && character != '\n' && character != '\t') {
			return fail.New("INVALID_COMMIT_MESSAGE", "commit message contains an unsupported control character")
		}
	}
	trimmed := strings.TrimSpace(value)
	if value != trimmed {
		return fail.New("INVALID_COMMIT_MESSAGE", "commit message cannot start or end with whitespace")
	}
	if trimmed == "" || strings.TrimSpace(strings.SplitN(trimmed, "\n", 2)[0]) == "" {
		return fail.New("INVALID_COMMIT_MESSAGE", "commit message subject cannot be empty")
	}
	return nil
}
