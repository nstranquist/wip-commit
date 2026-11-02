package wipcommit

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseCorpusUsesCurrentLineWithoutRewritingHistoricalEvidence(t *testing.T) {
	const (
		currentSource = "0.2.0-dev"
		currentTag    = "v0.2.0-beta.1"
		oldLine       = "0.1.0"
	)

	prospective := map[string][]string{
		"README.md":    {currentSource, currentTag},
		"CHANGELOG.md": {currentSource, currentTag},
		"SECURITY.md":  {currentTag},
		"docs/KEP-0002-hosted-candidate-correction.md": {currentTag},
		"docs/OSS-PUBLIC-BETA.md":                      {currentTag},
		"docs/OSS-READINESS.md":                        {currentTag},
		"docs/RELEASE.md":                              {currentTag},
		"scripts/release/main.go":                      {currentTag},
	}
	for path, required := range prospective {
		body := readVersionCorpusFile(t, path)
		if strings.Contains(body, oldLine) {
			t.Errorf("%s contains superseded prospective release line %q", path, oldLine)
		}
		for _, fragment := range required {
			if !strings.Contains(body, fragment) {
				t.Errorf("%s does not contain current release identity %q", path, fragment)
			}
		}
	}

	mixedEvidence := map[string][]string{
		"docs/OSS-PUBLIC-BETA.requirements.yaml": {
			`"target_version": "v0.2.0-beta.1"`,
			"returned wip 0.1.0-beta.1",
		},
		"docs/PUBLICATION-CORRECTION.md": {
			"target is `v0.2.0-beta.1`",
			"candidate/v0.1.0-beta.1:31996054315",
		},
		"docs/PUBLICATION-EVIDENCE.md": {
			"Current release target: `v0.2.0-beta.1`",
			"`wip 0.1.0-beta.1`",
		},
		"docs/PUBLICATION-HANDOFF.md": {
			"Current release target: `v0.2.0-beta.1`",
			"refs/heads/candidate/v0.1.0-beta.1",
		},
		"docs/DOGFOOD.md": {
			"installed binary reported `0.1.0-beta.1`",
		},
		"docs/BETA-EXERCISE.maintainer-2026-08-16.json": {
			`"wip_version": "0.1.0-beta.1"`,
		},
	}
	for path, required := range mixedEvidence {
		body := readVersionCorpusFile(t, path)
		for _, fragment := range required {
			if !strings.Contains(body, fragment) {
				t.Errorf("%s does not preserve release evidence %q", path, fragment)
			}
		}
	}
}

func readVersionCorpusFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
