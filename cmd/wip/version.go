package main

import (
	"runtime"
	"runtime/debug"
	"strings"
)

const versionResultSchema = "1.0.0"

var (
	buildRevision    string
	buildSourceState string
	buildVCSTime     string
)

type versionResult struct {
	SchemaVersion string `json:"schema_version"`
	Version       string `json:"version"`
	Revision      string `json:"revision,omitempty"`
	SourceState   string `json:"source_state,omitempty"`
	VCSTime       string `json:"vcs_time,omitempty"`
	GoVersion     string `json:"go_version"`
}

func currentVersionInfo() versionResult {
	result := versionResult{
		SchemaVersion: versionResultSchema,
		Version:       version,
		Revision:      strings.TrimSpace(buildRevision),
		SourceState:   strings.TrimSpace(buildSourceState),
		VCSTime:       strings.TrimSpace(buildVCSTime),
		GoVersion:     runtime.Version(),
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return result
	}
	if build.GoVersion != "" {
		result.GoVersion = build.GoVersion
	}
	return versionInfoFromSettings(result, build.Settings)
}

func versionInfoFromSettings(result versionResult, settings []debug.BuildSetting) versionResult {
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
