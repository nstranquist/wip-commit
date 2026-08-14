package safeio

import (
	"fmt"
	"io"
	"os"
)

// ReadRegular reads one bounded regular file and rejects path replacement.
func ReadRegular(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	linked, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !linked.Mode().IsRegular() || opened.Mode()&os.ModeSymlink != 0 || linked.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, linked) {
		return nil, fmt.Errorf("%s is not a stable regular file", path)
	}
	if maximum < 0 || opened.Size() > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maximum)
	}
	body, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maximum || int64(len(body)) != opened.Size() {
		return nil, fmt.Errorf("%s changed while it was read", path)
	}
	current, err := os.Lstat(path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return nil, fmt.Errorf("%s changed while it was read", path)
	}
	return body, nil
}
