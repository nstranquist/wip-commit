package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/store"
)

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func statusObserveRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--observe" {
			return true
		}
	}
	return false
}

func (application app) runStatus(ctx context.Context, laneStore store.Store, args []string) int {
	set := application.flagSet("status")
	var identity identityFlags
	observe := set.Bool("observe", false, "inspect without renewing active leases")
	identity.bind(set)
	if err := set.Parse(args); err != nil {
		return application.failure("status", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("status", err, nil, 2)
	}
	status, err := resolveStatus(laneStore, identity)
	if err != nil {
		return application.failure("status", err, nil, 1)
	}
	renewal := renewalStatus{}
	if !*observe {
		renewal.Attempted = true
		if _, err := laneStore.Renew(status.Lane.ID, status.Lane.Agent, status.Lane.Session); err != nil {
			data, inspectErr := buildOperationalStatus(ctx, laneStore, status, renewal)
			if inspectErr != nil {
				return application.failure("status", err, nil, 1)
			}
			return application.failureWithContext("status", err, data, 1, data.context())
		}
		now := time.Now().UTC()
		renewal.Succeeded, renewal.RenewedAt = true, &now
		status, err = laneStore.Status(status.Lane.ID)
		if err != nil {
			return application.failure("status", err, nil, 1)
		}
	}
	data, err := buildOperationalStatus(ctx, laneStore, status, renewal)
	if err != nil {
		return application.failure("status", err, nil, 1)
	}
	human := fmt.Sprintf("lane %s (%s) -> %s\nref %s\ncapture candidates: %s\nalready captured and staged: %s\nforeign staged: %s\nlease remaining: %dms", status.Lane.ID, status.Lane.Mode, status.Lane.CurrentSHA, status.Lane.Ref, strings.Join(data.CaptureCandidates, ", "), strings.Join(data.CapturedButStillStaged, ", "), strings.Join(data.ForeignStaged, ", "), data.LeaseRemainingMS)
	return application.successWithContext("status", data, human, data.context())
}

func envObserveRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--observe" {
			return true
		}
	}
	return false
}

func (application app) runEnv(ctx context.Context, laneStore store.Store, args []string) int {
	set := application.flagSet("env")
	lane := set.String("lane", envDefault("WIP_LANE"), "lane id (default WIP_LANE)")
	observe := set.Bool("observe", false, "print without renewing active leases")
	if err := set.Parse(args); err != nil {
		return application.failure("env", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("env", err, nil, 2)
	}
	if strings.TrimSpace(*lane) == "" {
		return application.failure("env", fail.New("INVALID_ARGS", "--lane or WIP_LANE is required"), nil, 2)
	}
	saved, err := loadProfile(laneStore, *lane)
	if err != nil {
		return application.failure("env", err, nil, 1)
	}
	if laneStore.Repo.Root != saved.Worktree {
		return application.failure("env", fail.New("WORKTREE_MISMATCH", "lane is bound to a different checkout"), nil, 1)
	}
	renewal := renewalStatus{}
	if !*observe {
		renewal.Attempted = true
		if _, err := laneStore.Renew(saved.Lane, saved.Agent, saved.Session); err != nil {
			return application.failure("env", err, nil, 1)
		}
		renewal.Succeeded = true
	}
	laneContext, err := laneContextFor(ctx, laneStore, saved.Lane, renewal)
	if err != nil {
		return application.failure("env", err, nil, 1)
	}
	return application.successWithContext("env", saved, shellEnvironment(saved), laneContext)
}

func (application app) runClaim(ctx context.Context, laneStore store.Store, args []string) int {
	set := application.flagSet("claim")
	var identity identityFlags
	var paths stringList
	identity.bind(set)
	set.Var(&paths, "path", "repository-relative path to claim (repeatable)")
	if err := set.Parse(args); err != nil {
		return application.failure("claim", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("claim", err, nil, 2)
	}
	if len(paths) == 0 {
		return application.failure("claim", fail.New("INVALID_ARGS", "at least one --path is required"), nil, 2)
	}
	status, err := resolveStatus(laneStore, identity)
	if err != nil {
		return application.failure("claim", err, nil, 1)
	}
	lease, err := laneStore.Claim(status.Lane.ID, status.Lane.Agent, status.Lane.Session, paths)
	if err != nil {
		return application.failure("claim", err, nil, 1)
	}
	renewed, err := laneStore.Renew(status.Lane.ID, status.Lane.Agent, status.Lane.Session)
	if err != nil {
		return application.failure("claim", err, lease, 1)
	}
	for _, candidate := range renewed {
		if candidate.ID == lease.ID {
			lease = candidate
			break
		}
	}
	laneContext, err := laneContextFor(ctx, laneStore, status.Lane.ID, renewalStatus{Attempted: true, Succeeded: true})
	if err != nil {
		return application.failure("claim", err, lease, 1)
	}
	return application.successWithContext("claim", lease, fmt.Sprintf("claimed %s for lane %s until %s", strings.Join(lease.Paths, ", "), lease.LaneID, lease.ExpiresAt.Format("2006-01-02T15:04:05Z")), laneContext)
}

func (application app) runRenew(ctx context.Context, laneStore store.Store, args []string) int {
	set := application.flagSet("renew")
	var identity identityFlags
	identity.bind(set)
	if err := set.Parse(args); err != nil {
		return application.failure("renew", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure("renew", err, nil, 2)
	}
	status, err := resolveStatus(laneStore, identity)
	if err != nil {
		return application.failure("renew", err, nil, 1)
	}
	leases, err := laneStore.Renew(status.Lane.ID, status.Lane.Agent, status.Lane.Session)
	if err != nil {
		return application.failure("renew", err, nil, 1)
	}
	laneContext, err := laneContextFor(ctx, laneStore, status.Lane.ID, renewalStatus{Attempted: true, Succeeded: true})
	if err != nil {
		return application.failure("renew", err, leases, 1)
	}
	return application.successWithContext("renew", leases, fmt.Sprintf("renewed %d lease(s) for lane %s", len(leases), status.Lane.ID), laneContext)
}

func (application app) runRelease(ctx context.Context, laneStore store.Store, args []string, abort bool) int {
	action := "release"
	if abort {
		action = "abort"
	}
	set := application.flagSet(action)
	var identity identityFlags
	identity.bind(set)
	if err := set.Parse(args); err != nil {
		return application.failure(action, fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	if err := noArgs(set); err != nil {
		return application.failure(action, err, nil, 2)
	}
	status, err := resolveStatus(laneStore, identity)
	if err != nil {
		return application.failure(action, err, nil, 1)
	}
	if err := laneStore.Release(status.Lane.ID, status.Lane.Agent, status.Lane.Session, abort); err != nil {
		return application.failure(action, err, nil, 1)
	}
	state := "released"
	if abort {
		state = "aborted"
	}
	laneContext, contextErr := laneContextFor(ctx, laneStore, status.Lane.ID, renewalStatus{})
	if contextErr != nil {
		return application.failure(action, contextErr, map[string]string{"lane": status.Lane.ID, "state": state}, 1)
	}
	return application.successWithContext(action, map[string]string{"lane": status.Lane.ID, "state": state}, fmt.Sprintf("%s lane %s; its ref was preserved", state, status.Lane.ID), laneContext)
}
