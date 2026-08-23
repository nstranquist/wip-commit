package filelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Lock struct{ file *os.File }

const MaxWait = 24 * time.Hour

func Acquire(path string, wait time.Duration) (*Lock, error) {
	return acquire(path, wait, true)
}

// AcquireExisting locks an existing regular lock file without changing its
// bytes or creating a parent directory.
func AcquireExisting(path string, wait time.Duration) (*Lock, error) {
	return acquire(path, wait, false)
}

func acquire(path string, wait time.Duration, writeOwner bool) (*Lock, error) {
	if wait < 0 {
		return nil, errors.New("lock wait cannot be negative")
	}
	if wait > MaxWait {
		return nil, fmt.Errorf("lock wait exceeds %s", MaxWait)
	}
	if writeOwner {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
	}
	var file *os.File
	var err error
	if writeOwner {
		file, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	} else {
		file, err = openExisting(path)
	}
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		owned, lockErr := tryExclusive(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, lockErr
		}
		if owned {
			if writeOwner {
				if err = file.Truncate(0); err == nil {
					_, err = file.Seek(0, 0)
				}
				if err == nil {
					_, err = fmt.Fprintf(file, "pid=%d\nstarted_at=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
				}
				if err == nil {
					err = file.Sync()
				}
			} else {
				err = validateExistingPath(path, file)
			}
			if err != nil {
				_ = unlock(file)
				_ = file.Close()
				return nil, err
			}
			return &Lock{file: file}, nil
		}
		if wait == 0 || !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("lock remained held after %s: %s", wait, path)
		}
		pause := min(50*time.Millisecond, time.Until(deadline))
		if pause > 0 {
			time.Sleep(pause)
		}
	}
}

func openExisting(path string) (*os.File, error) {
	expected, err := regularPathInfo(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(expected, opened) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("lock path changed while opening: %s", path)
	}
	return file, nil
}

func validateExistingPath(path string, file *os.File) error {
	current, err := regularPathInfo(path)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(opened, current) {
		return fmt.Errorf("lock path changed while acquiring: %s", path)
	}
	return nil
}

func regularPathInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("lock path is not a regular file: %s", path)
	}
	return info, nil
}

func (lock *Lock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := errors.Join(unlock(lock.file), lock.file.Close())
	lock.file = nil
	return err
}
