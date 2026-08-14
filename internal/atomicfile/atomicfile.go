package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrExists = errors.New("destination already exists")

// Create writes a durable file without replacing an existing path.
func Create(path string, body []byte, mode os.FileMode) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, os.ErrExist) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
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
	remove = false
	return syncDirectory(filepath.Dir(path))
}

func Write(path string, body []byte, mode os.FileMode) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".~"+filepath.Base(path)+".*")
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
	return syncDirectory(filepath.Dir(path))
}
