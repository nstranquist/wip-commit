package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/recordjson"
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

const (
	profileSchemaVersion = 1
	maxProfileBytes      = 64 << 10
)

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

func profilePath(laneStore store.Store, lane string) (string, error) {
	if err := store.ValidateID(lane, "lane"); err != nil {
		return "", err
	}
	return filepath.Join(laneStore.Root, "profiles", lane+".json"), nil
}

func writeProfile(laneStore store.Store, lane store.Lane) (string, error) {
	wanted := profile{SchemaVersion: profileSchemaVersion, Lane: lane.ID, Agent: lane.Agent, Session: lane.Session, Mode: lane.Mode, Worktree: lane.Worktree}
	body, err := recordjson.Marshal(wanted, maxProfileBytes, "PROFILE_WRITE_FAILED", "lane profile")
	if err != nil {
		return "", err
	}
	path, err := profilePath(laneStore, lane.ID)
	if err != nil {
		return "", err
	}
	err = atomicfile.CreateWithTempDir(path, filepath.Join(laneStore.Root, "locks"), body, 0o600)
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
	path, err := profilePath(laneStore, lane)
	if err != nil {
		return value, err
	}
	body, err := safeio.ReadRegular(path, maxProfileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return value, fail.New("PROFILE_NOT_FOUND", "lane profile not found; pass --agent and --session or rerun wip init")
	}
	if err != nil {
		return value, fail.Wrap("PROFILE_READ_FAILED", err)
	}
	if err := strictjson.Decode(body, &value); err != nil {
		return profile{}, fail.Wrap("PROFILE_READ_FAILED", err)
	}
	if value.SchemaVersion != profileSchemaVersion {
		return profile{}, fail.Errorf("MIGRATION_REQUIRED", "profile schema version %d is unsupported; this wip release supports version %d", value.SchemaVersion, profileSchemaVersion)
	}
	if value.Lane != lane {
		return profile{}, fail.New("PROFILE_READ_FAILED", "lane profile identity is invalid")
	}
	for label, identifier := range map[string]string{"lane": value.Lane, "agent": value.Agent, "session": value.Session} {
		if err := store.ValidateID(identifier, label); err != nil {
			return profile{}, fail.Wrap("PROFILE_READ_FAILED", err)
		}
	}
	if value.Mode != store.ModeShared && value.Mode != store.ModeWorktree {
		return profile{}, fail.New("PROFILE_READ_FAILED", "lane profile mode is invalid")
	}
	if value.Worktree == "" || !filepath.IsAbs(value.Worktree) || filepath.Clean(value.Worktree) != value.Worktree {
		return profile{}, fail.New("PROFILE_READ_FAILED", "lane profile worktree is invalid")
	}
	laneRecord, err := laneStore.Load(lane)
	if err != nil {
		return profile{}, err
	}
	if laneRecord.Agent != value.Agent || laneRecord.Session != value.Session || laneRecord.Mode != value.Mode || laneRecord.Worktree != value.Worktree {
		return profile{}, fail.New("PROFILE_READ_FAILED", "lane profile does not match the authoritative lane manifest")
	}
	return value, nil
}

func resolveStatus(laneStore store.Store, identity identityFlags) (store.Status, error) {
	if identity.lane != "" && (identity.agent == "" || identity.session == "") {
		saved, err := loadProfile(laneStore, identity.lane)
		if err != nil {
			return store.Status{}, err
		}
		if identity.agent == "" {
			identity.agent = saved.Agent
		}
		if identity.session == "" {
			identity.session = saved.Session
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
		return "$env:WIP_LANE = " + quotePowerShell(saved.Lane) + "\n$env:WIP_AGENT = " + quotePowerShell(saved.Agent) + "\n$env:WIP_SESSION = " + quotePowerShell(saved.Session)
	}
	return "export WIP_LANE=" + quotePOSIX(saved.Lane) + "\nexport WIP_AGENT=" + quotePOSIX(saved.Agent) + "\nexport WIP_SESSION=" + quotePOSIX(saved.Session)
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func quotePowerShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func quoteCommandArg(value string) string {
	if runtime.GOOS == "windows" {
		return quotePowerShell(value)
	}
	return quotePOSIX(value)
}
