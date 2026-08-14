package main

import (
	"fmt"
	"strings"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/store"
)

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func (application app) runStatus(laneStore store.Store, args []string) int {
	set := application.flagSet("status")
	var identity identityFlags
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
	active, err := laneStore.ActivePaths(status.Lane.ID)
	if err != nil {
		return application.failure("status", err, nil, 1)
	}
	data := struct {
		store.Status
		ActivePaths []string `json:"active_paths"`
	}{Status: status, ActivePaths: active}
	human := fmt.Sprintf("lane %s (%s) -> %s\nref %s\nactive paths: %s", status.Lane.ID, status.Lane.Mode, status.Lane.CurrentSHA, status.Lane.Ref, strings.Join(active, ", "))
	return application.success("status", data, human)
}

func (application app) runEnv(laneStore store.Store, args []string) int {
	set := application.flagSet("env")
	lane := set.String("lane", envDefault("WIP_LANE"), "lane id (default WIP_LANE)")
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
	return application.success("env", saved, shellEnvironment(saved))
}

func (application app) runClaim(laneStore store.Store, args []string) int {
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
	return application.success("claim", lease, fmt.Sprintf("claimed %s for lane %s until %s", strings.Join(lease.Paths, ", "), lease.LaneID, lease.ExpiresAt.Format("2006-01-02T15:04:05Z")))
}

func (application app) runRenew(laneStore store.Store, args []string) int {
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
	return application.success("renew", leases, fmt.Sprintf("renewed %d lease(s) for lane %s", len(leases), status.Lane.ID))
}

func (application app) runRelease(laneStore store.Store, args []string, abort bool) int {
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
	return application.success(action, map[string]string{"lane": status.Lane.ID, "state": state}, fmt.Sprintf("%s lane %s; its ref was preserved", state, status.Lane.ID))
}
