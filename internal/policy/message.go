package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/message"
	"github.com/nstranquist/wip-commit/internal/safeio"
	"github.com/nstranquist/wip-commit/internal/strictjson"
)

const (
	ConfigName          = ".wip-commit.json"
	ConfigSchemaVersion = "1.0.0"
	ConventionalV1      = "conventional-v1"
	SafetyOnlyV1        = "safety-only-v1"
	maxConfigBytes      = 64 << 10
	subjectLimit        = 72
)

var (
	conventional = regexp.MustCompile(`^([a-z][a-z0-9-]*)(?:\(([a-z0-9][a-z0-9._/-]*)\))?(!)?: (.+)$`)
	types        = map[string]bool{"build": true, "chore": true, "ci": true, "docs": true, "feat": true, "fix": true, "perf": true, "refactor": true, "revert": true, "style": true, "test": true}
	vague        = map[string]bool{"change": true, "changes": true, "fix": true, "misc": true, "stuff": true, "update": true, "updates": true, "work": true}
)

type Receipt struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type config struct {
	SchemaVersion string `json:"schema_version"`
	MessagePolicy string `json:"message_policy"`
}

func Default() Receipt { return mustResolve(ConventionalV1) }

func Resolve(name string) (Receipt, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = ConventionalV1
	}
	if name != ConventionalV1 && name != SafetyOnlyV1 {
		return Receipt{}, fail.Errorf("INVALID_MESSAGE_POLICY", "unsupported message policy %q", name)
	}
	sum := sha256.Sum256([]byte(name + "\x00" + ConfigSchemaVersion))
	return Receipt{ID: name, Version: ConfigSchemaVersion, Digest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

func mustResolve(name string) Receipt {
	receipt, err := Resolve(name)
	if err != nil {
		panic(err)
	}
	return receipt
}

func Load(repoRoot, override string) (Receipt, error) {
	if strings.TrimSpace(override) != "" {
		return Resolve(override)
	}
	body, err := safeio.ReadRegular(filepath.Join(repoRoot, ConfigName), maxConfigBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Receipt{}, fail.Wrap("MESSAGE_POLICY_READ_FAILED", err)
	}
	var value config
	if err := strictjson.Decode(body, &value); err != nil {
		return Receipt{}, fail.Wrap("MESSAGE_POLICY_READ_FAILED", err)
	}
	if value.SchemaVersion != ConfigSchemaVersion {
		return Receipt{}, fail.Errorf("MIGRATION_REQUIRED", "message policy schema %q is unsupported; this wip release supports %s", value.SchemaVersion, ConfigSchemaVersion)
	}
	return Resolve(value.MessagePolicy)
}

func ValidateReceipt(receipt Receipt) error {
	wanted, err := Resolve(receipt.ID)
	if err != nil {
		return err
	}
	if receipt != wanted {
		return fail.New("INVALID_MESSAGE_POLICY", "message policy receipt does not match its named policy")
	}
	return nil
}

func Validate(receipt Receipt, value string, allowWIP bool) error {
	if err := ValidateReceipt(receipt); err != nil {
		return err
	}
	if err := message.ValidateSafety(value); err != nil {
		return err
	}
	if receipt.ID == SafetyOnlyV1 {
		return nil
	}
	lines := strings.Split(value, "\n")
	subject := strings.TrimSpace(lines[0])
	if utf8.RuneCountInString(subject) > subjectLimit {
		return fail.New("MESSAGE_POLICY_REJECTED", "commit subject exceeds 72 characters under conventional-v1")
	}
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" {
		return fail.New("MESSAGE_POLICY_REJECTED", "separate the subject and body with a blank line under conventional-v1")
	}
	match := conventional.FindStringSubmatch(subject)
	if match == nil {
		return fail.New("MESSAGE_POLICY_REJECTED", "use <type>(<scope>): <concrete outcome> under conventional-v1")
	}
	if match[1] == "wip" {
		if !allowWIP {
			return fail.New("WIP_PREFIX_NOT_AUTHORIZED", "wip: requires explicit authorization")
		}
	} else if !types[match[1]] {
		return fail.Errorf("MESSAGE_POLICY_REJECTED", "unsupported Conventional Commit type %q", match[1])
	}
	description := strings.TrimSpace(match[4])
	if description != match[4] {
		return fail.New("MESSAGE_POLICY_REJECTED", "use one space after the subject colon")
	}
	if utf8.RuneCountInString(description) < 4 || vague[strings.ToLower(strings.TrimSuffix(description, "."))] {
		return fail.New("MESSAGE_POLICY_REJECTED", "name the concrete outcome")
	}
	if strings.HasSuffix(description, ".") {
		return fail.New("MESSAGE_POLICY_REJECTED", "commit subject must not end with a period")
	}
	return nil
}

func ValidateMessages(receipt Receipt, values []string, allowWIP bool) error {
	seen := map[string]bool{}
	for _, value := range values {
		if err := Validate(receipt, value, allowWIP); err != nil {
			return err
		}
		key := strings.ToLower(strings.TrimSpace(value))
		if seen[key] {
			return fail.New("MESSAGE_POLICY_REJECTED", "commit groups need distinct messages under "+receipt.ID)
		}
		seen[key] = true
	}
	return nil
}
