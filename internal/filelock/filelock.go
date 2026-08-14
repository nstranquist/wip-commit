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
	if wait < 0 {
		return nil, errors.New("lock wait cannot be negative")
	}
	if wait > MaxWait {
		return nil, fmt.Errorf("lock wait exceeds %s", MaxWait)
	}
	if wait == 0 {
		wait = 2 * time.Minute
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
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
			if err = file.Truncate(0); err == nil {
				_, err = file.Seek(0, 0)
			}
			if err == nil {
				_, err = fmt.Fprintf(file, "pid=%d\nstarted_at=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			}
			if err == nil {
				err = file.Sync()
			}
			if err != nil {
				_ = unlock(file)
				_ = file.Close()
				return nil, err
			}
			return &Lock{file: file}, nil
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("lock remained held after %s: %s", wait, path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (lock *Lock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := errors.Join(unlock(lock.file), lock.file.Close())
	lock.file = nil
	return err
}
