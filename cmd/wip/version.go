package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	skillbundle "github.com/nstranquist/wip-commit/skills/wip-commit"
)

const versionResultSchema = "2.0.0"

const maxVersionExecutableBytes = 512 << 20

var (
	buildRevision    string
	buildSourceState string
	buildVCSTime     string
	buildReleaseTag  string
)

type versionResult struct {
	SchemaVersion        string `json:"schema_version"`
	Version              string `json:"version"`
	Channel              string `json:"channel"`
	ReleaseTag           string `json:"release_tag,omitempty"`
	SourceStamped        bool   `json:"source_stamped"`
	Revision             string `json:"revision,omitempty"`
	SourceState          string `json:"source_state,omitempty"`
	VCSTime              string `json:"vcs_time,omitempty"`
	GoVersion            string `json:"go_version"`
	ExecutableSHA256     string `json:"executable_sha256,omitempty"`
	PortableBundleSHA256 string `json:"portable_bundle_sha256,omitempty"`
}

func currentVersionInfo() versionResult {
	result := versionResult{
		SchemaVersion: versionResultSchema,
		Version:       version,
		ReleaseTag:    strings.TrimSpace(buildReleaseTag),
		Revision:      strings.TrimSpace(buildRevision),
		SourceState:   strings.TrimSpace(buildSourceState),
		VCSTime:       strings.TrimSpace(buildVCSTime),
		GoVersion:     runtime.Version(),
	}
	result.ExecutableSHA256 = currentExecutableDigest()
	result.PortableBundleSHA256, _ = skillbundle.PayloadDigest()
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return finalizeVersionIdentity(result)
	}
	if build.GoVersion != "" {
		result.GoVersion = build.GoVersion
	}
	return versionInfoFromSettings(result, build.Settings)
}

func currentExecutableDigest() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxVersionExecutableBytes {
		return ""
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxVersionExecutableBytes+1))
	if err != nil || written != info.Size() {
		return ""
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func versionInfoFromSettings(result versionResult, settings []debug.BuildSetting) versionResult {
	trustedReleaseInjection := result.ReleaseTag != "" && result.ReleaseTag == "v"+result.Version && !strings.HasSuffix(result.Version, "-dev")
	if !trustedReleaseInjection {
		// Development builds must use Go's VCS settings. Do not let arbitrary
		// linker flags turn a dirty or unrelated source build into a clean one
		// or claim that a development version is a release.
		result.ReleaseTag = ""
		result.Revision = ""
		result.SourceState = ""
		result.VCSTime = ""
	}
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			if result.Revision == "" {
				result.Revision = strings.TrimSpace(setting.Value)
			}
		case "vcs.time":
			if result.VCSTime == "" {
				result.VCSTime = strings.TrimSpace(setting.Value)
			}
		case "vcs.modified":
			if result.SourceState != "" {
				continue
			}
			switch strings.TrimSpace(setting.Value) {
			case "true":
				result.SourceState = "modified"
			case "false":
				result.SourceState = "clean"
			}
		}
	}
	return finalizeVersionIdentity(result)
}

func finalizeVersionIdentity(result versionResult) versionResult {
	result.SourceStamped = result.Revision != ""
	switch {
	case result.ReleaseTag != "" && result.ReleaseTag == "v"+result.Version:
		result.Channel = "release"
	case result.ReleaseTag == "" && strings.HasSuffix(result.Version, "-dev"):
		result.Channel = "development"
	default:
		result.Channel = "unverified"
	}
	return result
}

func (result versionResult) human() string {
	text := "wip " + result.Version
	if result.Revision == "" {
		return text
	}
	revision := result.Revision
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if result.SourceState != "" {
		return text + " (" + revision + ", " + result.SourceState + ")"
	}
	return text + " (" + revision + ")"
}
