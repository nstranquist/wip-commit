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

func FuzzPortablePathIdentity(f *testing.F) {
	for _, seed := range [][2]string{{"src", "src/api/file.go"}, {"Docs/CAFÉ.txt", "docs/cafe\u0301.txt"}, {"src/api", "src/apis"}, {"a b", "a b/c\nd"}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, left, right string) {
		if Overlap(left, right) != Overlap(right, left) {
			t.Fatalf("overlap is not symmetric for %q and %q", left, right)
		}
		if Key(Key(left)) != Key(left) || Key(Key(right)) != Key(right) {
			t.Fatalf("portable key is not idempotent for %q or %q", left, right)
		}
		if Covered(left, []string{right}) && !Overlap(left, right) {
			t.Fatalf("covered path does not overlap its scope: %q and %q", left, right)
		}
	})
}
