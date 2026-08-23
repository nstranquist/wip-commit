// Package contract defines the public command and durable-reader capability
// contract. The values are data, not feature detection by prose or version
// string.
package contract

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	SchemaVersion         = "1.0.0"
	Protocol              = "wip.command/2.0.0"
	PortableSkillContract = "wip.portable-skill/1.0.0"
)

var capabilities = []string{
	"capture.v2.private-index-cas",
	"doctor.v1",
	"handoff.v1.capture-only",
	"install.digest-upgrade.v1",
	"plan.v2.no-overwrite",
	"state.archive-history.v1",
	"state.capture-intent.v2",
	"state.init-intent.v2",
	"state.profile.v2",
	"state.reader-floor.v1",
	"status.v2.observe",
	"version.v2.provenance",
}

var stateReaderCapabilities = []string{
	"state.archive-history.v1",
	"state.capture-intent.v2",
	"state.init-intent.v2",
	"state.profile.v2",
	"state.reader-floor.v1",
}

type Document struct {
	SchemaVersion         string   `json:"schema_version"`
	Protocol              string   `json:"protocol"`
	Capabilities          []string `json:"capabilities"`
	PortableSkillContract string   `json:"portable_skill_contract"`
}

func Current() Document {
	return Document{
		SchemaVersion:         SchemaVersion,
		Protocol:              Protocol,
		Capabilities:          slices.Clone(capabilities),
		PortableSkillContract: PortableSkillContract,
	}
}

func StateReaderCapabilities() []string {
	return slices.Clone(stateReaderCapabilities)
}

func Supports(required []string) error {
	for _, capability := range required {
		if !slices.Contains(capabilities, capability) {
			return fmt.Errorf("unsupported required capability %q", capability)
		}
	}
	return nil
}

// MeetsProtocolFloor reports whether current is in the same command-protocol
// family and is at least the requested semantic version. Protocol-major
// changes are deliberately incompatible even when their numeric value is
// greater.
func MeetsProtocolFloor(current, floor string) error {
	currentVersion, err := parseProtocol(current)
	if err != nil {
		return fmt.Errorf("current protocol: %w", err)
	}
	floorVersion, err := parseProtocol(floor)
	if err != nil {
		return fmt.Errorf("reader floor: %w", err)
	}
	if currentVersion[0] != floorVersion[0] {
		return fmt.Errorf("protocol major %d cannot read protocol major %d state", currentVersion[0], floorVersion[0])
	}
	for index := range currentVersion {
		if currentVersion[index] > floorVersion[index] {
			return nil
		}
		if currentVersion[index] < floorVersion[index] {
			return fmt.Errorf("protocol %s is below required floor %s", current, floor)
		}
	}
	return nil
}

func parseProtocol(value string) ([3]uint64, error) {
	var version [3]uint64
	const prefix = "wip.command/"
	if !strings.HasPrefix(value, prefix) {
		return version, fmt.Errorf("invalid protocol %q", value)
	}
	parts := strings.Split(strings.TrimPrefix(value, prefix), ".")
	if len(parts) != len(version) {
		return version, fmt.Errorf("invalid protocol %q", value)
	}
	for index, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return version, fmt.Errorf("invalid protocol %q", value)
		}
		parsed, err := strconv.ParseUint(part, 10, 31)
		if err != nil {
			return version, fmt.Errorf("invalid protocol %q", value)
		}
		version[index] = parsed
	}
	return version, nil
}
