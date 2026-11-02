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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/pathid"
	"github.com/nstranquist/wip-commit/internal/policy"
	"github.com/nstranquist/wip-commit/internal/process"
)

const (
	ResultSchemaVersion  = "3.0.0"
	DefaultVerifyTimeout = 2 * time.Minute
	MaxVerifyTimeout     = 24 * time.Hour
	maxVerifyOutput      = 64 * 1024
	maxCommitGroups      = 128
	maxPlannedPaths      = 10_000
	maxVerifyCommands    = 64
	maxVerifyArguments   = 256
	maxVerifyScopes      = 256
	maxVerifyTreeEntries = 16_384
	maxVerifyTreeBytes   = 512 << 20
	maxVerifyListing     = 64 << 20
)

type VerifyCommand struct {
	Argv        []string `json:"argv"`
	Directory   string   `json:"directory,omitempty"`
	Materialize []string `json:"materialize,omitempty"`
	TimeoutMS   int64    `json:"timeout_ms,omitempty"`
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

type ExecutionCount struct {
	Planned  int `json:"planned"`
	Executed int `json:"executed"`
	Skipped  int `json:"skipped"`
}

type ExecutionReport struct {
	Policy          string         `json:"policy"`
	PreCommitHooks  ExecutionCount `json:"pre_commit_hooks"`
	PostCommitHooks ExecutionCount `json:"post_commit_hooks"`
	VerifyCommands  ExecutionCount `json:"verify_commands"`
}

type Options struct {
	Repo                 gitx.Repo
	TargetRef            string
	ExpectedRef          string
	ExpectedSourceHead   string
	ExpectedSourceDigest string
	SourceMode           SourceMode
	UntrackedPolicy      UntrackedPolicy
	ReviewPlanDigest     string
	MessagePolicy        policy.Receipt
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
	SchemaVersion    string           `json:"schema_version"`
	TargetRef        string           `json:"target_ref"`
	ExpectedRef      string           `json:"expected_ref"`
	FinalCommit      string           `json:"final_commit,omitempty"`
	FinalTree        string           `json:"final_tree"`
	SourceHead       string           `json:"source_head"`
	SourceWorktree   string           `json:"source_worktree"`
	SourceMode       SourceMode       `json:"source_mode"`
	UntrackedPolicy  UntrackedPolicy  `json:"untracked_policy"`
	SourceDigest     string           `json:"source_digest"`
	SourcePaths      []string         `json:"source_paths"`
	HookDigest       string           `json:"hook_digest"`
	RequestedPaths   []string         `json:"requested_paths"`
	Commits          []PlannedCommit  `json:"commits"`
	GateOutcome      string           `json:"gate_outcome"`
	Execution        *ExecutionReport `json:"execution,omitempty"`
	DryRun           bool             `json:"dry_run"`
	RefUpdated       bool             `json:"ref_updated"`
	PublicationScope string           `json:"publication_scope,omitempty"`
	PlanID           string           `json:"plan_id,omitempty"`
	PlanDigest       string           `json:"plan_digest,omitempty"`
	ReviewPlanDigest string           `json:"review_plan_digest,omitempty"`
	MessagePolicy    *policy.Receipt  `json:"message_policy,omitempty"`
	IntentPath       string           `json:"intent_path,omitempty"`
	IntentState      string           `json:"intent_state,omitempty"`
}

func Run(ctx context.Context, options Options) (result Result, err error) {
	result.SchemaVersion, result.DryRun, result.GateOutcome = ResultSchemaVersion, options.DryRun, "failed"
	result.ReviewPlanDigest = options.ReviewPlanDigest
	options.SourceMode, options.UntrackedPolicy, err = NormalizeSource(options.SourceMode, options.UntrackedPolicy)
	if err != nil {
		return result, err
	}
	result.SourceMode, result.UntrackedPolicy = options.SourceMode, options.UntrackedPolicy
	boundPlan := strings.TrimSpace(options.ReviewPlanDigest) != "" || options.MessagePolicy != (policy.Receipt{}) || strings.TrimSpace(options.ExpectedSourceDigest) != ""
	if boundPlan && (strings.TrimSpace(options.ReviewPlanDigest) == "" || options.MessagePolicy == (policy.Receipt{}) || strings.TrimSpace(options.ExpectedSourceDigest) == "") {
		return result, fail.New("INVALID_PLAN_BINDING", "reviewed plan digest, message policy, and expected source digest must be supplied together")
	}
	if boundPlan {
		receipt := options.MessagePolicy
		result.MessagePolicy = &receipt
		if !intentDigestPattern.MatchString(options.ReviewPlanDigest) {
			return result, fail.New("INVALID_PLAN_BINDING", "reviewed plan digest must be a sha256 receipt")
		}
		if !intentDigestPattern.MatchString(options.ExpectedSourceDigest) {
			return result, fail.New("INVALID_PLAN_BINDING", "expected source digest must be a sha256 receipt")
		}
		if err := policy.ValidateReceipt(options.MessagePolicy); err != nil {
			return result, err
		}
	}
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

	indexRoot := ""
	var objectEnvironment []string
	if options.DryRun {
		indexRoot, err = os.MkdirTemp("", "wip-dry-run-*")
		if err != nil {
			return result, fail.Wrap("TEMP_INDEX_FAILED", err)
		}
		defer func() { _ = os.RemoveAll(indexRoot) }()
		objectEnvironment, err = isolatedObjectEnvironment(options.Repo, indexRoot)
		if err != nil {
			return result, err
		}
	} else {
		indexRoot, err = preparePrivateIndexRoot(options.Repo.GitDir)
		if err != nil {
			return result, err
		}
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
	if options.DryRun {
		preCommitHooks, postCommitHooks, verifyCommands := 0, 0, 0
		if preHook.Private != "" {
			preCommitHooks = len(groups)
		}
		if postHook.Private != "" {
			postCommitHooks = len(groups)
		}
		for _, group := range groups {
			verifyCommands += len(group.Verify)
		}
		result.Execution = &ExecutionReport{
			Policy:          "no-exec",
			PreCommitHooks:  skippedExecutionCount(preCommitHooks),
			PostCommitHooks: skippedExecutionCount(postCommitHooks),
			VerifyCommands:  skippedExecutionCount(verifyCommands),
		}
	}

	sourceSnapshot, cleanupSource, err := captureSource(ctx, options.Repo, expected, allPaths, options.SourceMode, options.UntrackedPolicy, indexRoot, options.DryRun)
	if err != nil {
		return result, err
	}
	defer cleanupSource()
	source := sourceSnapshot.Entries
	result.SourcePaths = append([]string(nil), sourceSnapshot.Paths...)
	result.SourceDigest = sourceSnapshot.Digest
	if boundPlan && result.SourceDigest != options.ExpectedSourceDigest {
		return result, fail.New("SOURCE_MOVED", "the selected source no longer matches the reviewed plan")
	}
	_, environment, cleanup, err := privateIndex(ctx, options.Repo, indexRoot, expected, objectEnvironment)
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
		tree := preHookTree
		if !options.DryRun {
			if err := preHook.run(ctx, options.Repo, hookEnvironment(environment, hookPhasePreCommit, target, ""), options.HookTimeout, options.Output, options.ErrorOutput); err != nil {
				return result, err
			}
			tree, treeErr = options.Repo.Text(ctx, environment, "write-tree")
			if treeErr != nil {
				return result, fail.Wrap("PRIVATE_INDEX_FAILED", treeErr)
			}
		}
		changed, changedErr := options.Repo.NULPaths(ctx, environment, "diff", "--no-renames", "--name-only", "-z", parentTree, tree)
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
		if _, err := options.Repo.Text(ctx, environment, "diff", "--check", parentTree, tree); err != nil {
			return result, fail.Wrap("DIFF_CHECK_FAILED", err)
		}
		if err := verifyTree(ctx, options.Repo, environment, tree, group.Verify, options.DefaultVerifyTimeout, !options.DryRun); err != nil {
			return result, err
		}
		repairs, repairsErr := options.Repo.NULPaths(ctx, environment, "diff", "--no-renames", "--name-only", "-z", preHookTree, tree)
		if repairsErr != nil {
			return result, fail.Wrap("PRIVATE_INDEX_FAILED", repairsErr)
		}
		plannedParent := parentCommit
		if options.DryRun && groupIndex > 0 {
			plannedParent = ""
		}
		planned := PlannedCommit{Index: groupIndex + 1, Message: group.Message, Parent: plannedParent, Tree: tree, ChangedPaths: changed, VerifyCount: len(group.Verify), Repairs: len(repairs)}
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
	if !options.DryRun && options.BeforePublish != nil {
		if err := options.BeforePublish(); err != nil {
			return result, err
		}
	}
	currentHead, err := options.Repo.Text(ctx, nil, "rev-parse", "HEAD")
	if err != nil || currentHead != head {
		return result, fail.New("SOURCE_HEAD_MOVED", "source HEAD changed while capture was prepared")
	}
	currentSource, cleanupCurrentSource, err := captureSource(ctx, options.Repo, expected, allPaths, options.SourceMode, options.UntrackedPolicy, indexRoot, options.DryRun)
	if err != nil {
		return result, err
	}
	defer cleanupCurrentSource()
	if currentSource.Digest != result.SourceDigest || !sameSourcePaths(currentSource.Paths, result.SourcePaths) {
		return result, fail.New("SOURCE_MOVED", "the selected source changed while capture was prepared")
	}
	if err := preHook.validate(); err != nil {
		return result, err
	}
	if err := postHook.validate(); err != nil {
		return result, err
	}
	if options.DryRun {
		result.GateOutcome = "skipped"
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
			_, _ = fmt.Fprintf(options.ErrorOutput, "wip: prepare post-commit index for %s: %v\n", commit.Commit, err)
			continue
		}
		if err := postHook.run(ctx, options.Repo, hookEnvironment(environment, hookPhasePostCommit, target, commit.Commit), options.HookTimeout, options.Output, options.ErrorOutput); err != nil {
			_, _ = fmt.Fprintf(options.ErrorOutput, "wip: post-commit hook failed for %s: %v\n", commit.Commit, err)
		}
	}
	return result, nil
}

func preparePrivateIndexRoot(gitDir string) (string, error) {
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		return "", fail.Wrap("TEMP_INDEX_FAILED", err)
	}
	defer func() { _ = root.Close() }()
	for _, directory := range []string{"wip", "wip/indexes"} {
		info, statErr := root.Lstat(directory)
		if errors.Is(statErr, os.ErrNotExist) {
			if mkdirErr := root.Mkdir(directory, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return "", fail.Wrap("TEMP_INDEX_FAILED", mkdirErr)
			}
			info, statErr = root.Lstat(directory)
		}
		if statErr != nil {
			return "", fail.Wrap("TEMP_INDEX_FAILED", statErr)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fail.Errorf("TEMP_INDEX_FAILED", "%s is not a regular private-index directory", directory)
		}
		if chmodErr := root.Chmod(directory, 0o700); chmodErr != nil {
			return "", fail.Wrap("TEMP_INDEX_FAILED", chmodErr)
		}
	}
	return filepath.Join(gitDir, "wip", "indexes"), nil
}

func normalizeGroups(repo gitx.Repo, input []Group, _ bool) ([]Group, []string, error) {
	groups := make([]Group, 0, len(input))
	var all []string
	for index, group := range input {
		if len(group.Verify) > maxVerifyCommands {
			return nil, nil, fail.Errorf("INVALID_PLAN", "group %d exceeds %d verify commands", index+1, maxVerifyCommands)
		}
		if err := ValidateMessageSafety(group.Message); err != nil {
			return nil, nil, fail.Wrap("INVALID_COMMIT_MESSAGE", fmt.Errorf("group %d: %w", index+1, err))
		}
		for commandIndex := range group.Verify {
			normalized, verifyErr := normalizeVerifyCommand(repo, group.Verify[commandIndex], commandIndex)
			if verifyErr != nil {
				return nil, nil, fail.Wrap("INVALID_VERIFY_COMMAND", fmt.Errorf("group %d: %w", index+1, verifyErr))
			}
			group.Verify[commandIndex] = normalized
		}
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

func normalizeVerifyCommand(repo gitx.Repo, command VerifyCommand, index int) (VerifyCommand, error) {
	if len(command.Argv) == 0 || strings.TrimSpace(command.Argv[0]) == "" {
		return VerifyCommand{}, fmt.Errorf("verify command %d has no executable", index+1)
	}
	if len(command.Argv) > maxVerifyArguments {
		return VerifyCommand{}, fmt.Errorf("verify command %d exceeds %d arguments", index+1, maxVerifyArguments)
	}
	for _, argument := range command.Argv {
		if strings.ContainsRune(argument, '\x00') {
			return VerifyCommand{}, fmt.Errorf("verify command %d contains a NUL argument", index+1)
		}
	}
	if command.TimeoutMS < 0 || command.TimeoutMS > MaxVerifyTimeout.Milliseconds() {
		return VerifyCommand{}, fmt.Errorf("verify command %d timeout is outside the safety range", index+1)
	}
	if command.Directory != "" && command.Directory != "." {
		directory, err := normalizeVerifyScope(repo, command.Directory)
		if err != nil {
			return VerifyCommand{}, fmt.Errorf("verify command %d directory: %w", index+1, err)
		}
		command.Directory = directory
	}
	if len(command.Materialize) > maxVerifyScopes {
		return VerifyCommand{}, fmt.Errorf("verify command %d exceeds %d materialization scopes", index+1, maxVerifyScopes)
	}
	materialize := make([]string, 0, len(command.Materialize))
	seen := map[string]bool{}
	for _, raw := range command.Materialize {
		scope, err := normalizeVerifyScope(repo, raw)
		if err != nil {
			return VerifyCommand{}, fmt.Errorf("verify command %d materialization scope: %w", index+1, err)
		}
		if seen[scope] {
			return VerifyCommand{}, fmt.Errorf("verify command %d has duplicate materialization scope %q", index+1, scope)
		}
		seen[scope] = true
		materialize = append(materialize, scope)
	}
	sort.Strings(materialize)
	command.Materialize = materialize
	return command, nil
}

func normalizeVerifyScope(repo gitx.Repo, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" || strings.ContainsRune(raw, '\x00') || strings.ContainsRune(raw, '\\') || filepath.IsAbs(raw) {
		return "", fmt.Errorf("scope must be a non-empty portable repository-relative path")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(raw)))
	if clean != raw || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("scope %q is not canonical", raw)
	}
	normalized, err := repo.NormalizePaths([]string{raw})
	if err != nil || len(normalized) != 1 || normalized[0] != raw {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("scope %q is not canonical", raw)
	}
	return raw, nil
}

func privateIndex(ctx context.Context, repo gitx.Repo, root, base string, baseEnvironment []string) (string, []string, func(), error) {
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
	environment := append([]string(nil), baseEnvironment...)
	environment = append(environment, "GIT_INDEX_FILE="+path)
	if _, err := repo.Text(ctx, environment, "read-tree", base); err != nil {
		return "", nil, func() {}, fail.Wrap("PRIVATE_INDEX_FAILED", err)
	}
	return path, environment, func() { _ = os.Remove(path) }, nil
}

func sameSourcePaths(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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

// SelectedIndexDigest returns a stable digest for entries covered by paths.
// Callers use it to bind a reviewed plan to the exact staged index content.
func SelectedIndexDigest(entries map[string]string, paths []string) string {
	return selectedDigest(entries, paths)
}

func skippedExecutionCount(planned int) ExecutionCount {
	return ExecutionCount{Planned: planned, Executed: 0, Skipped: planned}
}

func verifyTree(ctx context.Context, repo gitx.Repo, environment []string, tree string, commands []VerifyCommand, defaultTimeout time.Duration, execute bool) error {
	if len(commands) == 0 {
		return nil
	}
	paths, err := verifyMaterializationPaths(ctx, repo, environment, tree, commands, maxVerifyTreeEntries, maxVerifyTreeBytes)
	if err != nil {
		return err
	}
	scratch := ""
	if execute {
		createdScratch, err := os.MkdirTemp("", "wip-verify-*")
		if err != nil {
			return fail.Wrap("VERIFY_FAILED", err)
		}
		defer func() { _ = os.RemoveAll(createdScratch) }()
		// macOS exposes /var as a symlink to /private/var. Resolve the scratch
		// root once before comparing a resolved verification subdirectory to it.
		// Otherwise an in-tree directory appears to escape whenever TMPDIR uses
		// the /var spelling. The candidate directory is still resolved for every
		// command, so a symlink inside the candidate cannot cross this root.
		scratch, err = filepath.EvalSymlinks(createdScratch)
		if err != nil {
			return fail.Wrap("VERIFY_FAILED", err)
		}
		if err := checkoutVerifyPaths(ctx, repo, environment, scratch+string(filepath.Separator), paths); err != nil {
			return err
		}
	}
	for index, command := range commands {
		directory := scratch
		if command.Directory != "" {
			candidate := filepath.FromSlash(command.Directory)
			if execute {
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
		}
		if !execute {
			continue
		}
		timeout := defaultTimeout
		if command.TimeoutMS > 0 {
			timeout = time.Duration(command.TimeoutMS) * time.Millisecond
		}
		commandCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := process.CommandContext(commandCtx, command.Argv[0], command.Argv[1:]...)
		cmd.Dir = directory
		cmd.Env = verificationEnvironment(tree)
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

func verifyMaterializationPaths(ctx context.Context, repo gitx.Repo, environment []string, tree string, commands []VerifyCommand, maxEntries int, maxBytes int64) ([]string, error) {
	fullTree := false
	scopeSet := map[string]bool{}
	for index, command := range commands {
		if command.Directory == "." {
			fullTree = true
		} else if command.Directory != "" {
			kind, kindErr := repo.Text(ctx, environment, "cat-file", "-t", tree+":"+command.Directory)
			if kindErr != nil || kind != "tree" {
				return nil, fail.Errorf("INVALID_VERIFY_COMMAND", "verify command %d directory is unavailable in the candidate tree", index+1)
			}
			scopeSet[command.Directory] = true
		} else if len(command.Materialize) == 0 {
			fullTree = true
		}
		for _, scope := range command.Materialize {
			scopeSet[scope] = true
		}
	}
	scopes := make([]string, 0, len(scopeSet))
	for scope := range scopeSet {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)

	arguments := []string{"ls-tree", "-r", "-l", "-z", "--full-tree", tree}
	if !fullTree {
		arguments = append(arguments, "--")
		for _, scope := range scopes {
			arguments = append(arguments, ":(top,literal)"+scope)
		}
	}
	listing := &boundedBuffer{limit: maxVerifyListing}
	errorsOutput := &boundedBuffer{limit: maxVerifyOutput}
	command := process.CommandContext(ctx, "git", append([]string{"-C", repo.Root}, arguments...)...)
	command.Env = gitx.Environment(environment)
	command.Stdout, command.Stderr = listing, errorsOutput
	if runErr := command.Run(); runErr != nil {
		return nil, fail.Errorf("VERIFY_FAILED", "list candidate tree: %s", strings.TrimSpace(errorsOutput.String()))
	}
	if listing.Truncated() {
		return nil, fail.Errorf("VERIFY_MATERIALIZATION_LIMIT", "candidate tree listing exceeds %d bytes", maxVerifyListing)
	}
	paths, _, parseErr := parseVerifyTreeListing(listing.String(), maxEntries, maxBytes)
	if parseErr != nil {
		return nil, parseErr
	}
	if !fullTree {
		for _, scope := range scopes {
			matched := false
			for _, path := range paths {
				if path == scope || strings.HasPrefix(path, scope+"/") {
					matched = true
					break
				}
			}
			if !matched {
				return nil, fail.Errorf("INVALID_VERIFY_COMMAND", "materialization scope %q is unavailable in the candidate tree", scope)
			}
		}
	}
	return paths, nil
}

func parseVerifyTreeListing(listing string, maxEntries int, maxBytes int64) ([]string, int64, error) {
	paths := make([]string, 0)
	seen := map[string]bool{}
	var totalBytes int64
	for _, row := range strings.Split(listing, "\x00") {
		if row == "" {
			continue
		}
		metadata, path, found := strings.Cut(row, "\t")
		fields := strings.Fields(metadata)
		if !found || len(fields) != 4 || path == "" {
			return nil, 0, fail.New("VERIFY_FAILED", "candidate tree listing is malformed")
		}
		if seen[path] {
			continue
		}
		size := int64(0)
		if fields[3] != "-" {
			parsed, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil || parsed < 0 {
				return nil, 0, fail.New("VERIFY_FAILED", "candidate tree listing has an invalid object size")
			}
			size = parsed
		}
		if len(paths) >= maxEntries {
			return nil, 0, fail.Errorf("VERIFY_MATERIALIZATION_LIMIT", "candidate tree exceeds %d materialized entries", maxEntries)
		}
		if size > maxBytes-totalBytes {
			return nil, 0, fail.Errorf("VERIFY_MATERIALIZATION_LIMIT", "candidate tree exceeds %d materialized bytes", maxBytes)
		}
		seen[path] = true
		paths = append(paths, path)
		totalBytes += size
	}
	sort.Strings(paths)
	return paths, totalBytes, nil
}

func checkoutVerifyPaths(ctx context.Context, repo gitx.Repo, environment []string, prefix string, paths []string) error {
	var input bytes.Buffer
	for _, path := range paths {
		_, _ = input.WriteString(path)
		_ = input.WriteByte(0)
	}
	errorsOutput := &boundedBuffer{limit: maxVerifyOutput}
	// Verification must see the candidate's committed bytes. A host-level
	// core.autocrlf (the Windows default) would otherwise rewrite line endings.
	command := process.CommandContext(ctx, "git", "-C", repo.Root, "-c", "core.autocrlf=false", "-c", "core.eol=lf", "checkout-index", "--force", "--stdin", "-z", "--prefix="+prefix)
	command.Env = gitx.Environment(environment)
	command.Stdin = &input
	command.Stderr = errorsOutput
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(errorsOutput.String())
		if detail == "" {
			detail = err.Error()
		}
		return fail.Errorf("VERIFY_FAILED", "materialize candidate tree: %s", detail)
	}
	return nil
}

func verificationEnvironment(tree string) []string {
	blocked := map[string]bool{
		"GIT_INDEX_FILE":       true,
		"WIP_AGENT":            true,
		"WIP_CAPTURE":          true,
		"WIP_CANDIDATE_TREE":   true,
		"WIP_COMMIT_OBJECT":    true,
		"WIP_HOOK_PHASE":       true,
		"WIP_LANE":             true,
		"WIP_SESSION":          true,
		"WIP_SOURCE_READ_ONLY": true,
		"WIP_TARGET_REF":       true,
	}
	inherited := gitx.Environment(nil)
	environment := make([]string, 0, len(inherited)+1)
	for _, entry := range inherited {
		name, _, _ := strings.Cut(entry, "=")
		if !blocked[strings.ToUpper(name)] {
			environment = append(environment, entry)
		}
	}
	return append(environment, "WIP_CANDIDATE_TREE="+tree)
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	original := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			buffer.truncated = true
			value = value[:remaining]
		}
		_, _ = buffer.buffer.Write(value)
	} else if len(value) > 0 {
		buffer.truncated = true
	}
	return original, nil
}

func (buffer *boundedBuffer) Truncated() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.truncated
}

func (buffer *boundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}
