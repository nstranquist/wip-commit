package pathid

import (
	"path/filepath"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var fold = cases.Fold()

func Key(path string) string {
	path = filepath.ToSlash(filepath.Clean(path))
	folded := fold.String(norm.NFC.String(path))
	// Unicode case folding uses uppercase forms for some Cherokee characters.
	// Lowercasing the folded form gives every supported path an idempotent key.
	return norm.NFC.String(strings.ToLower(folded))
}

func Overlap(left, right string) bool {
	left, right = Key(left), Key(right)
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func Covered(path string, scopes []string) bool {
	path = Key(path)
	for _, scope := range scopes {
		scope = Key(scope)
		if path == scope || strings.HasPrefix(path, scope+"/") {
			return true
		}
	}
	return false
}
