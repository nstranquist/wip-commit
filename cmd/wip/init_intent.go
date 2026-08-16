package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/recordjson"
	"github.com/nstranquist/wip-commit/internal/safeio"
	"github.com/nstranquist/wip-commit/internal/store"
	"github.com/nstranquist/wip-commit/internal/strictjson"
)

const (
	initIntentSchemaVersion = 1
	maxInitIntentBytes      = 1 << 20
)

var initStepOrder = []string{
	"worktree-ready",
	"lane-ready",
	"lease-ready",
	"profile-ready",
	"binary-ready",
	"skill-ready",
}

type initIntent struct {
	SchemaVersion  int        `json:"schema_version"`
	ID             string     `json:"id"`
	Digest         string     `json:"digest"`
	State          string     `json:"state"`
	Lane           string     `json:"lane"`
	Agent          string     `json:"agent"`
	Session        string     `json:"session"`
	Mode           store.Mode `json:"mode"`
	BaseSHA        string     `json:"base_sha"`
	Worktree       string     `json:"worktree"`
	Paths          []string   `json:"paths"`
	InstallPath    string     `json:"install_path,omitempty"`
	SkillPath      string     `json:"skill_path,omitempty"`
	CompletedSteps []string   `json:"completed_steps,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func beginInitIntent(laneStore store.Store, candidate initIntent) (initIntent, string, error) {
	candidate.SchemaVersion = initIntentSchemaVersion
	candidate.Paths = append([]string(nil), candidate.Paths...)
	sort.Strings(candidate.Paths)
	digest, err := digestInitIntent(candidate)
	if err != nil {
		return initIntent{}, "", err
	}
	candidate.Digest = digest
	candidate.ID = "init-" + strings.TrimPrefix(digest, "sha256:")[:24]
	path := filepath.Join(laneStore.Root, "init-intents", candidate.ID+".json")
	if existing, loadErr := loadInitIntent(path); loadErr == nil {
		if existing.Digest != candidate.Digest {
			return initIntent{}, path, fail.New("INIT_INTENT_CONFLICT", "existing initialization intent does not match this setup")
		}
		return existing, path, nil
	} else if fail.Code(loadErr) != "INIT_INTENT_NOT_FOUND" {
		return initIntent{}, path, loadErr
	}
	now := time.Now().UTC()
	candidate.State, candidate.CreatedAt, candidate.UpdatedAt = "pending", now, now
	if err := validateInitIntentCapacity(candidate); err != nil {
		return initIntent{}, path, err
	}
	if err := writeNewInitIntent(path, candidate); err != nil {
		if errors.Is(err, atomicfile.ErrExists) {
			existing, loadErr := loadInitIntent(path)
			if loadErr == nil && existing.Digest == candidate.Digest {
				return existing, path, nil
			}
			if loadErr != nil {
				return initIntent{}, path, loadErr
			}
			return initIntent{}, path, fail.New("INIT_INTENT_CONFLICT", "concurrent initialization intent differs from this setup")
		}
		return initIntent{}, path, err
	}
	return candidate, path, nil
}

func markInitStep(path string, intent initIntent, step string) (initIntent, error) {
	stepIndex := -1
	for index, candidate := range initStepOrder {
		if candidate == step {
			stepIndex = index
			break
		}
	}
	if stepIndex < 0 {
		return initIntent{}, fail.New("INIT_INTENT_FAILED", "unknown initialization step: "+step)
	}
	lock, err := filelock.Acquire(initIntentLockPath(path), 0)
	if err != nil {
		return initIntent{}, fail.Wrap("INIT_INTENT_FAILED", err)
	}
	defer func() { _ = lock.Release() }()
	current, err := loadInitIntent(path)
	if err != nil {
		return initIntent{}, err
	}
	if current.Digest != intent.Digest {
		return initIntent{}, fail.New("INIT_INTENT_CONFLICT", "initialization intent changed while setup was running")
	}
	if stepIndex < len(current.CompletedSteps) {
		return current, nil
	}
	if stepIndex != len(current.CompletedSteps) {
		return initIntent{}, fail.New("INIT_INTENT_FAILED", "initialization steps must complete in order")
	}
	current.CompletedSteps = append(current.CompletedSteps, step)
	current.UpdatedAt = time.Now().UTC()
	if len(current.CompletedSteps) == len(initStepOrder) {
		current.State = "complete"
	}
	if err := writeInitIntent(path, current); err != nil {
		return initIntent{}, err
	}
	return current, nil
}

func loadInitIntent(path string) (initIntent, error) {
	var intent initIntent
	body, err := safeio.ReadRegular(path, maxInitIntentBytes)
	if errors.Is(err, os.ErrNotExist) {
		return intent, fail.New("INIT_INTENT_NOT_FOUND", "initialization intent does not exist")
	}
	if err != nil {
		return intent, fail.Wrap("INIT_INTENT_FAILED", err)
	}
	if err := strictjson.Decode(body, &intent); err != nil {
		return initIntent{}, fail.Wrap("INIT_INTENT_FAILED", err)
	}
	if intent.SchemaVersion != initIntentSchemaVersion {
		return initIntent{}, fail.Errorf("MIGRATION_REQUIRED", "init intent schema version %d is unsupported; this wip release supports version %d", intent.SchemaVersion, initIntentSchemaVersion)
	}
	if err := validateInitIntent(intent); err != nil {
		return initIntent{}, err
	}
	if intent.State != "pending" && intent.State != "complete" {
		return initIntent{}, fail.New("INIT_INTENT_FAILED", "initialization intent identity or state is invalid")
	}
	digest, err := digestInitIntent(intent)
	if err != nil {
		return initIntent{}, err
	}
	if digest != intent.Digest || intent.ID != "init-"+strings.TrimPrefix(digest, "sha256:")[:24] {
		return initIntent{}, fail.New("INIT_INTENT_FAILED", "initialization intent digest is invalid")
	}
	if len(intent.CompletedSteps) > len(initStepOrder) {
		return initIntent{}, fail.New("INIT_INTENT_FAILED", "initialization intent step sequence is invalid")
	}
	for index, step := range intent.CompletedSteps {
		if step != initStepOrder[index] {
			return initIntent{}, fail.New("INIT_INTENT_FAILED", "initialization intent step sequence is invalid")
		}
	}
	if (intent.State == "complete") != (len(intent.CompletedSteps) == len(initStepOrder)) {
		return initIntent{}, fail.New("INIT_INTENT_FAILED", "initialization intent state does not match its completed steps")
	}
	return intent, nil
}

func validateInitIntent(intent initIntent) error {
	for label, value := range map[string]string{"init intent": intent.ID, "lane": intent.Lane, "agent": intent.Agent, "session": intent.Session} {
		if err := store.ValidateID(value, label); err != nil {
			return fail.Wrap("INIT_INTENT_FAILED", err)
		}
	}
	if intent.Mode != store.ModeShared && intent.Mode != store.ModeWorktree {
		return fail.New("INIT_INTENT_FAILED", "initialization intent mode is invalid")
	}
	if intent.Worktree == "" || !filepath.IsAbs(intent.Worktree) || filepath.Clean(intent.Worktree) != intent.Worktree {
		return fail.New("INIT_INTENT_FAILED", "initialization intent worktree is invalid")
	}
	if len(intent.BaseSHA) != 40 && len(intent.BaseSHA) != 64 {
		return fail.New("INIT_INTENT_FAILED", "initialization intent base commit is invalid")
	}
	for _, character := range intent.BaseSHA {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return fail.New("INIT_INTENT_FAILED", "initialization intent base commit is invalid")
			}
		}
	}
	if len(intent.Paths) == 0 || !sort.StringsAreSorted(intent.Paths) {
		return fail.New("INIT_INTENT_FAILED", "initialization intent path set is empty or not canonical")
	}
	for index, path := range intent.Paths {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		if strings.TrimSpace(path) == "" || filepath.IsAbs(path) || path == "." || path == ".." || strings.HasPrefix(path, "../") || clean != path || index > 0 && intent.Paths[index-1] == path {
			return fail.New("INIT_INTENT_FAILED", "initialization intent path set is invalid")
		}
	}
	for _, path := range []string{intent.InstallPath, intent.SkillPath} {
		if path != "" && (!filepath.IsAbs(path) || filepath.Clean(path) != path) {
			return fail.New("INIT_INTENT_FAILED", "initialization intent install path is invalid")
		}
	}
	if intent.CreatedAt.IsZero() || intent.UpdatedAt.IsZero() {
		return fail.New("INIT_INTENT_FAILED", "initialization intent timestamps are invalid")
	}
	return nil
}

func digestInitIntent(intent initIntent) (string, error) {
	intent.ID = ""
	intent.Digest = ""
	intent.State = ""
	intent.CompletedSteps = nil
	intent.CreatedAt = time.Time{}
	intent.UpdatedAt = time.Time{}
	body, err := json.Marshal(intent)
	if err != nil {
		return "", fail.Wrap("INIT_INTENT_FAILED", err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func writeNewInitIntent(path string, intent initIntent) error {
	body, err := marshalInitIntentRecord(intent)
	if err != nil {
		return err
	}
	return atomicfile.CreateWithTempDir(path, initIntentTempDir(path), body, 0o600)
}

func writeInitIntent(path string, intent initIntent) error {
	body, err := marshalInitIntentRecord(intent)
	if err != nil {
		return err
	}
	return fail.Wrap("INIT_INTENT_FAILED", atomicfile.WriteWithTempDir(path, initIntentTempDir(path), body, 0o600))
}

func marshalInitIntentRecord(intent initIntent) ([]byte, error) {
	return recordjson.Marshal(intent, maxInitIntentBytes, "INIT_INTENT_FAILED", "initialization intent")
}

func validateInitIntentCapacity(intent initIntent) error {
	maximum := intent
	maximum.State = "complete"
	maximum.CompletedSteps = append([]string(nil), initStepOrder...)
	maximum.CreatedAt = maximumInitIntentTimestamp()
	maximum.UpdatedAt = maximum.CreatedAt
	_, err := marshalInitIntentRecord(maximum)
	return err
}

func maximumInitIntentTimestamp() time.Time {
	return time.Date(2000, time.December, 31, 23, 59, 59, 123456789, time.UTC)
}

func initIntentTempDir(path string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(path)), "locks")
}

func initIntentLockPath(path string) string {
	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return filepath.Join(initIntentTempDir(path), "intent-"+id+".lock")
}
