package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/engine"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/pathid"
	"github.com/nstranquist/wip-commit/internal/store"
	"github.com/nstranquist/wip-commit/internal/strictjson"
)

const maxPlanBytes = 1 << 20

type commitOptions struct {
	identity                                identityFlags
	plan, message                           string
	paths                                   stringList
	single, allowWIP, dryRun, noInteractive bool
	hookTimeout, verifyTimeout, lockWait    time.Duration
}

func (application app) runCommit(ctx context.Context, laneStore store.Store, args []string) int {
	set := application.flagSet("commit")
	options := commitOptions{}
	options.identity.bind(set)
	set.StringVar(&options.plan, "plan", "", "strict JSON split plan file, or - for stdin")
	set.BoolVar(&options.single, "single", false, "explicitly capture one commit instead of the split default")
	set.StringVar(&options.message, "message", "", "single-commit Conventional Commit message")
	set.Var(&options.paths, "path", "staged path scope for --single (repeatable)")
	set.BoolVar(&options.allowWIP, "allow-wip", false, "explicitly authorize the wip: commit prefix")
	set.BoolVar(&options.dryRun, "dry-run", false, "run all gates without creating commits or moving the lane ref")
	set.BoolVar(&options.noInteractive, "non-interactive", false, "fail instead of prompting for a split plan")
	set.DurationVar(&options.hookTimeout, "hook-timeout", engine.DefaultHookTimeout, "pre/post-commit hook timeout")
	set.DurationVar(&options.verifyTimeout, "verify-timeout", engine.DefaultVerifyTimeout, "default verify command timeout")
	set.DurationVar(&options.lockWait, "lock-wait", 0, "time to wait for the lane lock")
	if err := set.Parse(args); err != nil {
		return application.failure("commit", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("commit", err, nil, 2)
	}
	if options.lockWait < 0 {
		return application.failure("commit", fail.New("INVALID_ARGS", "--lock-wait cannot be negative"), nil, 2)
	}
	status, err := resolveStatus(laneStore, options.identity)
	if err != nil {
		return application.failure("commit", err, nil, 1)
	}
	lock, err := laneStore.LaneLock(status.Lane.ID, options.lockWait)
	if err != nil {
		return application.failure("commit", err, nil, 1)
	}
	defer func() { _ = lock.Release() }()
	lane, err := laneStore.Load(status.Lane.ID)
	if err != nil {
		return application.failure("commit", err, nil, 1)
	}
	if lane.State != "active" || lane.Agent != status.Lane.Agent || lane.Session != status.Lane.Session || lane.Worktree != laneStore.Repo.Root {
		return application.failure("commit", fail.New("LANE_MOVED", "lane identity, state, or worktree changed before capture"), nil, 1)
	}
	allowed, err := laneStore.ActivePaths(lane.ID)
	if err != nil {
		return application.failure("commit", err, nil, 1)
	}
	if len(allowed) == 0 {
		return application.failure("commit", fail.New("LEASE_REQUIRED", "claim or renew paths before capture"), nil, 1)
	}
	groups, err := application.resolveGroups(ctx, laneStore, allowed, options)
	if err != nil {
		return application.failure("commit", err, nil, 2)
	}
	hookOutput := application.stdout
	if application.jsonMode {
		hookOutput = io.Discard
	}
	result, err := engine.Run(ctx, engine.Options{
		Repo:                 laneStore.Repo,
		TargetRef:            lane.Ref,
		ExpectedRef:          lane.CurrentSHA,
		ExpectedSourceHead:   lane.BaseSHA,
		AllowedPaths:         allowed,
		Groups:               groups,
		AllowWIP:             options.allowWIP,
		DryRun:               options.dryRun,
		HookTimeout:          options.hookTimeout,
		DefaultVerifyTimeout: options.verifyTimeout,
		BeforePublish: func() error {
			return laneStore.ValidateCapture(ctx, lane, allowed)
		},
		Output:      hookOutput,
		ErrorOutput: application.stderr,
	})
	if err != nil {
		return application.failure("commit", err, result, 1)
	}
	if !options.dryRun {
		if err := laneStore.RecordCommit(ctx, lane.ID, result.FinalCommit); err != nil {
			return application.failure("commit", err, result, 1)
		}
		intent, markErr := engine.MarkIntent(laneStore.Repo, result.PlanID, result.PlanDigest, "complete")
		if markErr != nil {
			return application.failure("commit", markErr, result, 1)
		}
		result.IntentState = intent.State
	}
	human := fmt.Sprintf("captured %d commit(s) on %s", len(result.Commits), result.TargetRef)
	if options.dryRun {
		human = fmt.Sprintf("all gates passed for a %d-commit split plan; no ref moved", len(result.Commits))
	} else {
		human += " -> " + result.FinalCommit
	}
	return application.success("commit", result, human)
}

func (application app) resolveGroups(ctx context.Context, laneStore store.Store, allowed []string, options commitOptions) ([]engine.Group, error) {
	if options.plan != "" {
		if options.single || options.message != "" || len(options.paths) > 0 {
			return nil, fail.New("INVALID_ARGS", "--plan cannot be combined with --single, --message, or --path")
		}
		var reader io.Reader
		if options.plan == "-" {
			reader = application.stdin
		} else {
			file, err := os.Open(options.plan)
			if err != nil {
				return nil, fail.Wrap("INVALID_PLAN", err)
			}
			defer func() { _ = file.Close() }()
			reader = file
		}
		return decodePlan(reader)
	}
	if options.single {
		if strings.TrimSpace(options.message) == "" {
			return nil, fail.New("INVALID_ARGS", "--single requires --message")
		}
		selected, err := selectedStaged(ctx, laneStore, allowed, options.paths)
		if err != nil {
			return nil, err
		}
		return []engine.Group{{Message: options.message, Files: selected}}, nil
	}
	if options.message != "" || len(options.paths) > 0 {
		return nil, fail.New("SPLIT_PLAN_REQUIRED", "use --single to opt out, or omit --message and --path for the interactive split planner")
	}
	if options.noInteractive {
		return nil, fail.New("SPLIT_PLAN_REQUIRED", "non-interactive capture requires --plan or an explicit --single")
	}
	selected, err := selectedStaged(ctx, laneStore, allowed, nil)
	if err != nil {
		return nil, err
	}
	promptOutput := application.stdout
	if application.jsonMode {
		promptOutput = application.stderr
	}
	return interactiveGroups(selected, options.allowWIP, prompt{reader: bufio.NewReader(application.stdin), out: promptOutput})
}

func decodePlan(reader io.Reader) ([]engine.Group, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxPlanBytes+1))
	if err != nil {
		return nil, fail.Wrap("INVALID_PLAN", err)
	}
	if len(body) > maxPlanBytes {
		return nil, fail.Errorf("INVALID_PLAN", "plan exceeds %d bytes", maxPlanBytes)
	}
	var groups []engine.Group
	if err := strictjson.Decode(body, &groups); err != nil {
		return nil, fail.Wrap("INVALID_PLAN", err)
	}
	if len(groups) == 0 {
		return nil, fail.New("INVALID_PLAN", "plan must contain at least one commit group")
	}
	return groups, nil
}

func selectedStaged(ctx context.Context, laneStore store.Store, allowed []string, requested []string) ([]string, error) {
	staged, err := laneStore.Repo.NULPaths(ctx, nil, "diff", "--cached", "--no-renames", "--name-only", "-z")
	if err != nil {
		return nil, fail.Wrap("GIT_FAILED", err)
	}
	scopes := allowed
	if len(requested) > 0 {
		scopes, err = laneStore.Repo.NormalizePaths(requested)
		if err != nil {
			return nil, err
		}
		for _, scope := range scopes {
			if !pathid.Covered(scope, allowed) {
				return nil, fail.Errorf("LEASE_REQUIRED", "requested scope %q is outside the active lease set", scope)
			}
		}
	}
	var selected []string
	for _, path := range staged {
		if pathid.Covered(path, scopes) && pathid.Covered(path, allowed) {
			selected = append(selected, path)
		}
	}
	sort.Strings(selected)
	if len(selected) == 0 {
		return nil, fail.New("EMPTY_SELECTION", "no staged paths are covered by the selected active leases")
	}
	if len(requested) > 0 {
		for _, scope := range scopes {
			matched := false
			for _, path := range selected {
				if pathid.Covered(path, []string{scope}) {
					matched = true
					break
				}
			}
			if !matched {
				return nil, fail.Errorf("EMPTY_SELECTION", "requested scope %q contains no staged path", scope)
			}
		}
	}
	return selected, nil
}

func interactiveGroups(paths []string, allowWIP bool, prompter prompt) ([]engine.Group, error) {
	grouped := map[string][]string{}
	for _, path := range paths {
		parts := strings.SplitN(path, "/", 2)
		key := parts[0]
		grouped[key] = append(grouped[key], path)
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	_, _ = fmt.Fprintln(prompter.out, "Proposed split groups (one ref update after every group passes):")
	for _, key := range keys {
		_, _ = fmt.Fprintf(prompter.out, "  %s:\n    %s\n", key, strings.Join(grouped[key], "\n    "))
	}
	accepted, err := prompter.confirm("Use these split groups", true)
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, fail.New("SPLIT_PLAN_REQUIRED", "write a JSON plan to define different groups")
	}
	seen := map[string]bool{}
	groups := make([]engine.Group, 0, len(keys))
	for _, key := range keys {
		for {
			message, err := prompter.ask("Conventional Commit message for "+key, "")
			if err != nil {
				return nil, err
			}
			if err := engine.ValidateMessage(message, allowWIP); err != nil {
				_, _ = fmt.Fprintf(prompter.out, "%s: %s\n", fail.Code(err), err)
				continue
			}
			normalized := strings.ToLower(strings.TrimSpace(message))
			if seen[normalized] {
				_, _ = fmt.Fprintln(prompter.out, "DUPLICATE_COMMIT_MESSAGE: use a distinct outcome for each group")
				continue
			}
			seen[normalized] = true
			groups = append(groups, engine.Group{Message: message, Files: grouped[key]})
			break
		}
	}
	return groups, nil
}

func (application app) runReconcile(ctx context.Context, laneStore store.Store, args []string) int {
	set := application.flagSet("reconcile")
	var identity identityFlags
	identity.bind(set)
	planID := set.String("plan-id", "", "immutable plan id from the failed commit result")
	planDigest := set.String("plan-digest", "", "immutable sha256 plan digest")
	lockWait := set.Duration("lock-wait", 0, "time to wait for the lane lock")
	if err := set.Parse(args); err != nil {
		return application.failure("reconcile", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("reconcile", err, nil, 2)
	}
	if *planID == "" || *planDigest == "" || *lockWait < 0 {
		return application.failure("reconcile", fail.New("INVALID_ARGS", "--plan-id and --plan-digest are required; --lock-wait cannot be negative"), nil, 2)
	}
	status, err := resolveStatus(laneStore, identity)
	if err != nil {
		return application.failure("reconcile", err, nil, 1)
	}
	lock, err := laneStore.LaneLock(status.Lane.ID, *lockWait)
	if err != nil {
		return application.failure("reconcile", err, nil, 1)
	}
	defer func() { _ = lock.Release() }()
	lane, err := laneStore.Load(status.Lane.ID)
	if err != nil {
		return application.failure("reconcile", err, nil, 1)
	}
	intent, alreadyClean, err := engine.ValidateApplied(ctx, laneStore.Repo, *planID, *planDigest, lane.Ref, lane.CurrentSHA)
	if err != nil {
		return application.failure("reconcile", err, nil, 1)
	}
	result := engine.ReconcileResult{PlanID: intent.PlanID, PlanDigest: intent.PlanDigest, Commit: intent.ExpectedNew, IntentState: intent.State, AlreadyClean: alreadyClean}
	if alreadyClean {
		return application.success("reconcile", result, "plan is already fully reconciled")
	}
	if lane.CurrentSHA != intent.ExpectedNew {
		if err := laneStore.RecordCommit(ctx, lane.ID, intent.ExpectedNew); err != nil {
			return application.failure("reconcile", err, result, 1)
		}
	}
	if intent.State == "prepared" {
		intent, err = engine.MarkIntent(laneStore.Repo, intent.PlanID, intent.PlanDigest, "applied")
		if err != nil {
			return application.failure("reconcile", err, result, 1)
		}
	}
	intent, err = engine.MarkIntent(laneStore.Repo, intent.PlanID, intent.PlanDigest, "complete")
	if err != nil {
		return application.failure("reconcile", err, result, 1)
	}
	result.IntentState = intent.State
	return application.success("reconcile", result, "reconciled lane "+lane.ID+" to "+intent.ExpectedNew)
}
