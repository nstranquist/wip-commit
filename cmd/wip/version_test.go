package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestCurrentVersionBindsExecutableAndPortableBundle(t *testing.T) {
	result := currentVersionInfo()
	if !strings.HasPrefix(result.ExecutableSHA256, "sha256:") || len(result.ExecutableSHA256) != 71 {
		t.Fatalf("executable digest = %q", result.ExecutableSHA256)
	}
	if !strings.HasPrefix(result.PortableBundleSHA256, "sha256:") || len(result.PortableBundleSHA256) != 71 {
		t.Fatalf("portable bundle digest = %q", result.PortableBundleSHA256)
	}
}

func TestVersionInfoReportsExactSourceProvenance(t *testing.T) {
	revision := "0123456789abcdef0123456789abcdef01234567"
	result := versionInfoFromSettings(versionResult{
		SchemaVersion: versionResultSchema,
		Version:       "0.2.0-dev",
		GoVersion:     "go1.26.7",
	}, []debug.BuildSetting{
		{Key: "vcs.revision", Value: revision},
		{Key: "vcs.time", Value: "2026-08-21T05:00:00Z"},
		{Key: "vcs.modified", Value: "false"},
	})

	if result.Revision != revision || result.SourceState != "clean" || result.VCSTime != "2026-08-21T05:00:00Z" {
		t.Fatalf("version provenance = %#v", result)
	}
	if result.Channel != "development" || !result.SourceStamped {
		t.Fatalf("development identity = %#v", result)
	}
	if got := result.human(); got != "wip 0.2.0-dev (0123456789ab, clean)" {
		t.Fatalf("human version = %q", got)
	}
}

func TestVersionInfoKeepsHumanOutputWithoutVCSMetadata(t *testing.T) {
	result := finalizeVersionIdentity(versionResult{SchemaVersion: versionResultSchema, Version: "0.2.0-dev", GoVersion: "go1.26.7"})
	if result.Channel != "development" || result.SourceStamped {
		t.Fatalf("unstamped development identity = %#v", result)
	}
	if got := result.human(); got != "wip 0.2.0-dev" {
		t.Fatalf("human version = %q", got)
	}
}

func TestVersionInfoRequiresMatchingInjectedReleaseTag(t *testing.T) {
	release := finalizeVersionIdentity(versionResult{SchemaVersion: versionResultSchema, Version: "0.2.0-beta.1", ReleaseTag: "v0.2.0-beta.1", Revision: "abc"})
	if release.Channel != "release" || !release.SourceStamped {
		t.Fatalf("release identity = %#v", release)
	}
	unverified := finalizeVersionIdentity(versionResult{SchemaVersion: versionResultSchema, Version: "0.2.0-beta.1", ReleaseTag: "v0.2.0-beta.2"})
	if unverified.Channel != "unverified" {
		t.Fatalf("mismatched release identity = %#v", unverified)
	}
}

func TestVersionInfoPrefersLinkerProvenance(t *testing.T) {
	result := versionInfoFromSettings(versionResult{
		SchemaVersion: versionResultSchema,
		Version:       "0.2.0-beta.1",
		ReleaseTag:    "v0.2.0-beta.1",
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

func TestDevelopmentVersionIgnoresLinkerProvenance(t *testing.T) {
	result := versionInfoFromSettings(versionResult{
		SchemaVersion: versionResultSchema,
		Version:       "0.2.0-dev",
		ReleaseTag:    "v0.2.0-dev",
		Revision:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SourceState:   "clean",
		VCSTime:       "2026-08-21T05:00:00Z",
		GoVersion:     "go1.26.7",
	}, []debug.BuildSetting{
		{Key: "vcs.revision", Value: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{Key: "vcs.time", Value: "2026-08-21T06:00:00Z"},
		{Key: "vcs.modified", Value: "true"},
	})
	if result.Revision != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || result.SourceState != "modified" || result.VCSTime != "2026-08-21T06:00:00Z" {
		t.Fatalf("development linker provenance was trusted: %#v", result)
	}
	if result.ReleaseTag != "" || result.Channel != "development" {
		t.Fatalf("development linker release identity was trusted: %#v", result)
	}
}
