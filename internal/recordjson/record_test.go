package recordjson

import (
	"testing"

	"github.com/nstranquist/wip-commit/internal/fail"
)

func TestMarshalAcceptsExactLimitAndRejectsOneByteOver(t *testing.T) {
	record, err := Marshal("abc", 6, "RECORD_FAILED", "test record")
	if err != nil || string(record) != "\"abc\"\n" {
		t.Fatalf("exact record = %q, %v", record, err)
	}
	if _, err := Marshal("abc", 5, "RECORD_FAILED", "test record"); fail.Code(err) != "RECORD_FAILED" {
		t.Fatalf("oversized error = %v (%s)", err, fail.Code(err))
	}
}
