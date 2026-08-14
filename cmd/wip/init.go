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

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/pathid"
	"github.com/nstranquist/wip-commit/internal/store"
)

type initOptions struct {
	mode, lane, agent, session, baseRef string
	createWorktree, installDir          string
	paths                               stringList
	nonInteractive, yes, dryRun         bool
	install, noInstall                  bool
}

type initResult struct {
	Version             string       `json:"version"`
	DryRun              bool         `json:"dry_run"`
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
	StagedPaths         []string     `json:"staged_paths"`
	DiffCheckPassed     bool         `json:"diff_check_passed"`
	Environment         string       `json:"environment"`
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
	if err := set.Parse(args); err != nil {
		return application.failure("init", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("init", err, nil, 2)
	}
	if options.install && options.noInstall {
		return application.failure("init", fail.New("INVALID_ARGS", "--install and --no-install cannot be combined"), nil, 2)
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
		return application.failure("init", err, result, 1)
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
		staged, _ := source.NULPaths(ctx, nil, "diff", "--cached", "--no-renames", "--name-only", "-z")
		if len(staged) > 0 {
			sort.Strings(staged)
			fmt.Fprintln(prompter.out, "Staged paths are suggestions only. Confirm ownership one path at a time:")
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
			fmt.Fprintln(prompter.out, "Enter owned paths one at a time. A blank line finishes the list.")
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
				fmt.Fprintln(prompter.out, "At least one explicit path is required.")
			}
		}
	}
	if options.installDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fail.Wrap("INSTALL_FAILED", err)
		}
		options.installDir = filepath.Join(home, ".local", "bin")
	}
	if !options.install && !options.noInstall && !options.nonInteractive {
		if _, err := exec.LookPath(binaryName()); err != nil {
			install, promptErr := prompter.confirm("Install wip in "+options.installDir, false)
			if promptErr != nil {
				return promptErr
			}
			options.install = install
		}
	}
	return nil
}

func executeInit(ctx context.Context, source gitx.Repo, options initOptions, prompter prompt, output io.Writer) (initResult, error) {
	result := initResult{Version: version, DryRun: options.dryRun, Worktree: source.Root, StagedPaths: []string{}}
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
		fmt.Fprintf(output, "\nSetup summary:\n  mode: %s\n  lane: %s\n  agent: %s\n  session: %s\n  base: %s\n  paths: %s\n", options.mode, options.lane, options.agent, options.session, options.baseRef, strings.Join(normalized, ", "))
		if options.createWorktree != "" {
			fmt.Fprintf(output, "  create worktree: %s\n", options.createWorktree)
		}
		if options.install {
			fmt.Fprintf(output, "  install: %s\n", filepath.Join(options.installDir, binaryName()))
		}
		confirmed, confirmErr := prompter.confirm("Apply this setup", false)
		if confirmErr != nil {
			return result, confirmErr
		}
		if !confirmed {
			return result, fail.New("CANCELLED", "setup was not applied")
		}
	}
	if options.dryRun {
		target := source
		targetExists := true
		if options.createWorktree != "" {
			target, targetExists, err = inspectWorktree(ctx, source, options.createWorktree, options.baseRef)
			if err != nil {
				return result, err
			}
			result.Worktree = target.Root
			if !targetExists {
				result.Worktree, _ = filepath.Abs(options.createWorktree)
			}
		} else {
			base, baseErr := source.Text(ctx, nil, "rev-parse", "--verify", options.baseRef+"^{commit}")
			head, headErr := source.Text(ctx, nil, "rev-parse", "HEAD")
			if baseErr != nil || headErr != nil || head != base {
				return result, fail.New("WORKTREE_BASE_MISMATCH", "current checkout HEAD does not match the requested base")
			}
		}
		if targetExists {
			result.StagedPaths, _ = target.NULPaths(ctx, nil, "diff", "--cached", "--no-renames", "--name-only", "-z")
			sort.Strings(result.StagedPaths)
			_, diffErr := target.Text(ctx, nil, "diff", "--cached", "--check")
			result.DiffCheckPassed = diffErr == nil
		}
		result.Environment = shellEnvironment(profile{SchemaVersion: 1, Lane: options.lane, Agent: options.agent, Session: options.session, Mode: store.Mode(options.mode), Worktree: result.Worktree})
		return result, nil
	}
	target := source
	if options.createWorktree != "" {
		var created bool
		target, created, err = ensureWorktree(ctx, source, options.createWorktree, options.baseRef)
		if err != nil {
			return result, err
		}
		result.CreatedWorktree = created
	}
	result.Worktree = target.Root
	laneStore, err := store.Open(target)
	if err != nil {
		return result, err
	}
	lane, err := ensureLane(ctx, laneStore, options)
	if err != nil {
		return result, err
	}
	result.Lane = &lane
	active, err := laneStore.ActivePaths(lane.ID)
	if err != nil {
		return result, err
	}
	var missing []string
	for _, wanted := range normalized {
		if !pathid.Covered(wanted, active) {
			missing = append(missing, wanted)
		}
	}
	if len(missing) > 0 {
		lease, claimErr := laneStore.Claim(lane.ID, lane.Agent, lane.Session, missing)
		if claimErr != nil {
			return result, claimErr
		}
		result.Lease = &lease
	}
	result.ProfilePath, err = writeProfile(laneStore, lane)
	if err != nil {
		return result, err
	}
	saved := profile{SchemaVersion: 1, Lane: lane.ID, Agent: lane.Agent, Session: lane.Session, Mode: lane.Mode, Worktree: lane.Worktree}
	result.Environment = shellEnvironment(saved)
	result.StagedPaths, _ = target.NULPaths(ctx, nil, "diff", "--cached", "--no-renames", "--name-only", "-z")
	sort.Strings(result.StagedPaths)
	_, diffErr := target.Text(ctx, nil, "diff", "--cached", "--check")
	result.DiffCheckPassed = diffErr == nil
	if options.install {
		result.InstallPath, result.Installed, result.InstallAlreadyValid, err = installSelf(options.installDir)
		if err != nil {
			return result, err
		}
		result.InstallDirOnPath = directoryOnPath(options.installDir)
	}
	return result, nil
}

func ensureLane(ctx context.Context, laneStore store.Store, options initOptions) (store.Lane, error) {
	wanted := store.CreateOptions{ID: options.lane, Agent: options.agent, Session: options.session, BaseRef: options.baseRef, Mode: store.Mode(options.mode), Worktree: laneStore.Repo.Root}
	existing, err := laneStore.Load(options.lane)
	if err != nil && fail.Code(err) == "LANE_NOT_FOUND" {
		return laneStore.Create(ctx, wanted)
	}
	if err != nil {
		return store.Lane{}, err
	}
	base, err := laneStore.Repo.Text(ctx, nil, "rev-parse", "--verify", options.baseRef+"^{commit}")
	if err != nil {
		return store.Lane{}, fail.Wrap("BASE_NOT_FOUND", err)
	}
	if existing.State != "active" || existing.Agent != options.agent || existing.Session != options.session || existing.Mode != store.Mode(options.mode) || existing.BaseSHA != base || existing.Worktree != laneStore.Repo.Root {
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
	executable, err := os.Executable()
	if err != nil {
		return "", false, false, fail.Wrap("INSTALL_FAILED", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", false, false, fail.Wrap("INSTALL_FAILED", err)
	}
	body, err := os.ReadFile(executable)
	if err != nil {
		return "", false, false, fail.Wrap("INSTALL_FAILED", err)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", false, false, fail.Wrap("INSTALL_FAILED", err)
	}
	path = filepath.Join(directory, binaryName())
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() {
			return path, false, false, fail.New("INSTALL_CONFLICT", "install target exists and is not a regular file")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			return path, false, false, fail.New("INSTALL_CONFLICT", "install target is not executable; refusing to change it")
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return path, false, false, fail.Wrap("INSTALL_FAILED", readErr)
		}
		left, right := sha256.Sum256(existing), sha256.Sum256(body)
		if left != right {
			return path, false, false, fail.New("INSTALL_CONFLICT", "install target exists with different content; refusing to overwrite")
		}
		return path, false, true, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return path, false, false, fail.Wrap("INSTALL_FAILED", statErr)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return path, false, false, fail.Wrap("INSTALL_FAILED", err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if written, err := file.Write(body); err != nil || written != len(body) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return path, false, false, fail.Wrap("INSTALL_FAILED", err)
	}
	if err := file.Sync(); err != nil {
		return path, false, false, fail.Wrap("INSTALL_FAILED", err)
	}
	if err := file.Close(); err != nil {
		return path, false, false, fail.Wrap("INSTALL_FAILED", err)
	}
	remove = false
	return path, true, false, nil
}

func (question prompt) ask(label, fallback string) (string, error) {
	if fallback == "" {
		fmt.Fprintf(question.out, "%s: ", label)
	} else {
		fmt.Fprintf(question.out, "%s [%s]: ", label, fallback)
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
			fmt.Fprintln(question.out, "Enter yes or no.")
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
		return "Git setup validation passed. Installation was not attempted. No files, refs, worktrees, or binaries were changed.\n\n" + result.Environment
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
	if !result.DiffCheckPassed && len(result.StagedPaths) > 0 {
		lines = append(lines, "The staged diff has whitespace errors. Fix them before capture.")
	}
	lines = append(lines, "Use this environment in the agent shell:", result.Environment, "Then run: wip commit")
	return strings.Join(lines, "\n")
}
