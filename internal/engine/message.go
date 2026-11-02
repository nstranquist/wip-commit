package engine

import (
	"github.com/nstranquist/wip-commit/internal/message"
	"github.com/nstranquist/wip-commit/internal/policy"
)

// ValidateMessage remains for callers of the pre-v2 internal API. New capture
// paths validate named policy before entering the engine.
func ValidateMessage(value string, allowWIP bool) error {
	return policy.Validate(policy.Default(), value, allowWIP)
}

func ValidateMessageSafety(value string) error { return message.ValidateSafety(value) }
