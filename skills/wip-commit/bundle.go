// Package skillbundle embeds the portable wip-commit agent skill in the wip
// binary so `wip init` can install the exact public skill without a source
// checkout or a private toolchain.
package skillbundle

import "embed"

// FS contains the complete portable runtime surface.
//
//go:embed SKILL.md references/automation.md agents/openai.yaml
var FS embed.FS

var Paths = []string{
	"SKILL.md",
	"references/automation.md",
	"agents/openai.yaml",
}
