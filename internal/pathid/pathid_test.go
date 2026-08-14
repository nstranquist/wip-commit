package pathid

import "testing"

func TestPortableUnicodeIdentity(t *testing.T) {
	if Key("Docs/CAFÉ.txt") != Key("docs/cafe\u0301.txt") {
		t.Fatal("case fold and NFC normalization did not converge")
	}
	if !Overlap("SRC", "src/api/file.go") {
		t.Fatal("case-insensitive parent scope did not overlap")
	}
	if Overlap("src/api", "src/apis") {
		t.Fatal("component boundary was ignored")
	}
}
