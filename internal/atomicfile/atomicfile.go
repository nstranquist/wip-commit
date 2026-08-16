package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrExists = errors.New("destination already exists")

// Create publishes a complete durable file without replacing an existing path.
func Create(path string, body []byte, mode os.FileMode) (err error) {
	return CreateWithTempDir(path, filepath.Dir(path), body, mode)
}

// CreateWithTempDir publishes a complete durable file without replacement and
// keeps its temporary file outside the destination directory when requested.
// Both directories must be on one filesystem so hard-link publication remains
// atomic.
func CreateWithTempDir(path, temporaryDirectory string, body []byte, mode os.FileMode) (err error) {
	destinationDirectory := filepath.Dir(path)
	if err := os.MkdirAll(destinationDirectory, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(temporaryDirectory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(temporaryDirectory, ".~"+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() {
		_ = file.Close()
		_ = os.Remove(temporary)
	}()
	if err = file.Chmod(mode); err != nil {
		return err
	}
	if written, writeErr := file.Write(body); writeErr != nil {
		return writeErr
	} else if written != len(body) {
		return io.ErrShortWrite
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Link(temporary, path); errors.Is(err, os.ErrExist) {
		return ErrExists
	} else if err != nil {
		return err
	}
	if err = syncDirectory(destinationDirectory); err != nil {
		return err
	}
	if err = os.Remove(temporary); err != nil {
		return err
	}
	return syncDirectory(temporaryDirectory)
}

func Write(path string, body []byte, mode os.FileMode) (err error) {
	return WriteWithTempDir(path, filepath.Dir(path), body, mode)
}

// WriteWithTempDir atomically replaces a durable file and keeps its temporary
// file outside the destination directory when requested.
func WriteWithTempDir(path, temporaryDirectory string, body []byte, mode os.FileMode) (err error) {
	destinationDirectory := filepath.Dir(path)
	if err := os.MkdirAll(destinationDirectory, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(temporaryDirectory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(temporaryDirectory, ".~"+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() {
		_ = file.Close()
		_ = os.Remove(temporary)
	}()
	if written, writeErr := file.Write(body); writeErr != nil {
		return writeErr
	} else if written != len(body) {
		return io.ErrShortWrite
	}
	if err = file.Chmod(mode); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = replaceFile(temporary, path); err != nil {
		return fmt.Errorf("atomic replace %s: %w", path, err)
	}
	if err = syncDirectory(destinationDirectory); err != nil {
		return err
	}
	return syncDirectory(temporaryDirectory)
}
