package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/pathid"
	"github.com/nstranquist/wip-commit/internal/process"
)

const (
	ResultSchemaVersion  = "1.0.0"
	DefaultVerifyTimeout = 2 * time.Minute
	MaxVerifyTimeout     = 24 * time.Hour
	maxVerifyOutput      = 64 * 1024
	maxCommitGroups      = 128
	maxPlannedPaths      = 10_000
	maxVerifyCommands    = 64
	maxVerifyArguments   = 256
)

type VerifyCommand struct {
	Argv      []string `json:"argv"`
	Directory string   `json:"directory,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`
}

type Group struct {
	Message string          `json:"message"`
	Files   []string        `json:"files"`
	Verify  []VerifyCommand `json:"verify,omitempty"`
}

type PlannedCommit struct {
	Index        int      `json:"index"`
	Message      string   `json:"message"`
	Commit       string   `json:"commit,omitempty"`
	Parent       string   `json:"parent"`
	Tree         string   `json:"tree"`
	ChangedPaths []string `json:"changed_paths"`
	VerifyCount  int      `json:"verify_count"`
	Repairs      int      `json:"repairs_applied"`
}

type Options struct {
	Repo                 gitx.Repo
	TargetRef            string
	ExpectedRef          string
	ExpectedSourceHead   string
	AllowedPaths         []string
	Groups               []Group
	AllowWIP             bool
	DryRun               bool
	HookTimeout          time.Duration
	DefaultVerifyTimeout time.Duration
	BeforePublish        func() error
	Output               io.Writer
	ErrorOutput          io.Writer
}

type Result struct {
	SchemaVersion     string          `json:"schema_version"`
	TargetRef         string          `json:"target_ref"`
	ExpectedRef       string          `json:"expected_ref"`
	FinalCommit       string          `json:"final_commit,omitempty"`
	FinalTree         string          `json:"final_tree"`
	SourceHead        string          `json:"source_head"`
	SourceWorktree    string          `json:"source_worktree"`
	SourceIndexDigest string          `json:"source_index_digest"`
	HookDigest        string          `json:"hook_digest"`
	RequestedPaths    []string        `json:"requested_paths"`
	Commits           []PlannedCommit `json:"commits"`
	GateOutcome       string          `json:"gate_outcome"`
	DryRun            bool            `json:"dry_run"`
	RefUpdated        bool            `json:"ref_updated"`
	PublicationScope  string          `json:"publication_scope,omitempty"`
	PlanID            string          `json:"plan_id,omitempty"`
	PlanDigest        string          `json:"plan_digest,omitempty"`
	IntentPath        string          `json:"intent_path,omitempty"`
	IntentState       string          `json:"intent_state,omitempty"`
}

func Run(ctx context.Context, options Options) (result Result, err error) {
	result.SchemaVersion, result.DryRun, result.GateOutcome = ResultSchemaVersion, options.DryRun, "failed"
	if len(options.Groups) == 0 {
		return result, fail.New("INVALID_PLAN", "commit plan must contain at least one group")
	}
	if len(options.Groups) > maxCommitGroups {
		return result, fail.Errorf("INVALID_PLAN", "commit plan exceeds %d groups", maxCommitGroups)
	}
	if len(options.AllowedPaths) == 0 {
		return result, fail.New("LEASE_REQUIRED", "capture requires at least one active path lease")
	}
	if options.Output == nil {
		options.Output = io.Discard
	}
	if options.ErrorOutput == nil {
		options.ErrorOutput = io.Discard
	}
	if options.HookTimeout < 0 || options.DefaultVerifyTimeout < 0 {
		return result, fail.New("INVALID_ARGS", "timeouts cannot be negative")
	}
	if options.HookTimeout == 0 {
		options.HookTimeout = DefaultHookTimeout
	}
	if options.DefaultVerifyTimeout == 0 {
		options.DefaultVerifyTimeout = DefaultVerifyTimeout
	}
	if options.HookTimeout > MaxHookTimeout || options.DefaultVerifyTimeout > MaxVerifyTimeout {
		return result, fail.New("INVALID_ARGS", "timeout exceeds the 24-hour safety maximum")
	}
	allowed, err := options.Repo.NormalizePaths(options.AllowedPaths)
	if err != nil {
		return result, err
	}
	options.AllowedPaths = allowed
	groups, allPaths, err := normalizeGroups(options.Repo, options.Groups, options.AllowWIP)
	if err != nil {
		return result, err
	}
	result.RequestedPaths = append([]string(nil), allPaths...)
	for _, path := range allPaths {
		if !pathid.Covered(path, options.AllowedPaths) {
			return result, fail.Errorf("LEASE_REQUIRED", "planned path %q is outside the active lease set", path)
		}
	}
	target := strings.TrimSpace(options.TargetRef)
	if !strings.HasPrefix(target, "refs/heads/wip/") {
		return result, fail.New("INVALID_TARGET_REF", "target must use refs/heads/wip/<agent>/<lane>")
	}
	if _, err := options.Repo.Text(ctx, nil, "check-ref-format", target); err != nil {
		return result, fail.Wrap("INVALID_TARGET_REF", err)
	}
	expected, err := options.Repo.Text(ctx, nil, "rev-parse", "--verify", target+"^{commit}")
	if err != nil {
		return result, fail.Wrap("REF_NOT_FOUND", err)
	}
	if options.ExpectedRef != "" && options.ExpectedRef != expected {
		return result, fail.Errorf("REF_MOVED", "target ref moved from %s to %s", options.ExpectedRef, expected)
	}
	result.TargetRef, result.ExpectedRef = target, expected
	head, err := options.Repo.Text(ctx, nil, "rev-parse", "HEAD")
	if err != nil {
		return result, fail.Wrap("GIT_FAILED", err)
	}
	if options.ExpectedSourceHead != "" && options.ExpectedSourceHead != head {
		return result, fail.Errorf("SOURCE_HEAD_MOVED", "source HEAD is %s, expected %s", head, options.ExpectedSourceHead)
	}
	result.SourceHead, result.SourceWorktree = head, options.Repo.Root

	indexRoot := filepath.Join(options.Repo.GitDir, "wip", "indexes")
	if err := os.MkdirAll(indexRoot, 0o700); err != nil {
		return result, fail.Wrap("TEMP_INDEX_FAILED", err)
	}
	if err := os.Chmod(indexRoot, 0o700); err != nil {
		return result, fail.Wrap("TEMP_INDEX_FAILED", err)
	}
	preHook, err := prepareHook(ctx, options.Repo, indexRoot, "pre-commit")
	if err != nil {
		return result, err
	}
	defer preHook.cleanup()
	postHook, err := prepareHook(ctx, options.Repo, indexRoot, "post-commit")
	if err != nil {
		return result, err
	}
	defer postHook.cleanup()
	result.HookDigest = hooksDigest(preHook, postHook)

	source, err := options.Repo.IndexEntries(ctx, nil)
	if err != nil {
		return result, fail.Wrap("GIT_FAILED", err)
	}
	result.SourceIndexDigest = selectedDigest(source, allPaths)
	index, environment, cleanup, err := privateIndex(ctx, options.Repo, indexRoot, expected)
	if err != nil {
		return result, err
	}
	defer cleanup()
	parentCommit := expected
	parentTree, err := options.Repo.Text(ctx, nil, "show", "-s", "--format=%T", expected)
	if err != nil {
		return result, fail.Wrap("GIT_FAILED", err)
	}
	for groupIndex, group := range groups {
		baseEntries, entriesErr := options.Repo.IndexEntries(ctx, environment)
		if entriesErr != nil {
			return result, fail.Wrap("PRIVATE_INDEX_FAILED", entriesErr)
		}
		if err := stageEntries(ctx, options.Repo, environment, group.Files, source, baseEntries); err != nil {
			return result, err
		}
		preHookTree, treeErr := options.Repo.Text(ctx, environment, "write-tree")
		if treeErr != nil {
			return result, fail.Wrap("PRIVATE_INDEX_FAILED", treeErr)
		}
		if preHookTree == parentTree {
			return result, fail.Errorf("EMPTY_COMMIT_GROUP", "group %d has no captured staged change", groupIndex+1)
		}
		if err := preHook.run(ctx, options.Repo, hookEnvironment(index, target, ""), options.HookTimeout, options.Output, options.ErrorOutput); err != nil {
			return result, err
		}
		tree, treeErr := options.Repo.Text(ctx, environment, "write-tree")
		if treeErr != nil {
			return result, fail.Wrap("PRIVATE_INDEX_FAILED", treeErr)
		}
		changed, changedErr := options.Repo.NULPaths(ctx, nil, "diff", "--no-renames", "--name-only", "-z", parentTree, tree)
		if changedErr != nil {
			return result, fail.Wrap("PRIVATE_INDEX_FAILED", changedErr)
		}
		if len(changed) == 0 {
			return result, fail.Errorf("EMPTY_COMMIT_GROUP", "group %d became empty after pre-commit", groupIndex+1)
		}
		for _, path := range changed {
			if !pathid.Covered(path, group.Files) {
				return result, fail.Errorf("PLAN_SCOPE_MISMATCH", "group %d changed unplanned path %q", groupIndex+1, path)
			}
			if !pathid.Covered(path, options.AllowedPaths) {
				return result, fail.Errorf("LEASE_REQUIRED", "group %d changed unleased path %q", groupIndex+1, path)
			}
		}
		if _, err := options.Repo.Text(ctx, nil, "diff", "--check", parentTree, tree); err != nil {
			return result, fail.Wrap("DIFF_CHECK_FAILED", err)
		}
		if err := verifyTree(ctx, options.Repo, environment, tree, group.Verify, options.DefaultVerifyTimeout); err != nil {
			return result, err
		}
		repairs, _ := options.Repo.NULPaths(ctx, nil, "diff", "--no-renames", "--name-only", "-z", preHookTree, tree)
		planned := PlannedCommit{Index: groupIndex + 1, Message: group.Message, Parent: parentCommit, Tree: tree, ChangedPaths: changed, VerifyCount: len(group.Verify), Repairs: len(repairs)}
		if !options.DryRun {
			commit, commitErr := options.Repo.Text(ctx, environment, "commit-tree", tree, "-p", parentCommit, "-m", group.Message)
			if commitErr != nil {
				return result, fail.Wrap("COMMIT_FAILED", commitErr)
			}
			planned.Commit, parentCommit = commit, commit
		}
		parentTree = tree
		result.Commits = append(result.Commits, planned)
	}
	result.FinalTree = parentTree
	if options.BeforePublish != nil {
		if err := options.BeforePublish(); err != nil {
			return result, err
		}
	}
	currentHead, err := options.Repo.Text(ctx, nil, "rev-parse", "HEAD")
	if err != nil || currentHead != head {
		return result, fail.New("SOURCE_HEAD_MOVED", "source HEAD changed while capture was prepared")
	}
	currentIndex, err := options.Repo.IndexEntries(ctx, nil)
	if err != nil {
		return result, fail.Wrap("GIT_FAILED", err)
	}
	if selectedDigest(currentIndex, allPaths) != result.SourceIndexDigest {
		return result, fail.New("SOURCE_INDEX_MOVED", "selected staged entries changed while capture was prepared")
	}
	if err := preHook.validate(); err != nil {
		return result, err
	}
	if err := postHook.validate(); err != nil {
		return result, err
	}
	if options.DryRun {
		result.GateOutcome = "passed"
		return result, nil
	}
	result.FinalCommit = parentCommit
	intent, err := newIntent(result, options.AllowedPaths)
	if err != nil {
		return result, err
	}
	intentPath, err := createIntent(options.Repo, intent)
	if err != nil {
		return result, err
	}
	result.PlanID, result.PlanDigest, result.IntentPath, result.IntentState = intent.PlanID, intent.PlanDigest, intentPath, intent.State
	if _, err := options.Repo.Text(ctx, nil, "update-ref", "-m", fmt.Sprintf("wip plan: %d commit(s)", len(result.Commits)), target, parentCommit, expected); err != nil {
		return result, fail.New("REF_MOVED", "lane ref changed while capture was prepared")
	}
	result.RefUpdated, result.PublicationScope = true, "local_git_ref"
	applied, err := MarkIntent(options.Repo, intent.PlanID, intent.PlanDigest, "applied")
	if err != nil {
		return result, err
	}
	result.IntentState, result.GateOutcome = applied.State, "passed"
	for _, commit := range result.Commits {
		if _, err := options.Repo.Text(ctx, environment, "read-tree", commit.Tree); err != nil {
			fmt.Fprintf(options.ErrorOutput, "wip: prepare post-commit index for %s: %v\n", commit.Commit, err)
			continue
		}
		if err := postHook.run(ctx, options.Repo, hookEnvironment(index, target, commit.Commit), options.HookTimeout, options.Output, options.ErrorOutput); err != nil {
			fmt.Fprintf(options.ErrorOutput, "wip: post-commit hook failed for %s: %v\n", commit.Commit, err)
		}
	}
	return result, nil
}

func normalizeGroups(repo gitx.Repo, input []Group, allowWIP bool) ([]Group, []string, error) {
	groups := make([]Group, 0, len(input))
	seenMessages := map[string]bool{}
	var all []string
	for index, group := range input {
		if len(group.Verify) > maxVerifyCommands {
			return nil, nil, fail.Errorf("INVALID_PLAN", "group %d exceeds %d verify commands", index+1, maxVerifyCommands)
		}
		if err := ValidateMessage(group.Message, allowWIP); err != nil {
			return nil, nil, fail.Wrap("INVALID_COMMIT_MESSAGE", fmt.Errorf("group %d: %w", index+1, err))
		}
		key := strings.ToLower(strings.TrimSpace(group.Message))
		if seenMessages[key] {
			return nil, nil, fail.Errorf("DUPLICATE_COMMIT_MESSAGE", "group %d repeats a prior message", index+1)
		}
		seenMessages[key] = true
		paths, err := repo.NormalizePaths(group.Files)
		if err != nil {
			return nil, nil, err
		}
		if len(paths) == 0 {
			return nil, nil, fail.Errorf("INVALID_PLAN", "group %d has no files", index+1)
		}
		for _, path := range paths {
			for _, prior := range all {
				if pathid.Overlap(path, prior) {
					return nil, nil, fail.Errorf("OVERLAPPING_GROUPS", "planned paths %q and %q overlap", prior, path)
				}
			}
		}
		group.Files = paths
		groups, all = append(groups, group), append(all, paths...)
		if len(all) > maxPlannedPaths {
			return nil, nil, fail.Errorf("INVALID_PLAN", "commit plan exceeds %d paths", maxPlannedPaths)
		}
	}
	sort.Strings(all)
	return groups, all, nil
}

func privateIndex(ctx context.Context, repo gitx.Repo, root, base string) (string, []string, func(), error) {
	file, err := os.CreateTemp(root, "*.index")
	if err != nil {
		return "", nil, func() {}, fail.Wrap("TEMP_INDEX_FAILED", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, func() {}, fail.Wrap("TEMP_INDEX_FAILED", err)
	}
	if err := os.Remove(path); err != nil {
		return "", nil, func() {}, fail.Wrap("TEMP_INDEX_FAILED", err)
	}
	environment := []string{"GIT_INDEX_FILE=" + path}
	if _, err := repo.Text(ctx, environment, "read-tree", base); err != nil {
		return "", nil, func() {}, fail.Wrap("PRIVATE_INDEX_FAILED", err)
	}
	return path, environment, func() { _ = os.Remove(path) }, nil
}

func stageEntries(ctx context.Context, repo gitx.Repo, environment []string, paths []string, staged, base map[string]string) error {
	candidates := map[string]bool{}
	for path := range staged {
		if pathid.Covered(path, paths) {
			candidates[path] = true
		}
	}
	for path := range base {
		if pathid.Covered(path, paths) {
			candidates[path] = true
		}
	}
	changed := make([]string, 0, len(candidates))
	for path := range candidates {
		if staged[path] != base[path] {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	for _, path := range changed {
		entry := staged[path]
		if entry == "" {
			if _, err := repo.Text(ctx, environment, "update-index", "--force-remove", "--", path); err != nil {
				return fail.Wrap("STAGED_INDEX_INVALID", err)
			}
			continue
		}
		fields := strings.Fields(entry)
		if len(fields) != 3 || fields[2] != "0" {
			return fail.Errorf("STAGED_INDEX_INVALID", "selected path %q has an unmerged or malformed index entry", path)
		}
		if _, err := repo.Text(ctx, environment, "update-index", "--add", "--cacheinfo", fields[0], fields[1], path); err != nil {
			return fail.Wrap("STAGED_INDEX_INVALID", err)
		}
	}
	return nil
}

func selectedDigest(entries map[string]string, paths []string) string {
	selected := make([]string, 0, len(entries))
	for path, entry := range entries {
		if pathid.Covered(path, paths) {
			selected = append(selected, path+"\x00"+entry)
		}
	}
	sort.Strings(selected)
	hash := sha256.New()
	for _, entry := range selected {
		_, _ = hash.Write([]byte(entry))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func verifyTree(ctx context.Context, repo gitx.Repo, environment []string, tree string, commands []VerifyCommand, defaultTimeout time.Duration) error {
	if len(commands) == 0 {
		return nil
	}
	scratch, err := os.MkdirTemp("", "wip-verify-*")
	if err != nil {
		return fail.Wrap("VERIFY_FAILED", err)
	}
	defer os.RemoveAll(scratch)
	if _, err := repo.Text(ctx, environment, "checkout-index", "--all", "--force", "--prefix="+scratch+string(filepath.Separator)); err != nil {
		return fail.Wrap("VERIFY_FAILED", err)
	}
	for index, command := range commands {
		if len(command.Argv) == 0 || strings.TrimSpace(command.Argv[0]) == "" {
			return fail.Errorf("INVALID_VERIFY_COMMAND", "verify command %d has no executable", index+1)
		}
		if len(command.Argv) > maxVerifyArguments {
			return fail.Errorf("INVALID_VERIFY_COMMAND", "verify command %d exceeds %d arguments", index+1, maxVerifyArguments)
		}
		for _, argument := range command.Argv {
			if strings.ContainsRune(argument, '\x00') {
				return fail.Errorf("INVALID_VERIFY_COMMAND", "verify command %d contains a NUL argument", index+1)
			}
		}
		if strings.ContainsRune(command.Directory, '\x00') {
			return fail.Errorf("INVALID_VERIFY_COMMAND", "verify command %d directory contains NUL", index+1)
		}
		directory := scratch
		if command.Directory != "" {
			candidate := filepath.Clean(command.Directory)
			if filepath.IsAbs(candidate) || candidate == ".." || strings.HasPrefix(candidate, ".."+string(filepath.Separator)) {
				return fail.Errorf("INVALID_VERIFY_COMMAND", "verify command %d directory escapes the candidate tree", index+1)
			}
			resolved, resolveErr := filepath.EvalSymlinks(filepath.Join(scratch, candidate))
			if resolveErr != nil {
				return fail.Errorf("INVALID_VERIFY_COMMAND", "verify command %d directory is unavailable", index+1)
			}
			relative, relativeErr := filepath.Rel(scratch, resolved)
			if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return fail.Errorf("INVALID_VERIFY_COMMAND", "verify command %d directory follows a symlink outside the candidate tree", index+1)
			}
			directory = resolved
		}
		if command.TimeoutMS < 0 || command.TimeoutMS > MaxVerifyTimeout.Milliseconds() {
			return fail.Errorf("INVALID_VERIFY_COMMAND", "verify command %d timeout is outside the safety range", index+1)
		}
		timeout := defaultTimeout
		if command.TimeoutMS > 0 {
			timeout = time.Duration(command.TimeoutMS) * time.Millisecond
		}
		commandCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := process.CommandContext(commandCtx, command.Argv[0], command.Argv[1:]...)
		cmd.Dir = directory
		cmd.Env = append(os.Environ(), "WIP_CANDIDATE_TREE="+tree)
		output := &boundedBuffer{limit: maxVerifyOutput}
		cmd.Stdout, cmd.Stderr = output, output
		runErr := cmd.Run()
		contextErr := commandCtx.Err()
		cancel()
		if errors.Is(contextErr, context.DeadlineExceeded) {
			return fail.Errorf("VERIFY_TIMEOUT", "verify command %d exceeded %s", index+1, timeout)
		}
		if runErr != nil {
			detail := strings.TrimSpace(output.String())
			if detail == "" {
				detail = runErr.Error()
			}
			return fail.Errorf("VERIFY_FAILED", "verify command %d failed: %s", index+1, detail)
		}
	}
	return nil
}

type boundedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	limit  int
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	original := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = buffer.buffer.Write(value)
	}
	return original, nil
}

func (buffer *boundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}
