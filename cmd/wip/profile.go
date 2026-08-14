package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/safeio"
	"github.com/nstranquist/wip-commit/internal/store"
	"github.com/nstranquist/wip-commit/internal/strictjson"
)

type profile struct {
	SchemaVersion int        `json:"schema_version"`
	Lane          string     `json:"lane"`
	Agent         string     `json:"agent"`
	Session       string     `json:"session"`
	Mode          store.Mode `json:"mode"`
	Worktree      string     `json:"worktree"`
}

type identityFlags struct {
	lane, agent, session string
}

func (identity *identityFlags) bind(set flagBinder) {
	set.StringVar(&identity.lane, "lane", envDefault("WIP_LANE"), "lane id (default WIP_LANE)")
	set.StringVar(&identity.agent, "agent", envDefault("WIP_AGENT"), "agent id (default WIP_AGENT)")
	set.StringVar(&identity.session, "session", envDefault("WIP_SESSION"), "session id (default WIP_SESSION)")
}

type flagBinder interface {
	StringVar(*string, string, string, string)
}

func profilePath(laneStore store.Store, lane string) string {
	return filepath.Join(laneStore.Root, "profiles", lane+".json")
}

func writeProfile(laneStore store.Store, lane store.Lane) (string, error) {
	wanted := profile{SchemaVersion: 1, Lane: lane.ID, Agent: lane.Agent, Session: lane.Session, Mode: lane.Mode, Worktree: lane.Worktree}
	body, err := json.MarshalIndent(wanted, "", "  ")
	if err != nil {
		return "", fail.Wrap("PROFILE_WRITE_FAILED", err)
	}
	path := profilePath(laneStore, lane.ID)
	err = atomicfile.Create(path, append(body, '\n'), 0o600)
	if err == nil {
		return path, nil
	}
	if !errors.Is(err, atomicfile.ErrExists) {
		return "", fail.Wrap("PROFILE_WRITE_FAILED", err)
	}
	existing, loadErr := loadProfile(laneStore, lane.ID)
	if loadErr != nil {
		return "", loadErr
	}
	if existing != wanted {
		return "", fail.New("PROFILE_CONFLICT", "existing lane profile does not match this setup")
	}
	return path, nil
}

func loadProfile(laneStore store.Store, lane string) (profile, error) {
	var value profile
	body, err := safeio.ReadRegular(profilePath(laneStore, lane), 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return value, fail.New("PROFILE_NOT_FOUND", "lane profile not found; pass --agent and --session or rerun wip init")
	}
	if err != nil {
		return value, fail.Wrap("PROFILE_READ_FAILED", err)
	}
	if err := strictjson.Decode(body, &value); err != nil || value.SchemaVersion != 1 || value.Lane != lane {
		return profile{}, fail.New("PROFILE_READ_FAILED", "lane profile is invalid or unsupported")
	}
	return value, nil
}

func resolveStatus(laneStore store.Store, identity identityFlags) (store.Status, error) {
	if identity.lane != "" && (identity.agent == "" || identity.session == "") {
		if saved, err := loadProfile(laneStore, identity.lane); err == nil {
			if identity.agent == "" {
				identity.agent = saved.Agent
			}
			if identity.session == "" {
				identity.session = saved.Session
			}
		}
	}
	status, err := laneStore.Current(identity.agent, identity.session, identity.lane)
	if err != nil {
		return store.Status{}, err
	}
	if identity.agent != "" && status.Lane.Agent != identity.agent || identity.session != "" && status.Lane.Session != identity.session {
		return store.Status{}, fail.New("IDENTITY_MISMATCH", "lane belongs to another agent or session")
	}
	return status, nil
}

func shellEnvironment(saved profile) string {
	if runtime.GOOS == "windows" {
		return "$env:WIP_LANE = '" + saved.Lane + "'\n$env:WIP_AGENT = '" + saved.Agent + "'\n$env:WIP_SESSION = '" + saved.Session + "'"
	}
	return "export WIP_LANE='" + saved.Lane + "'\nexport WIP_AGENT='" + saved.Agent + "'\nexport WIP_SESSION='" + saved.Session + "'"
}
