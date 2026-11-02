package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/nstranquist/wip-commit/internal/contract"
)

func TestContractCommandNeedsNoRepository(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--json", "contract"}, bytes.NewReader(nil), &stdout, &stderr); code != 0 {
		t.Fatalf("contract exit = %d, stderr = %s", code, stderr.String())
	}
	var result struct {
		OK     bool           `json:"ok"`
		Action string         `json:"action"`
		Data   contractResult `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Action != "contract" || result.Data.Protocol != contract.Protocol {
		t.Fatalf("contract result = %#v", result)
	}
	if result.Data.PortableBundleSHA256 == "" {
		t.Fatal("contract omitted portable bundle digest")
	}
}
