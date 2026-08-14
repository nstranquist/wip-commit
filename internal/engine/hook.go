package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/process"
)

const (
	DefaultHookTimeout = 2 * time.Minute
	MaxHookTimeout     = 24 * time.Hour
	maxHookBytes       = 4 << 20
)

type hookSnapshot struct {
	Exists     bool
	Mode       os.FileMode
	Size       int64
	ModifiedNS int64
	Digest     string
}

type boundHook struct {
	Name, Source, Private string
	Snapshot              hookSnapshot
	cleanup               func()
}

func prepareHook(ctx context.Context, repo gitx.Repo, root, name string) (boundHook, error) {
	source, err := repo.Text(ctx, nil, "rev-parse", "--git-path", "hooks/"+name)
	if err != nil {
		return boundHook{}, fail.Wrap("GIT_FAILED", err)
	}
	if !filepath.IsAbs(source) {
		source = filepath.Join(repo.Root, source)
	}
	snapshot, body, err := readHook(source)
	if err != nil {
		return boundHook{}, err
	}
	hook := boundHook{Name: name, Source: source, Snapshot: snapshot, cleanup: func() {}}
	if !snapshot.Exists || snapshot.Mode&0o111 == 0 {
		return hook, nil
	}
	directory, err := os.MkdirTemp(root, "hook-"+name+"-*")
	if err != nil {
		return boundHook{}, fail.Wrap("HOOK_PREPARE_FAILED", err)
	}
	hook.cleanup = func() { _ = os.RemoveAll(directory) }
	hook.Private = filepath.Join(directory, name)
	file, err := os.OpenFile(hook.Private, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		hook.cleanup()
		return boundHook{}, fail.Wrap("HOOK_PREPARE_FAILED", err)
	}
	if written, err := file.Write(body); err != nil || written != len(body) {
		_ = file.Close()
		hook.cleanup()
		if err == nil {
			err = io.ErrShortWrite
		}
		return boundHook{}, fail.Wrap("HOOK_PREPARE_FAILED", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		hook.cleanup()
		return boundHook{}, fail.Wrap("HOOK_PREPARE_FAILED", err)
	}
	if err := file.Close(); err != nil {
		hook.cleanup()
		return boundHook{}, fail.Wrap("HOOK_PREPARE_FAILED", err)
	}
	return hook, nil
}

func readHook(path string) (hookSnapshot, []byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return hookSnapshot{}, nil, nil
	}
	if err != nil {
		return hookSnapshot{}, nil, fail.Wrap("HOOK_PREPARE_FAILED", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return hookSnapshot{}, nil, fail.Wrap("HOOK_PREPARE_FAILED", err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return hookSnapshot{}, nil, fail.Wrap("HOOK_PREPARE_FAILED", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || !os.SameFile(info, pathInfo) {
		return hookSnapshot{}, nil, fail.Errorf("HOOK_UNSAFE_TYPE", "hook %s must be a regular non-symlink file", path)
	}
	if info.Size() > maxHookBytes {
		return hookSnapshot{}, nil, fail.Errorf("HOOK_TOO_LARGE", "hook %s exceeds %d bytes", path, maxHookBytes)
	}
	body, err := io.ReadAll(io.LimitReader(file, maxHookBytes+1))
	if err != nil {
		return hookSnapshot{}, nil, fail.Wrap("HOOK_PREPARE_FAILED", err)
	}
	if int64(len(body)) != info.Size() || len(body) > maxHookBytes {
		return hookSnapshot{}, nil, fail.Errorf("HOOK_SOURCE_MOVED", "hook %s changed while it was read", path)
	}
	digest := sha256.Sum256(body)
	return hookSnapshot{Exists: true, Mode: info.Mode(), Size: info.Size(), ModifiedNS: info.ModTime().UnixNano(), Digest: "sha256:" + hex.EncodeToString(digest[:])}, body, nil
}

func (hook boundHook) validate() error {
	current, _, err := readHook(hook.Source)
	if err != nil {
		return err
	}
	if current != hook.Snapshot {
		return fail.Errorf("HOOK_SOURCE_MOVED", "%s hook changed while capture was prepared", hook.Name)
	}
	return nil
}

func (hook boundHook) run(ctx context.Context, repo gitx.Repo, env []string, timeout time.Duration, stdout, stderr io.Writer) error {
	if hook.Private == "" {
		return nil
	}
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := process.CommandContext(hookCtx, hook.Private)
	if runtime.GOOS == "windows" {
		cmd = process.CommandContext(hookCtx, "git", "-C", repo.Root, "-c", "core.hooksPath="+filepath.Dir(hook.Private), "hook", "run", hook.Name)
	}
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = repo.Root, env, stdout, stderr
	err := cmd.Run()
	if errors.Is(hookCtx.Err(), context.DeadlineExceeded) {
		return fail.Errorf("HOOK_TIMEOUT", "%s hook exceeded %s", hook.Name, timeout)
	}
	if err != nil {
		return fail.Wrap("HOOK_FAILED", err)
	}
	return nil
}

func hooksDigest(hooks ...boundHook) string {
	hash := sha256.New()
	for _, hook := range hooks {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00", hook.Name, hook.Snapshot.Digest)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func hookEnvironment(index, target, object string) []string {
	environment := append([]string(nil), os.Environ()...)
	environment = append(environment, "GIT_INDEX_FILE="+index, "WIP_TARGET_REF="+target)
	if strings.TrimSpace(object) != "" {
		environment = append(environment, "WIP_COMMIT_OBJECT="+object)
	}
	return environment
}
