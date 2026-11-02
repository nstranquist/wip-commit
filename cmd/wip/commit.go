package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/engine"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/filelock"
	"github.com/nstranquist/wip-commit/internal/pathid"
	"github.com/nstranquist/wip-commit/internal/policy"
	"github.com/nstranquist/wip-commit/internal/store"
)

const maxPlanBytes = 1 << 20

type commitOptions struct {
	identity                                identityFlags
	plan, reviewPlanDigest, message         string
	paths                                   stringList
	single, allowWIP, dryRun, noInteractive bool
	final                                   bool
	hookTimeout, verifyTimeout, lockWait    time.Duration
}

func (application app) runCommit(ctx context.Context, laneStore store.Store, args []string) int {
	return application.runCaptureTransaction(ctx, laneStore, args, false)
}

func (application app) runCapture(ctx context.Context, laneStore store.Store, args []string) int {
	return application.runCaptureTransaction(ctx, laneStore, args, true)
}

// preflightCapturePlan verifies NDev's exact canonical reviewed-plan digest
// before command dispatch acquires any repository writer lock. The locked
// transaction reopens and rechecks file-backed plans. Stdin is bounded once
// and replayed so the same immutable input is checked again.
func (application *app) preflightCapturePlan(args []string) error {
	options, err := application.parseCommitOptions("capture", args, true)
	if err != nil {
		return err
	}
	if options.reviewPlanDigest == "" {
		return nil
	}
	var body []byte
	if options.plan == "-" {
		body, err = readCommitPlanBody(application.stdin)
		if err == nil {
			application.stdin = bytes.NewReader(body)
		}
	} else {
		file, openErr := os.Open(options.plan)
		if openErr != nil {
			return fail.Wrap("INVALID_PLAN", openErr)
		}
		body, err = readCommitPlanBody(file)
		closeErr := file.Close()
		if err == nil && closeErr != nil {
			err = fail.Wrap("INVALID_PLAN", closeErr)
		}
	}
	if err != nil {
		return err
	}
	resolved, err := decodeCommitPlanBody(body)
	if err != nil {
		return err
	}
	if !resolved.Reviewed || resolved.Plan == nil || resolved.Plan.SchemaVersion != reviewPlanSchemaVersion || resolved.Plan.State != "reviewed" {
		return fail.New("REVIEWED_PLAN_REQUIRED", "capture requires the reviewed v3 plan schema; legacy arrays remain commit-only")
	}
	digest, err := reviewPlanDigest(*resolved.Plan)
	if err != nil {
		return err
	}
	if digest != options.reviewPlanDigest {
		return fail.New("REVIEW_PLAN_DIGEST_MISMATCH", "reviewed plan does not match --review-plan-digest")
	}
	return nil
}

func (application app) runCaptureTransaction(ctx context.Context, laneStore store.Store, args []string, guided bool) int {
	action := "commit"
	if guided {
		action = "capture"
	}
	options, err := application.parseCommitOptions(action, args, guided)
	if err != nil {
		return application.failure(action, err, nil, 2)
	}
	status, err := resolveStatus(laneStore, options.identity)
	if err != nil {
		return application.failure(action, err, nil, 1)
	}
	var lock *filelock.Lock
	if options.dryRun {
		lock, err = laneStore.ExistingLaneLock(status.Lane.ID, options.lockWait)
	} else {
		lock, err = laneStore.LaneLock(status.Lane.ID, options.lockWait)
	}
	if err != nil {
		return application.failure(action, err, nil, 1)
	}
	defer func() { _ = lock.Release() }()
	lane, err := laneStore.Load(status.Lane.ID)
	if err != nil {
		return application.failure(action, err, nil, 1)
	}
	if lane.State != "active" || lane.Agent != status.Lane.Agent || lane.Session != status.Lane.Session || lane.Worktree != laneStore.Repo.Root {
		return application.failure(action, fail.New("LANE_MOVED", "lane identity, state, or worktree changed before capture"), nil, 1)
	}
	saved, err := loadProfile(laneStore, lane.ID)
	if err != nil {
		return application.failure(action, err, nil, 1)
	}
	allowed, err := laneStore.ActivePaths(lane.ID)
	if err != nil {
		return application.failure(action, err, nil, 1)
	}
	if len(allowed) == 0 {
		return application.failure(action, fail.New("LEASE_REQUIRED", "claim or renew paths before capture"), nil, 1)
	}

	// A guided capture validates the exact reviewed plan before it renews a
	// lease. The outer command preflight performs the same digest check before
	// acquiring the repository writer fence; this locked check closes a plan
	// file replacement between preflight and execution.
	var resolved resolvedCommitPlan
	if guided {
		resolved, err = application.resolveGroups(ctx, laneStore, lane, allowed, saved.MessagePolicy, options)
		if err != nil {
			return application.failure(action, err, nil, 2)
		}
		if !resolved.Reviewed {
			return application.failure(action, fail.New("REVIEWED_PLAN_REQUIRED", "capture requires the reviewed v3 plan schema; legacy arrays remain commit-only"), nil, 2)
		}
	}

	captureCtx := ctx
	stopHeartbeat := func() error { return nil }
	if !options.dryRun {
		heartbeat := application.leaseHeartbeat
		if heartbeat == nil {
			heartbeat = startLeaseHeartbeat
		}
		captureCtx, stopHeartbeat, err = heartbeat(ctx, laneStore, lane, allowed)
		if err != nil {
			return application.failure(action, err, nil, 1)
		}
	}
	heartbeatStopped := false
	stop := func() error {
		if heartbeatStopped {
			return nil
		}
		heartbeatStopped = true
		return stopHeartbeat()
	}
	defer func() { _ = stop() }()
	if !guided {
		resolved, err = application.resolveGroups(captureCtx, laneStore, lane, allowed, saved.MessagePolicy, options)
		if err != nil {
			if heartbeatErr := stop(); heartbeatErr != nil {
				return application.failure(action, heartbeatErr, nil, 1)
			}
			return application.failure(action, err, nil, 2)
		}
	}

	hookOutput := application.stdout
	if application.jsonMode {
		hookOutput = io.Discard
	}
	var beforePublish func() error
	if !options.dryRun {
		beforePublish = func() error { return laneStore.RefreshCaptureLease(captureCtx, lane, allowed) }
	}
	result, err := engine.Run(captureCtx, engine.Options{
		Repo:                 laneStore.Repo,
		TargetRef:            lane.Ref,
		ExpectedRef:          lane.CurrentSHA,
		ExpectedSourceHead:   lane.BaseSHA,
		ExpectedSourceDigest: resolved.ExpectedSourceDigest,
		SourceMode:           resolved.SourceMode,
		UntrackedPolicy:      resolved.UntrackedPolicy,
		AllowedPaths:         allowed,
		Groups:               resolved.Groups,
		ReviewPlanDigest:     resolved.ReviewDigest,
		MessagePolicy:        resolved.Policy,
		AllowWIP:             options.allowWIP,
		DryRun:               options.dryRun,
		HookTimeout:          options.hookTimeout,
		DefaultVerifyTimeout: options.verifyTimeout,
		BeforePublish:        beforePublish,
		Output:               hookOutput,
		ErrorOutput:          application.stderr,
	})
	heartbeatErr := stop()
	if heartbeatErr != nil {
		return application.failure(action, heartbeatErr, result, 1)
	}
	if err != nil {
		return application.failure(action, err, result, 1)
	}
	if !options.dryRun {
		if err := laneStore.RecordCommit(ctx, lane.ID, result.FinalCommit); err != nil {
			return application.failure(action, err, result, 1)
		}
		intent, markErr := engine.MarkIntent(laneStore.Repo, result.PlanID, result.PlanDigest, "complete")
		if markErr != nil {
			return application.failure(action, markErr, result, 1)
		}
		result.IntentState = intent.State
	}
	human := fmt.Sprintf("captured %d commit(s) on %s", len(result.Commits), result.TargetRef)
	if options.dryRun {
		hooks, verify := 0, 0
		if result.Execution != nil {
			hooks = result.Execution.PreCommitHooks.Skipped + result.Execution.PostCommitHooks.Skipped
			verify = result.Execution.VerifyCommands.Skipped
		}
		human = fmt.Sprintf("validated a %d-commit candidate structure; skipped %d repository hook invocation(s) and %d verify command(s); no ref moved", len(result.Commits), hooks, verify)
	} else {
		human += " -> " + result.FinalCommit
	}
	if !guided {
		renewal := renewalStatus{Attempted: !options.dryRun, Succeeded: !options.dryRun}
		laneContext, contextErr := laneContextFor(ctx, laneStore, lane.ID, renewal)
		if contextErr != nil {
			return application.failure(action, contextErr, result, 1)
		}
		return application.successWithContext(action, result, human, laneContext)
	}
	current, err := laneStore.Load(lane.ID)
	if err != nil {
		return application.failure(action, err, result, 1)
	}
	handoff, err := buildCaptureHandoff(ctx, laneStore, current, result.PlanID, result.PlanDigest)
	if err != nil {
		return application.failure(action, err, result, 1)
	}
	guidedResult := guidedCaptureResult{Capture: result, Handoff: handoff}
	if options.final {
		if application.beforeFinalRelease != nil {
			application.beforeFinalRelease()
		}
		if err := laneStore.ReleaseWithLockHeld(current, false); err != nil {
			return application.failure(action, err, guidedResult, 1)
		}
		guidedResult.LaneReleased = true
		current, err = laneStore.Load(current.ID)
		if err != nil {
			return application.failure(action, err, guidedResult, 1)
		}
	}
	status, statusErr := laneStore.Status(current.ID)
	if statusErr != nil {
		return application.failure(action, statusErr, guidedResult, 1)
	}
	operational, inspectErr := buildOperationalStatus(ctx, laneStore, status, renewalStatus{Attempted: true, Succeeded: true})
	if inspectErr != nil {
		return application.failure(action, inspectErr, guidedResult, 1)
	}
	human = fmt.Sprintf("captured %d reviewed commit(s) on %s and emitted a capture-only handoff", len(result.Commits), result.TargetRef)
	if options.final {
		human += "; lane released"
	}
	return application.successWithContext(action, guidedResult, human, operational.context())
}

func (application app) parseCommitOptions(action string, args []string, guided bool) (commitOptions, error) {
	set := application.flagSet(action)
	options := commitOptions{}
	options.identity.bind(set)
	set.StringVar(&options.plan, "plan", "", "strict JSON split plan file, or - for stdin")
	set.StringVar(&options.reviewPlanDigest, "review-plan-digest", "", "expected canonical SHA-256 digest of the reviewed plan")
	set.BoolVar(&options.single, "single", false, "explicitly capture one commit instead of the split default")
	set.StringVar(&options.message, "message", "", "single-commit message under the bound lane policy")
	set.Var(&options.paths, "path", "staged path scope for --single (repeatable)")
	set.BoolVar(&options.allowWIP, "allow-wip", false, "explicitly authorize the wip: commit prefix")
	set.BoolVar(&options.dryRun, "dry-run", false, "validate candidate structure without executing hooks, verify commands, commits, or ref updates")
	set.BoolVar(&options.noInteractive, "non-interactive", false, "fail instead of prompting for a split plan")
	set.BoolVar(&options.final, "final", false, "release the lane after a validated capture handoff")
	set.DurationVar(&options.hookTimeout, "hook-timeout", engine.DefaultHookTimeout, "pre/post-commit hook timeout")
	set.DurationVar(&options.verifyTimeout, "verify-timeout", engine.DefaultVerifyTimeout, "default verify command timeout")
	set.DurationVar(&options.lockWait, "lock-wait", 0, "time to wait for the lane lock; zero fails fast")
	if err := set.Parse(args); err != nil {
		return commitOptions{}, fail.Wrap("INVALID_ARGS", err)
	}
	if err := noArgs(set); err != nil {
		return commitOptions{}, err
	}
	if guided && (options.plan == "" || options.single || options.message != "" || len(options.paths) > 0 || options.noInteractive || options.dryRun) {
		return commitOptions{}, fail.New("REVIEWED_PLAN_REQUIRED", "capture requires one reviewed v3 --plan and does not accept --single, --message, --path, --non-interactive, or --dry-run")
	}
	if !guided && options.final {
		return commitOptions{}, fail.New("INVALID_ARGS", "--final is available only with wip capture")
	}
	if !guided && options.reviewPlanDigest != "" {
		return commitOptions{}, fail.New("INVALID_ARGS", "--review-plan-digest is available only with wip capture")
	}
	if options.reviewPlanDigest != "" && !validSHA256(options.reviewPlanDigest) {
		return commitOptions{}, fail.New("INVALID_ARGS", "--review-plan-digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	if options.lockWait < 0 {
		return commitOptions{}, fail.New("INVALID_ARGS", "--lock-wait cannot be negative")
	}
	return options, nil
}

func startLeaseHeartbeat(parent context.Context, laneStore store.Store, lane store.Lane, allowed []string) (context.Context, func() error, error) {
	return startLeaseHeartbeatWithObserver(parent, laneStore, lane, allowed, nil)
}

func startLeaseHeartbeatWithObserver(parent context.Context, laneStore store.Store, lane store.Lane, allowed []string, afterBackgroundRefresh func()) (context.Context, func() error, error) {
	if err := laneStore.RefreshCaptureLease(parent, lane, allowed); err != nil {
		return nil, nil, fail.Wrap("LEASE_HEARTBEAT_FAILED", err)
	}
	captureCtx, cancelCapture := context.WithCancel(parent)
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ttl := laneStore.LeaseTTL
	if ttl <= 0 {
		ttl = store.DefaultLeaseTTL
	}
	interval := ttl / 3
	if interval <= 0 {
		interval = time.Nanosecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-parent.Done():
				done <- nil
				return
			case <-heartbeatCtx.Done():
				done <- nil
				return
			case <-ticker.C:
				if err := laneStore.RefreshCaptureLease(parent, lane, allowed); err != nil {
					cancelCapture()
					done <- fail.Wrap("LEASE_HEARTBEAT_FAILED", err)
					return
				}
				if afterBackgroundRefresh != nil {
					afterBackgroundRefresh()
				}
			}
		}
	}()
	stop := func() error {
		cancelHeartbeat()
		err := <-done
		cancelCapture()
		return err
	}
	return captureCtx, stop, nil
}

func (application app) resolveGroups(ctx context.Context, laneStore store.Store, lane store.Lane, allowed []string, messagePolicy policy.Receipt, options commitOptions) (resolvedCommitPlan, error) {
	if options.plan != "" {
		if options.single || options.message != "" || len(options.paths) > 0 {
			return resolvedCommitPlan{}, fail.New("INVALID_ARGS", "--plan cannot be combined with --single, --message, or --path")
		}
		var reader io.Reader
		if options.plan == "-" {
			reader = application.stdin
		} else {
			file, err := os.Open(options.plan)
			if err != nil {
				return resolvedCommitPlan{}, fail.Wrap("INVALID_PLAN", err)
			}
			defer func() { _ = file.Close() }()
			reader = file
		}
		resolved, err := decodeCommitPlan(reader)
		if err != nil {
			return resolvedCommitPlan{}, err
		}
		if resolved.Reviewed {
			digest, digestErr := reviewPlanDigest(*resolved.Plan)
			if digestErr != nil {
				return resolvedCommitPlan{}, digestErr
			}
			if options.reviewPlanDigest != "" && digest != options.reviewPlanDigest {
				return resolvedCommitPlan{}, fail.New("REVIEW_PLAN_DIGEST_MISMATCH", "reviewed plan does not match --review-plan-digest")
			}
			return validateReviewedPlan(ctx, laneStore, lane, allowed, resolved.Plan, options.allowWIP)
		}
		messages := make([]string, 0, len(resolved.Groups))
		for _, group := range resolved.Groups {
			messages = append(messages, group.Message)
		}
		if err := policy.ValidateMessages(messagePolicy, messages, options.allowWIP); err != nil {
			return resolvedCommitPlan{}, err
		}
		return resolved, nil
	}
	if options.single {
		if strings.TrimSpace(options.message) == "" {
			return resolvedCommitPlan{}, fail.New("INVALID_ARGS", "--single requires --message")
		}
		if err := policy.Validate(messagePolicy, options.message, options.allowWIP); err != nil {
			return resolvedCommitPlan{}, err
		}
		selected, err := selectedStaged(ctx, laneStore, lane.CurrentSHA, allowed, options.paths)
		if err != nil {
			return resolvedCommitPlan{}, err
		}
		return resolvedCommitPlan{Groups: []engine.Group{{Message: options.message, Files: selected}}}, nil
	}
	if options.message != "" || len(options.paths) > 0 {
		return resolvedCommitPlan{}, fail.New("SPLIT_PLAN_REQUIRED", "use --single to opt out, or omit --message and --path for the interactive split planner")
	}
	if options.noInteractive {
		return resolvedCommitPlan{}, fail.New("SPLIT_PLAN_REQUIRED", "non-interactive capture requires --plan or an explicit --single")
	}
	selected, err := selectedStaged(ctx, laneStore, lane.CurrentSHA, allowed, nil)
	if err != nil {
		return resolvedCommitPlan{}, err
	}
	promptOutput := application.stdout
	if application.jsonMode {
		promptOutput = application.stderr
	}
	groups, err := interactiveGroups(selected, messagePolicy, options.allowWIP, prompt{reader: bufio.NewReader(application.stdin), out: promptOutput})
	return resolvedCommitPlan{Groups: groups}, err
}

func decodePlan(reader io.Reader) ([]engine.Group, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxPlanBytes+1))
	if err != nil {
		return nil, fail.Wrap("INVALID_PLAN", err)
	}
	if len(body) > maxPlanBytes {
		return nil, fail.Errorf("INVALID_PLAN", "plan exceeds %d bytes", maxPlanBytes)
	}
	return decodeLegacyPlan(body)
}

func selectedStaged(ctx context.Context, laneStore store.Store, laneCommit string, allowed []string, requested []string) ([]string, error) {
	staged, err := laneStore.Repo.NULPaths(ctx, nil, "diff", "--cached", "--no-renames", "--name-only", "-z", laneCommit, "--")
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

func interactiveGroups(paths []string, messagePolicy policy.Receipt, allowWIP bool, prompter prompt) ([]engine.Group, error) {
	proposals := proposeSplitGroups(paths)
	_, _ = fmt.Fprintln(prompter.out, "Proposed split groups (one ref update after every group passes):")
	for _, proposal := range proposals {
		_, _ = fmt.Fprintf(prompter.out, "  %s (prefix %s):\n    %s\n", proposal.Key, proposal.SuggestedPrefix, strings.Join(proposal.Files, "\n    "))
	}
	accepted, err := prompter.confirm("Use these split groups", true)
	if err != nil {
		return nil, err
	}
	if !accepted {
		return nil, fail.New("SPLIT_PLAN_REQUIRED", "write a JSON plan to define different groups")
	}
	seen := map[string]bool{}
	groups := make([]engine.Group, 0, len(proposals))
	for _, proposal := range proposals {
		for {
			message, err := prompter.ask("Conventional Commit message for "+proposal.Key+" (start with "+proposal.SuggestedPrefix+")", "")
			if err != nil {
				return nil, err
			}
			if err := policy.Validate(messagePolicy, message, allowWIP); err != nil {
				_, _ = fmt.Fprintf(prompter.out, "%s: %s\n", fail.Code(err), err)
				continue
			}
			normalized := strings.ToLower(strings.TrimSpace(message))
			if seen[normalized] {
				_, _ = fmt.Fprintln(prompter.out, "DUPLICATE_COMMIT_MESSAGE: use a distinct outcome for each group")
				continue
			}
			seen[normalized] = true
			groups = append(groups, engine.Group{Message: message, Files: proposal.Files})
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
	lockWait := set.Duration("lock-wait", 0, "time to wait for the lane lock; zero fails fast")
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
		laneContext, contextErr := laneContextFor(ctx, laneStore, lane.ID, renewalStatus{})
		if contextErr != nil {
			return application.failure("reconcile", contextErr, result, 1)
		}
		return application.successWithContext("reconcile", result, "plan is already fully reconciled", laneContext)
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
	laneContext, contextErr := laneContextFor(ctx, laneStore, lane.ID, renewalStatus{})
	if contextErr != nil {
		return application.failure("reconcile", contextErr, result, 1)
	}
	return application.successWithContext("reconcile", result, "reconciled lane "+lane.ID+" to "+intent.ExpectedNew, laneContext)
}
