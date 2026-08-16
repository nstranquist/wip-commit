package strictjson

import (
	"encoding/json"
	"testing"
)

type fixture struct {
	Name string `json:"name"`
}

func FuzzDecodeNeverAcceptsInvalidJSON(f *testing.F) {
	for _, seed := range []string{`{"name":"safe"}`, `{"name":"one","name":"two"}`, `{"extra":true}`, `null`, ``} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		var value fixture
		if err := Decode(body, &value); err != nil {
			return
		}
		if !json.Valid(body) {
			t.Fatalf("Decode accepted invalid JSON: %q", body)
		}
		roundTrip, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded fixture
		if err := Decode(roundTrip, &decoded); err != nil {
			t.Fatalf("strict round trip failed for %q: %v", body, err)
		}
	})
}

func TestDecodeRejectsAmbiguousInput(t *testing.T) {
	var value fixture
	if err := Decode([]byte(`{"name":"safe"}`), &value); err != nil || value.Name != "safe" {
		t.Fatalf("valid decode = %#v, %v", value, err)
	}
	invalid := []string{
		`{"name":"one","name":"two"}`,
		`{"name":"one","extra":true}`,
		`{"name":"one"} true`,
		``,
	}
	for _, body := range invalid {
		if err := Decode([]byte(body), &value); err == nil {
			t.Errorf("invalid JSON passed: %q", body)
		}
	}
}
