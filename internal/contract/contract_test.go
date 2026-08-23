package contract

import (
	"slices"
	"testing"
)

func TestCurrentContractIsCanonical(t *testing.T) {
	document := Current()
	if document.SchemaVersion != SchemaVersion || document.Protocol != Protocol || document.PortableSkillContract != PortableSkillContract {
		t.Fatalf("contract identity = %#v", document)
	}
	if !slices.IsSorted(document.Capabilities) {
		t.Fatalf("capabilities are not sorted: %v", document.Capabilities)
	}
	if len(document.Capabilities) != len(slices.Compact(slices.Clone(document.Capabilities))) {
		t.Fatalf("capabilities contain duplicates: %v", document.Capabilities)
	}
	if err := Supports(StateReaderCapabilities()); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolFloorIsMajorBoundedSemver(t *testing.T) {
	for _, test := range []struct {
		current, floor string
		allowed        bool
	}{
		{Protocol, Protocol, true},
		{"wip.command/2.1.0", Protocol, true},
		{"wip.command/2.0.0", "wip.command/2.1.0", false},
		{"wip.command/3.0.0", Protocol, false},
		{"wip.command/02.0.0", Protocol, false},
		{"other/2.0.0", Protocol, false},
	} {
		if allowed := MeetsProtocolFloor(test.current, test.floor) == nil; allowed != test.allowed {
			t.Errorf("MeetsProtocolFloor(%q, %q) = %v, want %v", test.current, test.floor, allowed, test.allowed)
		}
	}
}
