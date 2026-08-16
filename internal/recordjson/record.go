// Package recordjson encodes durable JSON records that have bounded readers.
package recordjson

import (
	"encoding/json"

	"github.com/nstranquist/wip-commit/internal/fail"
)

// Marshal encodes one indented JSON record with its final newline. It rejects
// a record that the corresponding bounded reader cannot read.
func Marshal(value any, maximum int64, code, name string) ([]byte, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fail.Wrap(code, err)
	}
	record := append(body, '\n')
	if maximum < 0 || int64(len(record)) > maximum {
		return nil, fail.Errorf(code, "%s exceeds the %d-byte durable record limit", name, maximum)
	}
	return record, nil
}
