package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/safeio"
	"github.com/nstranquist/wip-commit/internal/store"
	skillbundle "github.com/nstranquist/wip-commit/skills/wip-commit"
)

const (
	upgradeSchemaVersion = "2.0.0"
	maxUpgradeBinarySize = 512 << 20
)

type upgradeOptions struct {
	state, binary, skill bool
	apply, yes           bool
	planDigest           string
	installDir           string
	skillDir             string
}

type upgradeResult struct {
	SchemaVersion        string `json:"schema_version"`
	Target               string `json:"target"`
	Path                 string `json:"path"`
	CurrentSHA256        string `json:"current_sha256"`
	DesiredSHA256        string `json:"desired_sha256"`
	RevisionStore        string `json:"revision_store,omitempty"`
	PreviousRevisionPath string `json:"previous_revision_path,omitempty"`
	DesiredRevisionPath  string `json:"desired_revision_path,omitempty"`
	PlanDigest           string `json:"plan_digest"`
	AlreadyCurrent       bool   `json:"already_current"`
	Applied              bool   `json:"applied"`
}

type upgradeBinding struct {
	SchemaVersion        string `json:"schema_version"`
	Target               string `json:"target"`
	Path                 string `json:"path"`
	CurrentSHA256        string `json:"current_sha256"`
	DesiredSHA256        string `json:"desired_sha256"`
	RevisionStore        string `json:"revision_store,omitempty"`
	PreviousRevisionPath string `json:"previous_revision_path,omitempty"`
	DesiredRevisionPath  string `json:"desired_revision_path,omitempty"`
}

type upgradeMaterial struct {
	target, path, currentSHA256, desiredSHA256               string
	revisionStore, previousRevisionPath, desiredRevisionPath string
	alreadyCurrent                                           bool
}

func (application app) runUpgrade(ctx context.Context, args []string) int {
	set := application.flagSet("upgrade")
	options := upgradeOptions{}
	set.BoolVar(&options.state, "state", false, "upgrade the coordination-domain marker")
	set.BoolVar(&options.binary, "binary", false, "upgrade an existing installed binary")
	set.BoolVar(&options.skill, "skill", false, "upgrade an existing portable skill")
	set.BoolVar(&options.apply, "apply", false, "apply the reviewed upgrade")
	set.BoolVar(&options.yes, "yes", false, "confirm the reviewed upgrade")
	set.StringVar(&options.planDigest, "plan-digest", "", "exact digest from preview")
	set.StringVar(&options.installDir, "install-dir", "", "binary install directory")
	set.StringVar(&options.skillDir, "skill-dir", "", "agent skill directory")
	if err := set.Parse(args); err != nil {
		return application.failure("upgrade", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("upgrade", err, nil, 2)
	}
	selected := 0
	for _, value := range []bool{options.state, options.binary, options.skill} {
		if value {
			selected++
		}
	}
	if selected != 1 {
		return application.failure("upgrade", fail.New("INVALID_ARGS", "select exactly one of --state, --binary, or --skill"), nil, 2)
	}
	if options.apply && (!options.yes || options.planDigest == "") {
		return application.failure("upgrade", fail.New("INVALID_ARGS", "--apply requires --yes and --plan-digest"), nil, 2)
	}
	if !options.apply && (options.yes || options.planDigest != "") {
		return application.failure("upgrade", fail.New("INVALID_ARGS", "--yes and --plan-digest require --apply"), nil, 2)
	}
	if options.planDigest != "" && !validSHA256(options.planDigest) {
		return application.failure("upgrade", fail.New("INVALID_ARGS", "--plan-digest must be sha256 followed by 64 lowercase hexadecimal characters"), nil, 2)
	}

	material, repo, err := application.inspectUpgrade(ctx, options)
	if err != nil {
		return application.failure("upgrade", err, nil, 1)
	}
	result, err := bindUpgrade(material)
	if err != nil {
		return application.failure("upgrade", fail.Wrap("UPGRADE_FAILED", err), nil, 1)
	}
	if options.apply {
		if options.planDigest != result.PlanDigest {
			return application.failure("upgrade", fail.New("UPGRADE_PLAN_DIGEST_MISMATCH", "upgrade plan digest does not match the current byte-bound preview"), result, 1)
		}
		if err := application.applyUpgrade(ctx, options, repo, material); err != nil {
			return application.failure("upgrade", err, result, 1)
		}
		result.Applied = !result.AlreadyCurrent
	}
	return application.success("upgrade", result, formatUpgradeResult(result))
}

func (application app) inspectUpgrade(ctx context.Context, options upgradeOptions) (upgradeMaterial, gitx.Repo, error) {
	if options.state {
		repo, err := gitx.Discover(ctx, application.repoDir)
		if err != nil {
			return upgradeMaterial{}, gitx.Repo{}, err
		}
		plan, err := store.InspectDomainUpgrade(repo)
		if err != nil {
			return upgradeMaterial{}, repo, err
		}
		if !plan.AlreadyCurrent {
			if err := ensureStateUpgradeQuiescent(ctx, repo); err != nil {
				return upgradeMaterial{}, repo, err
			}
		}
		return upgradeMaterial{target: "state", path: plan.Path, currentSHA256: plan.CurrentSHA256, desiredSHA256: plan.DesiredSHA256, alreadyCurrent: plan.AlreadyCurrent}, repo, nil
	}
	if options.binary {
		directory, err := normalizedUpgradeDirectory(options.installDir, ".local", "bin")
		if err != nil {
			return upgradeMaterial{}, gitx.Repo{}, err
		}
		material, err := inspectBinaryUpgrade(directory)
		return material, gitx.Repo{}, err
	}
	directory, err := normalizedUpgradeDirectory(options.skillDir, ".agents", "skills")
	if err != nil {
		return upgradeMaterial{}, gitx.Repo{}, err
	}
	material, err := inspectSkillUpgrade(directory)
	return material, gitx.Repo{}, err
}

func (application app) applyUpgrade(ctx context.Context, options upgradeOptions, repo gitx.Repo, reviewed upgradeMaterial) error {
	if options.state {
		if err := ensureStateUpgradeQuiescent(ctx, repo); err != nil {
			return err
		}
		applied, err := store.ApplyDomainUpgrade(repo, reviewed.currentSHA256)
		if err != nil {
			return err
		}
		if applied.CurrentSHA256 != reviewed.desiredSHA256 || !applied.AlreadyCurrent {
			return fail.New("UPGRADE_FAILED", "state marker did not verify at the reviewed desired digest")
		}
		return nil
	}
	if options.binary {
		return applyBinaryUpgrade(reviewed)
	}
	return applySkillUpgrade(reviewed)
}

func ensureStateUpgradeQuiescent(ctx context.Context, repo gitx.Repo) error {
	laneStore, initialized, err := store.Inspect(repo)
	if err != nil {
		return err
	}
	if !initialized {
		return fail.New("STATE_NOT_INITIALIZED", "coordination state is not initialized")
	}
	report := doctorReport{Counts: map[string]int{}, ActiveLanes: []string{}, ExpiredLanes: []string{}, PendingInit: []string{}, PendingCapture: []string{}, PendingArchive: []string{}, ArchiveCandidates: []string{}, Findings: []doctorFinding{}}
	auditDoctor(ctx, laneStore, &report)
	if len(report.ActiveLanes) > 0 || len(report.PendingInit) > 0 || len(report.PendingCapture) > 0 || len(report.PendingArchive) > 0 {
		return fail.New("UPGRADE_BLOCKED", "state upgrade requires no active lanes and no pending initialization, capture, or archive recovery")
	}
	for _, finding := range report.Findings {
		if finding.Severity == "error" || finding.Severity == "recovery" {
			return fail.New("UPGRADE_BLOCKED", "state upgrade requires a healthy doctor report: "+finding.Code)
		}
	}
	return nil
}

func bindUpgrade(material upgradeMaterial) (upgradeResult, error) {
	binding := upgradeBinding{
		SchemaVersion: upgradeSchemaVersion, Target: material.target, Path: material.path,
		CurrentSHA256: material.currentSHA256, DesiredSHA256: material.desiredSHA256,
		RevisionStore: material.revisionStore, PreviousRevisionPath: material.previousRevisionPath, DesiredRevisionPath: material.desiredRevisionPath,
	}
	body, err := json.Marshal(binding)
	if err != nil {
		return upgradeResult{}, err
	}
	return upgradeResult{
		SchemaVersion: upgradeSchemaVersion, Target: material.target, Path: material.path,
		CurrentSHA256: material.currentSHA256, DesiredSHA256: material.desiredSHA256,
		RevisionStore: material.revisionStore, PreviousRevisionPath: material.previousRevisionPath, DesiredRevisionPath: material.desiredRevisionPath,
		PlanDigest: digest(body), AlreadyCurrent: material.alreadyCurrent,
	}, nil
}

func inspectBinaryUpgrade(directory string) (upgradeMaterial, error) {
	path := filepath.Join(directory, binaryName())
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return upgradeMaterial{}, fail.New("UPGRADE_TARGET_MISSING", "installed binary does not exist; use wip init --install for first installation")
	}
	if err != nil {
		return upgradeMaterial{}, fail.Wrap("UPGRADE_FAILED", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return upgradeMaterial{}, fail.New("UPGRADE_CONFLICT", "installed binary is not a regular executable file")
	}
	if info.Size() > maxUpgradeBinarySize {
		return upgradeMaterial{}, fail.New("UPGRADE_CONFLICT", "installed binary exceeds the upgrade read bound")
	}
	current, err := safeio.ReadRegular(path, maxUpgradeBinarySize)
	if err != nil {
		return upgradeMaterial{}, fail.Wrap("UPGRADE_FAILED", err)
	}
	_, desired, err := selfExecutable()
	if err != nil {
		return upgradeMaterial{}, err
	}
	currentDigest, desiredDigest := digest(current), digest(desired)
	revisionStore := filepath.Join(directory, "."+binaryName()+".revisions")
	return upgradeMaterial{
		target: "binary", path: path, currentSHA256: currentDigest, desiredSHA256: desiredDigest,
		revisionStore:        revisionStore,
		previousRevisionPath: binaryRevisionPath(revisionStore, currentDigest),
		desiredRevisionPath:  binaryRevisionPath(revisionStore, desiredDigest),
		alreadyCurrent:       currentDigest == desiredDigest,
	}, nil
}

func applyBinaryUpgrade(reviewed upgradeMaterial) error {
	lock, err := filelock.Acquire(reviewed.path+".upgrade.lock", filelock.DefaultWait)
	if err != nil {
		return fail.Wrap("LOCK_TIMEOUT", err)
	}
	defer func() { _ = lock.Release() }()
	fresh, err := inspectBinaryUpgrade(filepath.Dir(reviewed.path))
	if err != nil {
		return err
	}
	if fresh.path != reviewed.path || fresh.currentSHA256 != reviewed.currentSHA256 || fresh.desiredSHA256 != reviewed.desiredSHA256 {
		return fail.New("UPGRADE_PLAN_MOVED", "installed binary changed after preview; review a new upgrade plan")
	}
	if fresh.alreadyCurrent {
		return nil
	}
	_, body, err := selfExecutable()
	if err != nil {
		return err
	}
	previous, err := safeio.ReadRegular(reviewed.path, maxUpgradeBinarySize)
	if err != nil {
		return fail.Wrap("UPGRADE_FAILED", err)
	}
	if digest(previous) != reviewed.currentSHA256 || digest(body) != reviewed.desiredSHA256 {
		return fail.New("UPGRADE_PLAN_MOVED", "upgrade bytes changed after the reviewed plan was revalidated")
	}
	if err := retainBinaryRevision(reviewed.revisionStore, reviewed.previousRevisionPath, previous, reviewed.currentSHA256); err != nil {
		return err
	}
	if err := retainBinaryRevision(reviewed.revisionStore, reviewed.desiredRevisionPath, body, reviewed.desiredSHA256); err != nil {
		return err
	}
	if err := atomicfile.Write(reviewed.path, body, 0o755); err != nil {
		return fail.Wrap("UPGRADE_FAILED", err)
	}
	verified, err := inspectBinaryUpgrade(filepath.Dir(reviewed.path))
	if err != nil || verified.currentSHA256 != reviewed.desiredSHA256 {
		if err != nil {
			return err
		}
		return fail.New("UPGRADE_FAILED", "installed binary verification failed after atomic replacement")
	}
	return nil
}

func binaryRevisionPath(store, value string) string {
	return filepath.Join(store, "sha256-"+strings.TrimPrefix(value, "sha256:"))
}

func retainBinaryRevision(store, path string, body []byte, expectedDigest string) error {
	if path != binaryRevisionPath(store, expectedDigest) {
		return fail.New("UPGRADE_FAILED", "binary revision path does not match its reviewed digest")
	}
	if err := ensureBinaryRevisionStore(store); err != nil {
		return err
	}
	if err := atomicfile.Create(path, body, 0o555); err != nil && !errors.Is(err, atomicfile.ErrExists) {
		return fail.Wrap("UPGRADE_FAILED", err)
	}
	retained, err := safeio.ReadRegular(path, maxUpgradeBinarySize)
	if err != nil {
		return fail.Wrap("UPGRADE_FAILED", err)
	}
	if digest(retained) != expectedDigest {
		return fail.New("UPGRADE_CONFLICT", "retained binary revision does not match its content-addressed path")
	}
	if err := ensureBinaryRevisionStore(store); err != nil {
		return err
	}
	return nil
}

func ensureBinaryRevisionStore(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fail.Wrap("UPGRADE_FAILED", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fail.Wrap("UPGRADE_FAILED", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail.New("UPGRADE_CONFLICT", "binary revision store is not a regular directory")
	}
	return nil
}

func inspectSkillUpgrade(directory string) (upgradeMaterial, error) {
	path := portableSkillPath(directory)
	files, err := readSkillSnapshot(path)
	if err != nil {
		return upgradeMaterial{}, err
	}
	currentDigest, err := skillbundle.DigestFiles(files)
	if err != nil {
		return upgradeMaterial{}, fail.Wrap("UPGRADE_FAILED", err)
	}
	desiredDigest, err := skillbundle.BundleDigest()
	if err != nil {
		return upgradeMaterial{}, fail.Wrap("UPGRADE_FAILED", err)
	}
	return upgradeMaterial{target: "skill", path: path, currentSHA256: currentDigest, desiredSHA256: desiredDigest, alreadyCurrent: currentDigest == desiredDigest}, nil
}

func readSkillSnapshot(path string) (map[string][]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fail.New("UPGRADE_TARGET_MISSING", "portable skill does not exist; use wip init --install-skill for first installation")
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if err != nil {
			return nil, fail.Wrap("UPGRADE_FAILED", err)
		}
		return nil, fail.New("UPGRADE_CONFLICT", "portable skill target is not a regular directory")
	}
	allowedFiles := map[string]bool{}
	allowedDirectories := map[string]bool{}
	for _, name := range skillbundle.Paths {
		allowedFiles[name] = true
		for directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))); directory != "."; directory = filepath.ToSlash(filepath.Dir(filepath.FromSlash(directory))) {
			allowedDirectories[directory] = true
		}
	}
	files := map[string][]byte{}
	entries := 0
	err = filepath.WalkDir(path, func(candidate string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if candidate == path {
			return nil
		}
		entries++
		if entries > maxSkillEntries {
			return fail.Errorf("UPGRADE_CONFLICT", "portable skill exceeds %d entries", maxSkillEntries)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fail.New("UPGRADE_CONFLICT", "portable skill contains a symlink")
		}
		relative, relErr := filepath.Rel(path, candidate)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if !allowedDirectories[relative] {
				return fail.New("UPGRADE_CONFLICT", "portable skill has an unexpected directory: "+relative)
			}
			return nil
		}
		if !allowedFiles[relative] {
			return fail.New("UPGRADE_CONFLICT", "portable skill has an unexpected file: "+relative)
		}
		body, readErr := safeio.ReadRegular(candidate, maxPortableSkillFileSize)
		if readErr != nil {
			return readErr
		}
		files[relative] = body
		return nil
	})
	if err != nil {
		var typed *fail.Error
		if errors.As(err, &typed) {
			return nil, err
		}
		return nil, fail.Wrap("UPGRADE_FAILED", err)
	}
	for _, required := range skillbundle.PayloadPaths {
		if _, ok := files[required]; !ok {
			return nil, fail.New("UPGRADE_CONFLICT", "portable skill is missing required file: "+required)
		}
	}
	return files, nil
}

func applySkillUpgrade(reviewed upgradeMaterial) error {
	lockPath := filepath.Join(filepath.Dir(reviewed.path), ".wip-commit-skill-upgrade.lock")
	lock, err := filelock.Acquire(lockPath, filelock.DefaultWait)
	if err != nil {
		return fail.Wrap("LOCK_TIMEOUT", err)
	}
	defer func() { _ = lock.Release() }()
	fresh, err := inspectSkillUpgrade(filepath.Dir(reviewed.path))
	if err != nil {
		return err
	}
	if fresh.path != reviewed.path || fresh.currentSHA256 != reviewed.currentSHA256 || fresh.desiredSHA256 != reviewed.desiredSHA256 {
		return fail.New("UPGRADE_PLAN_MOVED", "portable skill changed after preview; review a new upgrade plan")
	}
	if fresh.alreadyCurrent {
		return nil
	}
	for _, name := range skillbundle.Paths {
		body, err := skillbundle.FS.ReadFile(name)
		if err != nil {
			return fail.Wrap("UPGRADE_FAILED", err)
		}
		if err := atomicfile.Write(filepath.Join(reviewed.path, filepath.FromSlash(name)), body, 0o644); err != nil {
			return fail.Wrap("UPGRADE_FAILED", err)
		}
	}
	verified, err := inspectSkillUpgrade(filepath.Dir(reviewed.path))
	if err != nil || verified.currentSHA256 != reviewed.desiredSHA256 {
		if err != nil {
			return err
		}
		return fail.New("UPGRADE_FAILED", "portable skill verification failed after replacement")
	}
	return nil
}

func normalizedUpgradeDirectory(value string, defaults ...string) (string, error) {
	if value == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fail.Wrap("INVALID_PATH", err)
		}
		parts := append([]string{home}, defaults...)
		value = filepath.Join(parts...)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fail.Wrap("INVALID_PATH", err)
	}
	return filepath.Clean(absolute), nil
}

func digest(body []byte) string {
	value := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(value[:])
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && value == strings.ToLower(value)
}

func formatUpgradeResult(result upgradeResult) string {
	state := "preview"
	if result.AlreadyCurrent {
		state = "already current"
	} else if result.Applied {
		state = "applied"
	}
	return fmt.Sprintf("%s upgrade %s: %s\nplan digest: %s", result.Target, state, result.Path, result.PlanDigest)
}
