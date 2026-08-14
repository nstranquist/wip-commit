package engine

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nstranquist/wip-commit/internal/fail"
)

const subjectLimit = 72

var (
	conventional = regexp.MustCompile(`^([a-z][a-z0-9-]*)(?:\(([a-z0-9][a-z0-9._/-]*)\))?(!)?: (.+)$`)
	types        = map[string]bool{"build": true, "chore": true, "ci": true, "docs": true, "feat": true, "fix": true, "perf": true, "refactor": true, "revert": true, "style": true, "test": true}
	vague        = map[string]bool{"change": true, "changes": true, "fix": true, "misc": true, "stuff": true, "update": true, "updates": true, "work": true}
)

func ValidateMessage(message string, allowWIP bool) error {
	if !utf8.ValidString(message) {
		return fail.New("INVALID_COMMIT_MESSAGE", "commit message must be valid UTF-8")
	}
	for _, character := range message {
		if character == '\r' || character == '\x00' || (unicode.IsControl(character) && character != '\n' && character != '\t') {
			return fail.New("INVALID_COMMIT_MESSAGE", "commit message contains an unsupported control character")
		}
	}
	trimmed := strings.TrimSpace(message)
	if message != trimmed {
		return fail.New("INVALID_COMMIT_MESSAGE", "commit message cannot start or end with whitespace")
	}
	message = trimmed
	if message == "" {
		return fail.New("INVALID_COMMIT_MESSAGE", "commit message cannot be empty")
	}
	lines := strings.Split(message, "\n")
	subject := strings.TrimSpace(lines[0])
	if utf8.RuneCountInString(subject) > subjectLimit {
		return fail.New("INVALID_COMMIT_MESSAGE", "commit subject exceeds 72 characters")
	}
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" {
		return fail.New("INVALID_COMMIT_MESSAGE", "separate the subject and body with a blank line")
	}
	match := conventional.FindStringSubmatch(subject)
	if match == nil {
		return fail.New("INVALID_COMMIT_MESSAGE", "use <type>(<scope>): <concrete outcome>")
	}
	if match[1] == "wip" {
		if !allowWIP {
			return fail.New("WIP_PREFIX_NOT_AUTHORIZED", "wip: requires explicit authorization")
		}
	} else if !types[match[1]] {
		return fail.Errorf("INVALID_COMMIT_MESSAGE", "unsupported Conventional Commit type %q", match[1])
	}
	description := strings.TrimSpace(match[4])
	if description != match[4] {
		return fail.New("INVALID_COMMIT_MESSAGE", "use one space after the subject colon")
	}
	if utf8.RuneCountInString(description) < 4 || vague[strings.ToLower(strings.TrimSuffix(description, "."))] {
		return fail.New("INVALID_COMMIT_MESSAGE", "name the concrete outcome")
	}
	if strings.HasSuffix(description, ".") {
		return fail.New("INVALID_COMMIT_MESSAGE", "commit subject must not end with a period")
	}
	return nil
}
