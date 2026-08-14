package strictjson

import "testing"

type fixture struct {
	Name string `json:"name"`
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
