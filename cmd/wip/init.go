package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/safeio"
	"github.com/nstranquist/wip-commit/internal/store"
)

type initOptions struct {
	mode, lane, agent, session, baseRef  string
	createWorktree, installDir, skillDir string
	paths                                stringList
	nonInteractive, yes, dryRun          bool
	install, noInstall                   bool
	installSkill, noInstallSkill         bool
}

type initResult struct {
	Version             string       `json:"version"`
	DryRun              bool         `json:"dry_run"`
	RepoDir             string       `json:"repo_dir"`
	BaseSHA             string       `json:"base_sha"`
	Lane                *store.Lane  `json:"lane,omitempty"`
	Lease               *store.Lease `json:"lease,omitempty"`
	ClaimedPaths        []string     `json:"claimed_paths"`
	ProfilePath         string       `json:"profile_path,omitempty"`
	Worktree            string       `json:"worktree"`
	CreatedWorktree     bool         `json:"created_worktree"`
	InstallPath         string       `json:"install_path,omitempty"`
	Installed           bool         `json:"installed"`
	InstallAlreadyValid bool         `json:"install_already_valid"`
	InstallDirOnPath    bool         `json:"install_dir_on_path"`
	SkillPath           string       `json:"skill_path,omitempty"`
	SkillInstalled      bool         `json:"skill_installed"`
	SkillAlreadyValid   bool         `json:"skill_already_valid"`
	StagedPaths         []string     `json:"staged_paths"`
	DiffCheckRun        bool         `json:"diff_check_run"`
	DiffCheckPassed     bool         `json:"diff_check_passed"`
	Environment         string       `json:"environment"`
	IntentID            string       `json:"intent_id,omitempty"`
	IntentPath          string       `json:"intent_path,omitempty"`
	IntentState         string       `json:"intent_state,omitempty"`
	CompletedSteps      []string     `json:"completed_steps,omitempty"`
	Recovery            []string     `json:"recovery,omitempty"`
}

type prompt struct {
	reader *bufio.Reader
	out    io.Writer
}

func (application app) runInit(ctx context.Context, source gitx.Repo, args []string) int {
	set := application.flagSet("init")
	options := initOptions{}
	set.StringVar(&options.mode, "mode", "", "shared or worktree")
	set.StringVar(&options.lane, "lane", envDefault("WIP_LANE"), "short task lane id")
	set.StringVar(&options.agent, "agent", envDefault("WIP_AGENT"), "agent id")
	set.StringVar(&options.session, "session", envDefault("WIP_SESSION"), "session id")
	set.StringVar(&options.baseRef, "base-ref", "HEAD", "commit from which the lane starts")
	set.StringVar(&options.createWorktree, "create-worktree", "", "create or reuse this linked worktree")
	set.Var(&options.paths, "path", "repository-relative path to claim (repeatable)")
	set.BoolVar(&options.nonInteractive, "non-interactive", false, "do not prompt; require explicit lane and paths")
	set.BoolVar(&options.yes, "yes", false, "accept the displayed setup")
	set.BoolVar(&options.dryRun, "dry-run", false, "validate and show setup without changing Git or installing")
	set.BoolVar(&options.install, "install", false, "copy this executable into --install-dir without overwriting")
	set.BoolVar(&options.noInstall, "no-install", false, "do not offer to install the executable")
	set.StringVar(&options.installDir, "install-dir", "", "binary install directory (default ~/.local/bin)")
	set.BoolVar(&options.installSkill, "install-skill", false, "install the embedded portable skill without overwriting")
	set.BoolVar(&options.noInstallSkill, "no-install-skill", false, "do not offer to install the portable skill")
	set.StringVar(&options.skillDir, "skill-dir", "", "agent skill directory (default ~/.agents/skills)")
	if err := set.Parse(args); err != nil {
		return application.failure("init", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("init", err, nil, 2)
	}
	if options.install && options.noInstall {
		return application.failure("init", fail.New("INVALID_ARGS", "--install and --no-install cannot be combined"), nil, 2)
	}
	if options.installSkill && options.noInstallSkill {
		return application.failure("init", fail.New("INVALID_ARGS", "--install-skill and --no-install-skill cannot be combined"), nil, 2)
	}
	if options.nonInteractive && !options.yes {
		options.yes = true
	}
	promptOutput := application.stdout
	if application.jsonMode {
		promptOutput = application.stderr
	}
	prompter := prompt{reader: bufio.NewReader(application.stdin), out: promptOutput}
	if err := prepareInitOptions(ctx, source, &options, prompter); err != nil {
		return application.failure("init", err, nil, 2)
	}
	result, err := executeInit(ctx, source, options, prompter, promptOutput)
	if err != nil {
		code := application.failure("init", err, result, 1)
		if !application.jsonMode && len(result.Recovery) > 0 {
			_, _ = fmt.Fprintln(application.stderr, formatInitRecovery(result))
		}
		return code
	}
	human := formatInitResult(result)
	return application.success("init", result, human)
}

func prepareInitOptions(ctx context.Context, source gitx.Repo, options *initOptions, prompter prompt) error {
	if options.mode == "" {
		defaultMode := string(store.ModeShared)
		if source.GitDir != source.CommonDir {
			defaultMode = string(store.ModeWorktree)
		}
		if options.nonInteractive {
			options.mode = defaultMode
		} else {
			value, err := prompter.ask("Lane mode (shared/worktree)", defaultMode)
			if err != nil {
				return err
			}
			options.mode = value
		}
	}
	if options.mode != string(store.ModeShared) && options.mode != string(store.ModeWorktree) {
		return fail.New("INVALID_MODE", "--mode must be shared or worktree")
	}
	if options.createWorktree != "" && options.mode != string(store.ModeWorktree) {
		return fail.New("INVALID_ARGS", "--create-worktree requires --mode worktree")
	}
	if options.mode == string(store.ModeWorktree) && source.GitDir == source.CommonDir && options.createWorktree == "" {
		if options.nonInteractive {
			return fail.New("WORKTREE_REQUIRED", "worktree mode from an anchor checkout requires --create-worktree")
		}
		value, err := prompter.ask("New linked worktree path", "")
		if err != nil {
			return err
		}
		options.createWorktree = value
	}
	if !options.nonInteractive {
		value, err := prompter.ask("Base ref", options.baseRef)
		if err != nil {
			return err
		}
		options.baseRef = value
	}
	if strings.TrimSpace(options.baseRef) == "" {
		return fail.New("INVALID_ARGS", "--base-ref cannot be empty")
	}
	if _, err := source.Text(ctx, nil, "rev-parse", "--verify", options.baseRef+"^{commit}"); err != nil {
		return fail.Wrap("BASE_NOT_FOUND", err)
	}
	if options.agent == "" {
		options.agent = defaultAgent(ctx, source)
	}
	if !options.nonInteractive {
		value, err := prompter.ask("Agent id", options.agent)
		if err != nil {
			return err
		}
		options.agent = value
	}
	if options.session == "" {
		options.session = fmt.Sprintf("session-%s-%d", time.Now().UTC().Format("20060102-150405"), os.Getpid())
	}
	if !options.nonInteractive {
		value, err := prompter.ask("Session id", options.session)
		if err != nil {
			return err
		}
		options.session = value
	}
	if options.lane == "" {
		if options.nonInteractive {
			return fail.New("INVALID_ARGS", "--lane is required in non-interactive mode")
		}
		value, err := prompter.ask("Lane name (short task slug)", "work-"+time.Now().UTC().Format("20060102-1504"))
		if err != nil {
			return err
		}
		options.lane = value
	} else if !options.nonInteractive {
		value, err := prompter.ask("Lane name (short task slug)", options.lane)
		if err != nil {
			return err
		}
		options.lane = value
	}
	if len(options.paths) == 0 {
		if options.nonInteractive {
			return fail.New("INVALID_ARGS", "at least one --path is required in non-interactive mode")
		}
		staged, stagedErr := source.NULPaths(ctx, nil, "diff", "--cached", "--no-renames", "--name-only", "-z")
		if stagedErr != nil {
			return fail.Wrap("GIT_FAILED", stagedErr)
		}
		if len(staged) > 0 {
			sort.Strings(staged)
			_, _ = fmt.Fprintln(prompter.out, "Staged paths are suggestions only. Confirm ownership one path at a time:")
			for _, path := range staged {
				accept, err := prompter.confirm("Claim "+path, false)
				if err != nil {
					return err
				}
				if accept {
					options.paths = append(options.paths, path)
				}
			}
		}
		for len(options.paths) == 0 {
			_, _ = fmt.Fprintln(prompter.out, "Enter owned paths one at a time. A blank line finishes the list.")
			for {
				value, err := prompter.ask("Path to claim", "")
				if err != nil {
					return err
				}
				if value == "" {
					break
				}
				options.paths = append(options.paths, value)
			}
			if len(options.paths) == 0 {
				_, _ = fmt.Fprintln(prompter.out, "At least one explicit path is required.")
			}
		}
	}
	offerInstall := !options.install && !options.noInstall && !options.nonInteractive
	installMissing := false
	if offerInstall {
		_, lookupErr := exec.LookPath(binaryName())
		installMissing = lookupErr != nil
	}
	if options.install || installMissing {
		if options.installDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fail.Wrap("INSTALL_FAILED", err)
			}
			options.installDir = filepath.Join(home, ".local", "bin")
		}
		absoluteInstallDir, err := filepath.Abs(options.installDir)
		if err != nil {
			return fail.Wrap("INSTALL_FAILED", err)
		}
		options.installDir = filepath.Clean(absoluteInstallDir)
	}
	offerSkill := !options.installSkill && !options.noInstallSkill && !options.nonInteractive
	if options.installSkill || offerSkill {
		if options.skillDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fail.Wrap("SKILL_INSTALL_FAILED", err)
			}
			options.skillDir = filepath.Join(home, ".agents", "skills")
		}
		absoluteSkillDir, err := filepath.Abs(options.skillDir)
		if err != nil {
			return fail.Wrap("SKILL_INSTALL_FAILED", err)
		}
		options.skillDir = filepath.Clean(absoluteSkillDir)
	}
	if options.createWorktree != "" {
		absoluteWorktree, err := filepath.Abs(options.createWorktree)
		if err != nil {
			return fail.Wrap("INVALID_PATH", err)
		}
		options.createWorktree = filepath.Clean(absoluteWorktree)
	}
	if installMissing {
		install, promptErr := prompter.confirm("Install wip in "+options.installDir, false)
		if promptErr != nil {
			return promptErr
		}
		options.install = install
	}
	if offerSkill {
		_, exists, valid, inspectErr := inspectPortableSkill(options.skillDir)
		if inspectErr != nil {
			if fail.Code(inspectErr) != "SKILL_INSTALL_CONFLICT" {
				return inspectErr
			}
			_, _ = fmt.Fprintln(prompter.out, "Portable skill installation is unavailable: "+inspectErr.Error())
			proceed, promptErr := prompter.confirm("Continue without portable skill installation", true)
			if promptErr != nil {
				return promptErr
			}
			if !proceed {
				return inspectErr
			}
			options.noInstallSkill = true
		} else if exists && valid {
			options.installSkill = true
		} else {
			install, promptErr := prompter.confirm("Install portable wip-commit skill in "+options.skillDir, true)
			if promptErr != nil {
				return promptErr
			}
			options.installSkill = install
		}
	}
	return nil
}

func executeInit(ctx context.Context, source gitx.Repo, options initOptions, prompter prompt, output io.Writer) (result initResult, returnErr error) {
	result = initResult{Version: version, DryRun: options.dryRun, RepoDir: source.Root, Worktree: source.Root, StagedPaths: []string{}}
	defer func() {
		if returnErr != nil && result.IntentID != "" && result.IntentState == "pending" {
			result.Recovery = initRecovery(result, options)
		}
	}()
	for label, value := range map[string]string{"lane": options.lane, "agent": options.agent, "session": options.session} {
		if err := store.ValidateID(value, label); err != nil {
			return result, err
		}
	}
	if _, err := source.Text(ctx, nil, "check-ref-format", store.LaneRef(options.agent, options.lane)); err != nil {
		return result, fail.Wrap("INVALID_REF", err)
	}
	normalized, err := source.NormalizePaths(options.paths)
	if err != nil {
		return result, err
	}
	result.ClaimedPaths = normalized
	if !options.yes {
		_, _ = fmt.Fprintf(output, "\nSetup summary:\n  mode: %s\n  lane: %s\n  agent: %s\n  session: %s\n  base: %s\n  paths: %s\n", options.mode, options.lane, options.agent, options.session, options.baseRef, strings.Join(normalized, ", "))
		if options.createWorktree != "" {
			_, _ = fmt.Fprintf(output, "  create worktree: %s\n", options.createWorktree)
		}
		if options.install {
			_, _ = fmt.Fprintf(output, "  install: %s\n", filepath.Join(options.installDir, binaryName()))
		}
		if options.installSkill {
			_, _ = fmt.Fprintf(output, "  install skill: %s\n", portableSkillPath(options.skillDir))
		}
		confirmed, confirmErr := prompter.confirm("Apply this setup", false)
		if confirmErr != nil {
			return result, confirmErr
		}
		if !confirmed {
			return result, fail.New("CANCELLED", "setup was not applied")
		}
	}
	baseSHA, err := source.Text(ctx, nil, "rev-parse", "--verify", options.baseRef+"^{commit}")
	if err != nil {
		return result, fail.Wrap("BASE_NOT_FOUND", err)
	}
	result.BaseSHA = baseSHA
	if options.dryRun {
		if err := store.Check(source); err != nil {
			return result, err
		}
		target := source
		targetExists := true
		if options.createWorktree != "" {
			target, targetExists, err = inspectWorktree(ctx, source, options.createWorktree, baseSHA)
			if err != nil {
				return result, err
			}
			result.Worktree = target.Root
			if !targetExists {
				result.Worktree, _ = filepath.Abs(options.createWorktree)
			}
		} else {
			head, headErr := source.Text(ctx, nil, "rev-parse", "HEAD")
			if headErr != nil || head != baseSHA {
				return result, fail.New("WORKTREE_BASE_MISMATCH", "current checkout HEAD does not match the requested base")
			}
		}
		if targetExists {
			if err := inspectStagedGit(ctx, target, &result); err != nil {
				return result, err
			}
		}
		result.Environment = shellEnvironment(profile{SchemaVersion: profileSchemaVersion, Lane: options.lane, Agent: options.agent, Session: options.session, Mode: store.Mode(options.mode), Worktree: result.Worktree})
		if options.install {
			result.InstallPath, _, result.InstallAlreadyValid, err = inspectSelfInstall(options.installDir)
			if err != nil {
				return result, err
			}
			result.InstallDirOnPath = directoryOnPath(options.installDir)
		}
		if options.installSkill {
			result.SkillPath, _, result.SkillAlreadyValid, err = inspectPortableSkill(options.skillDir)
			if err != nil {
				return result, err
			}
		}
		return result, nil
	}
	if options.install {
		result.InstallPath, _, result.InstallAlreadyValid, err = inspectSelfInstall(options.installDir)
		if err != nil {
			return result, err
		}
	}
	if options.installSkill {
		result.SkillPath, _, result.SkillAlreadyValid, err = inspectPortableSkill(options.skillDir)
		if err != nil {
			return result, err
		}
	}
	plannedWorktree := source.Root
	if options.createWorktree != "" {
		plannedWorktree = options.createWorktree
	}
	result.Worktree = plannedWorktree
	coordinationStore, err := store.Open(source)
	if err != nil {
		return result, err
	}
	candidateIntent := initIntent{Lane: options.lane, Agent: options.agent, Session: options.session, Mode: store.Mode(options.mode), BaseSHA: baseSHA, Worktree: plannedWorktree, Paths: normalized}
	if options.install {
		candidateIntent.InstallPath = filepath.Join(options.installDir, binaryName())
	}
	if options.installSkill {
		candidateIntent.SkillPath = portableSkillPath(options.skillDir)
	}
	intent, intentPath, err := beginInitIntent(coordinationStore, candidateIntent)
	if err != nil {
		return result, err
	}
	updateResultIntent(&result, intent, intentPath)
	markStep := func(step string) error {
		updated, markErr := markInitStep(intentPath, intent, step)
		if markErr != nil {
			return markErr
		}
		intent = updated
		updateResultIntent(&result, intent, intentPath)
		return nil
	}
	target := source
	if options.createWorktree != "" {
		var created bool
		target, created, err = ensureWorktree(ctx, source, options.createWorktree, baseSHA)
		if err != nil {
			return result, err
		}
		result.CreatedWorktree = created
	}
	result.Worktree = target.Root
	if err := markStep("worktree-ready"); err != nil {
		return result, err
	}
	laneStore, err := store.Open(target)
	if err != nil {
		return result, err
	}
	lane, err := ensureLane(ctx, laneStore, options, baseSHA)
	if err != nil {
		return result, err
	}
	result.Lane = &lane
	if err := markStep("lane-ready"); err != nil {
		return result, err
	}
	lease, err := laneStore.Claim(lane.ID, lane.Agent, lane.Session, normalized)
	if err != nil {
		return result, err
	}
	result.Lease = &lease
	if err := markStep("lease-ready"); err != nil {
		return result, err
	}
	result.ProfilePath, err = writeProfile(laneStore, lane)
	if err != nil {
		return result, err
	}
	if err := markStep("profile-ready"); err != nil {
		return result, err
	}
	saved := profile{SchemaVersion: profileSchemaVersion, Lane: lane.ID, Agent: lane.Agent, Session: lane.Session, Mode: lane.Mode, Worktree: lane.Worktree}
	result.Environment = shellEnvironment(saved)
	if err := inspectStagedGit(ctx, target, &result); err != nil {
		return result, err
	}
	if options.install {
		result.InstallPath, result.Installed, result.InstallAlreadyValid, err = installSelf(options.installDir)
		if err != nil {
			return result, err
		}
		result.InstallDirOnPath = directoryOnPath(options.installDir)
	}
	if err := markStep("binary-ready"); err != nil {
		return result, err
	}
	if options.installSkill {
		result.SkillPath, result.SkillInstalled, result.SkillAlreadyValid, err = installPortableSkill(options.skillDir)
		if err != nil {
			return result, err
		}
	}
	if err := markStep("skill-ready"); err != nil {
		return result, err
	}
	return result, nil
}

func ensureLane(ctx context.Context, laneStore store.Store, options initOptions, baseSHA string) (store.Lane, error) {
	wanted := store.CreateOptions{ID: options.lane, Agent: options.agent, Session: options.session, BaseRef: baseSHA, Mode: store.Mode(options.mode), Worktree: laneStore.Repo.Root}
	existing, err := laneStore.Load(options.lane)
	if err != nil && fail.Code(err) == "LANE_NOT_FOUND" {
		return laneStore.Create(ctx, wanted)
	}
	if err != nil {
		return store.Lane{}, err
	}
	if existing.State != "active" || existing.Agent != options.agent || existing.Session != options.session || existing.Mode != store.Mode(options.mode) || existing.BaseSHA != baseSHA || existing.Worktree != laneStore.Repo.Root {
		return store.Lane{}, fail.New("LANE_EXISTS", "existing lane does not match this initialization")
	}
	head, err := laneStore.Repo.Text(ctx, nil, "rev-parse", "HEAD")
	if err != nil || head != existing.BaseSHA {
		return store.Lane{}, fail.New("SOURCE_HEAD_MOVED", "source HEAD moved from the lane base")
	}
	return existing, nil
}

func ensureWorktree(ctx context.Context, source gitx.Repo, requested, baseRef string) (gitx.Repo, bool, error) {
	repo, exists, err := inspectWorktree(ctx, source, requested, baseRef)
	if err != nil {
		return gitx.Repo{}, false, err
	}
	if exists {
		return repo, false, nil
	}
	target := repo.Root
	if _, err := source.Text(ctx, nil, "worktree", "add", "--detach", target, baseRef); err != nil {
		if concurrent, exists, inspectErr := inspectWorktree(ctx, source, requested, baseRef); inspectErr == nil && exists {
			return concurrent, false, nil
		}
		return gitx.Repo{}, false, fail.Wrap("WORKTREE_CREATE_FAILED", err)
	}
	repo, discoverErr := gitx.Discover(ctx, target)
	if discoverErr != nil {
		return gitx.Repo{}, true, fail.Wrap("WORKTREE_NOT_REGISTERED", discoverErr)
	}
	return repo, true, nil
}

func inspectWorktree(ctx context.Context, source gitx.Repo, requested, baseRef string) (gitx.Repo, bool, error) {
	target, err := filepath.Abs(requested)
	if err != nil {
		return gitx.Repo{}, false, fail.Wrap("INVALID_PATH", err)
	}
	target = filepath.Clean(target)
	if relative, relErr := filepath.Rel(source.Root, target); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return gitx.Repo{}, false, fail.New("INVALID_PATH", "linked worktree must be outside the current repository root")
	}
	if _, statErr := os.Lstat(target); errors.Is(statErr, os.ErrNotExist) {
		return gitx.Repo{Root: target, CommonDir: source.CommonDir}, false, nil
	} else if statErr != nil {
		return gitx.Repo{}, false, fail.Wrap("INVALID_PATH", statErr)
	}
	repo, err := gitx.Discover(ctx, target)
	if err != nil || repo.CommonDir != source.CommonDir || repo.GitDir == repo.CommonDir {
		return gitx.Repo{}, false, fail.New("WORKTREE_CONFLICT", "existing target is not a linked worktree for this repository")
	}
	base, baseErr := source.Text(ctx, nil, "rev-parse", "--verify", baseRef+"^{commit}")
	head, headErr := repo.Text(ctx, nil, "rev-parse", "HEAD")
	if baseErr != nil || headErr != nil || head != base {
		return gitx.Repo{}, false, fail.New("WORKTREE_BASE_MISMATCH", "existing linked worktree HEAD does not match the requested base")
	}
	return repo, true, nil
}

func installSelf(directory string) (path string, installed, alreadyValid bool, err error) {
	path, exists, valid, err := inspectSelfInstall(directory)
	if err != nil {
		return path, false, false, err
	}
	if exists {
		return path, false, valid, nil
	}
	_, body, err := selfExecutable()
	if err != nil {
		return path, false, false, err
	}
	if err := atomicfile.Create(path, body, 0o755); err != nil {
		if errors.Is(err, atomicfile.ErrExists) {
			_, exists, valid, inspectErr := inspectSelfInstall(directory)
			if inspectErr == nil && exists && valid {
				return path, false, true, nil
			}
			if inspectErr != nil {
				return path, false, false, inspectErr
			}
			return path, false, false, fail.New("INSTALL_CONFLICT", "install target appeared during installation")
		}
		return path, false, false, fail.Wrap("INSTALL_FAILED", err)
	}
	if _, _, valid, err := inspectSelfInstall(directory); err != nil || !valid {
		if err == nil {
			err = fail.New("INSTALL_FAILED", "installed binary verification failed")
		}
		return path, false, false, err
	}
	return path, true, false, nil
}

func inspectSelfInstall(directory string) (path string, exists, valid bool, err error) {
	_, body, err := selfExecutable()
	if err != nil {
		return "", false, false, err
	}
	path = filepath.Join(directory, binaryName())
	info, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return path, false, false, nil
	}
	if statErr != nil {
		return path, false, false, fail.Wrap("INSTALL_FAILED", statErr)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return path, true, false, fail.New("INSTALL_CONFLICT", "install target exists and is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return path, true, false, fail.New("INSTALL_CONFLICT", "install target is not executable; refusing to change it")
	}
	existing, readErr := safeio.ReadRegular(path, int64(len(body))+1)
	if readErr != nil {
		return path, true, false, fail.Wrap("INSTALL_FAILED", readErr)
	}
	left, right := sha256.Sum256(existing), sha256.Sum256(body)
	if left != right {
		return path, true, false, fail.New("INSTALL_CONFLICT", "install target exists with different content; refusing to overwrite")
	}
	return path, true, true, nil
}

func selfExecutable() (string, []byte, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", nil, fail.Wrap("INSTALL_FAILED", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", nil, fail.Wrap("INSTALL_FAILED", err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return "", nil, fail.Wrap("INSTALL_FAILED", err)
	}
	body, err := safeio.ReadRegular(executable, info.Size()+1)
	if err != nil {
		return "", nil, fail.Wrap("INSTALL_FAILED", err)
	}
	return executable, body, nil
}

func (question prompt) ask(label, fallback string) (string, error) {
	if fallback == "" {
		_, _ = fmt.Fprintf(question.out, "%s: ", label)
	} else {
		_, _ = fmt.Fprintf(question.out, "%s [%s]: ", label, fallback)
	}
	value, err := question.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fail.Wrap("INPUT_FAILED", err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if errors.Is(err, io.EOF) && value == "" {
		return "", fail.New("INPUT_FAILED", "input ended before setup was complete")
	}
	return value, nil
}

func (question prompt) confirm(label string, fallback bool) (bool, error) {
	suffix := "[y/N]"
	if fallback {
		suffix = "[Y/n]"
	}
	for {
		value, err := question.ask(label+" "+suffix, "")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(value) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "":
			return fallback, nil
		default:
			_, _ = fmt.Fprintln(question.out, "Enter yes or no.")
		}
	}
}

func defaultAgent(ctx context.Context, repo gitx.Repo) string {
	if value, err := repo.Text(ctx, nil, "config", "--get", "user.name"); err == nil {
		if value = sanitizeID(value); value != "" {
			return value
		}
	}
	for _, variable := range []string{"USER", "USERNAME"} {
		if value := sanitizeID(os.Getenv(variable)); value != "" {
			return value
		}
	}
	return "agent"
}

func sanitizeID(value string) string {
	var output strings.Builder
	separator := false
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if character <= unicode.MaxASCII && (character >= 'a' && character <= 'z' || character >= '0' && character <= '9') {
			if separator && output.Len() > 0 {
				output.WriteByte('-')
			}
			output.WriteRune(character)
			separator = false
		} else {
			separator = true
		}
		if output.Len() >= 32 {
			break
		}
	}
	return strings.Trim(output.String(), "-")
}

func directoryOnPath(directory string) bool {
	wanted, err := filepath.Abs(directory)
	if err != nil {
		return false
	}
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		candidate, candidateErr := filepath.Abs(entry)
		if candidateErr == nil && candidate == wanted {
			return true
		}
	}
	return false
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "wip.exe"
	}
	return "wip"
}

func formatInitResult(result initResult) string {
	if result.DryRun {
		status := "Git setup validation passed."
		if !result.DiffCheckRun {
			status = "Git setup validation completed. The staged diff check did not run because the target worktree does not exist."
		} else if !result.DiffCheckPassed {
			status = "Git setup validation found staged whitespace errors. Fix them before capture."
		}
		return status + " Installation was not attempted. No files, refs, worktrees, or binaries were changed.\n\n" + result.Environment
	}
	var lines []string
	if result.Lane != nil {
		lines = append(lines, fmt.Sprintf("Initialized %s lane %s on %s.", result.Lane.Mode, result.Lane.ID, result.Lane.Ref))
	}
	if result.CreatedWorktree {
		lines = append(lines, "Created linked worktree "+result.Worktree+".")
	}
	if result.InstallPath != "" {
		state := "Installed"
		if result.InstallAlreadyValid {
			state = "Verified existing"
		}
		lines = append(lines, state+" binary at "+result.InstallPath+".")
		if !result.InstallDirOnPath {
			lines = append(lines, "Add "+filepath.Dir(result.InstallPath)+" to PATH.")
		}
	}
	if result.SkillPath != "" {
		state := "Installed"
		if result.SkillAlreadyValid {
			state = "Verified existing"
		}
		lines = append(lines, state+" portable skill at "+result.SkillPath+".")
	}
	if result.DiffCheckRun && !result.DiffCheckPassed {
		lines = append(lines, "The staged diff has whitespace errors. Fix them before capture.")
	}
	lines = append(lines, "Use this environment in the agent shell:", result.Environment, "Then run: wip commit")
	return strings.Join(lines, "\n")
}

func inspectStagedGit(ctx context.Context, repo gitx.Repo, result *initResult) error {
	staged, err := repo.NULPaths(ctx, nil, "diff", "--cached", "--no-renames", "--name-only", "-z")
	if err != nil {
		return fail.Wrap("GIT_FAILED", err)
	}
	result.StagedPaths = append([]string{}, staged...)
	sort.Strings(result.StagedPaths)
	result.DiffCheckRun = true
	result.DiffCheckPassed, err = repo.CheckStagedDiff(ctx)
	if err != nil {
		return fail.Wrap("DIFF_CHECK_FAILED", err)
	}
	return nil
}

func updateResultIntent(result *initResult, intent initIntent, path string) {
	result.IntentID = intent.ID
	result.IntentPath = path
	result.IntentState = intent.State
	result.CompletedSteps = append([]string(nil), intent.CompletedSteps...)
}

func initRecovery(result initResult, options initOptions) []string {
	steps := "none"
	if len(result.CompletedSteps) > 0 {
		steps = strings.Join(result.CompletedSteps, ", ")
	}
	command := "wip"
	if result.RepoDir != "" {
		command += " --repo-dir " + quoteCommandArg(result.RepoDir)
	}
	command += " init"
	baseRef := options.baseRef
	if result.BaseSHA != "" {
		baseRef = result.BaseSHA
	}
	for _, option := range []struct {
		flag  string
		value string
	}{
		{flag: "--mode", value: string(options.mode)},
		{flag: "--lane", value: options.lane},
		{flag: "--agent", value: options.agent},
		{flag: "--session", value: options.session},
		{flag: "--base-ref", value: baseRef},
	} {
		command += " " + option.flag + " " + quoteCommandArg(option.value)
	}
	paths := []string(options.paths)
	if len(result.ClaimedPaths) > 0 {
		paths = result.ClaimedPaths
	}
	for _, path := range paths {
		command += " --path " + quoteCommandArg(path)
	}
	if options.createWorktree != "" {
		command += " --create-worktree " + quoteCommandArg(options.createWorktree)
	}
	if options.install {
		command += " --install --install-dir " + quoteCommandArg(options.installDir)
	} else {
		command += " --no-install"
	}
	if options.installSkill {
		command += " --install-skill --skill-dir " + quoteCommandArg(options.skillDir)
	} else {
		command += " --no-install-skill"
	}
	command += " --non-interactive"
	return []string{
		"Initialization is resumable; preserve the worktree, lane ref, leases, profile, and installed files.",
		"Completed steps: " + steps + ".",
		"Rerun the same immutable setup: " + command,
	}
}

func formatInitRecovery(result initResult) string {
	lines := []string{"Initialization receipt: " + result.IntentPath}
	lines = append(lines, result.Recovery...)
	return strings.Join(lines, "\n")
}
