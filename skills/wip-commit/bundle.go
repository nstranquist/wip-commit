// Package skillbundle embeds the portable wip-commit agent skill in the wip
// binary so `wip init` can install the exact public skill without a source
// checkout or a private toolchain.
package skillbundle

import (
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
)

// FS contains the complete portable runtime surface.
//
//go:embed SKILL.md contract.json references/automation.md agents/openai.yaml
var FS embed.FS

var Paths = []string{
	"SKILL.md",
	"references/automation.md",
	"agents/openai.yaml",
	// Publish the contract last so it acts as the bundle's commit marker.
	"contract.json",
}

var PayloadPaths = []string{
	"SKILL.md",
	"references/automation.md",
	"agents/openai.yaml",
}

// PayloadDigest binds the portable instructions without creating a self-hash
// cycle through contract.json. Each sorted path and byte length is framed.
func PayloadDigest() (string, error) {
	return embeddedDigest(PayloadPaths)
}

func BundleDigest() (string, error) {
	return embeddedDigest(Paths)
}

func embeddedDigest(paths []string) (string, error) {
	files := make(map[string][]byte, len(paths))
	for _, path := range paths {
		body, err := FS.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read embedded skill file %s: %w", path, err)
		}
		files[path] = body
	}
	return DigestFiles(files)
}

// DigestFiles binds a complete named file set with sorted path and length
// framing. Callers must first reject unsafe or unexpected filesystem entries.
func DigestFiles(files map[string][]byte) (string, error) {
	paths := make([]string, 0, len(files))
	for path := range files {
		if path == "" {
			return "", fmt.Errorf("portable bundle contains an empty path")
		}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	hash := sha256.New()
	var length [8]byte
	for _, path := range paths {
		body := files[path]
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		binary.BigEndian.PutUint64(length[:], uint64(len(body)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(body)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
