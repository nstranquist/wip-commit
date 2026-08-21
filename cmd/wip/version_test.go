package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionInfoReportsExactSourceProvenance(t *testing.T) {
	revision := "0123456789abcdef0123456789abcdef01234567"
	result := versionInfoFromSettings(versionResult{
		SchemaVersion: versionResultSchema,
		Version:       "0.1.0-beta.1",
		GoVersion:     "go1.26.7",
	}, []debug.BuildSetting{
		{Key: "vcs.revision", Value: revision},
		{Key: "vcs.time", Value: "2026-08-21T05:00:00Z"},
		{Key: "vcs.modified", Value: "false"},
	})

	if result.Revision != revision || result.SourceState != "clean" || result.VCSTime != "2026-08-21T05:00:00Z" {
		t.Fatalf("version provenance = %#v", result)
	}
	if got := result.human(); got != "wip 0.1.0-beta.1 (0123456789ab, clean)" {
		t.Fatalf("human version = %q", got)
	}
}

func TestVersionInfoKeepsLegacyHumanOutputWithoutVCSMetadata(t *testing.T) {
	result := versionResult{SchemaVersion: versionResultSchema, Version: "0.1.0-beta.1", GoVersion: "go1.26.7"}
	if got := result.human(); got != "wip 0.1.0-beta.1" {
		t.Fatalf("human version = %q", got)
	}
}

func TestVersionInfoPrefersLinkerProvenance(t *testing.T) {
	result := versionInfoFromSettings(versionResult{
		SchemaVersion: versionResultSchema,
		Version:       "0.1.0-beta.1",
		Revision:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SourceState:   "clean",
		VCSTime:       "2026-08-21T05:00:00Z",
		GoVersion:     "go1.26.7",
	}, []debug.BuildSetting{
		{Key: "vcs.revision", Value: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{Key: "vcs.time", Value: "2026-08-21T06:00:00Z"},
		{Key: "vcs.modified", Value: "true"},
	})
	if result.Revision != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || result.SourceState != "clean" || result.VCSTime != "2026-08-21T05:00:00Z" {
		t.Fatalf("linker provenance was replaced: %#v", result)
	}
}
