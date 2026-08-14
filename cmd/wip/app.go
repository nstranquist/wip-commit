package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/store"
)

type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	jsonMode       bool
	repoDir        string
}

type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	OK     bool          `json:"ok"`
	Action string        `json:"action"`
	Data   any           `json:"data,omitempty"`
	Error  *errorPayload `json:"error,omitempty"`
}

func (application *app) run(ctx context.Context, args []string) int {
	command, rest, err := application.parseGlobal(args)
	if err != nil {
		return application.failure("usage", fail.Wrap("INVALID_ARGS", err), nil, 2)
	}
	switch command {
	case "":
		application.printHelp()
		return 0
	case "help", "--help", "-h":
		if len(rest) == 1 {
			if !application.printCommandHelp(rest[0]) {
				return 2
			}
		} else {
			application.printHelp()
		}
		return 0
	case "version", "--version":
		return application.success("version", map[string]string{"version": version}, "wip "+version)
	}
	if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
		if application.printCommandHelp(command) {
			return 0
		}
		return 2
	}
	repo, err := gitx.Discover(ctx, application.repoDir)
	if err != nil {
		return application.failure(command, err, nil, 1)
	}
	if command == "init" {
		return application.runInit(ctx, repo, rest)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		return application.failure(command, err, nil, 1)
	}
	switch command {
	case "status":
		return application.runStatus(laneStore, rest)
	case "env":
		return application.runEnv(laneStore, rest)
	case "claim":
		return application.runClaim(laneStore, rest)
	case "renew":
		return application.runRenew(laneStore, rest)
	case "commit":
		return application.runCommit(ctx, laneStore, rest)
	case "reconcile":
		return application.runReconcile(ctx, laneStore, rest)
	case "release":
		return application.runRelease(laneStore, rest, false)
	case "abort":
		return application.runRelease(laneStore, rest, true)
	default:
		return application.failure(command, fail.Errorf("INVALID_ARGS", "unknown command %q", command), nil, 2)
	}
}

func (application *app) parseGlobal(args []string) (string, []string, error) {
	for len(args) > 0 {
		switch args[0] {
		case "--json":
			application.jsonMode, args = true, args[1:]
		case "--repo-dir", "-C":
			if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
				return "", nil, errors.New(args[0] + " requires a directory")
			}
			application.repoDir, args = args[1], args[2:]
		default:
			return args[0], args[1:], nil
		}
	}
	return "", nil, nil
}

func (application app) success(action string, data any, human string) int {
	if application.jsonMode {
		_ = json.NewEncoder(application.stdout).Encode(envelope{OK: true, Action: action, Data: data})
	} else if human != "" {
		_, _ = fmt.Fprintln(application.stdout, human)
	}
	return 0
}

func (application app) failure(action string, err error, data any, code int) int {
	payload := &errorPayload{Code: fail.Code(err), Message: err.Error()}
	if application.jsonMode {
		_ = json.NewEncoder(application.stdout).Encode(envelope{OK: false, Action: action, Data: data, Error: payload})
	} else {
		_, _ = fmt.Fprintf(application.stderr, "wip %s: %s: %s\n", action, payload.Code, payload.Message)
	}
	return code
}

func (application app) flagSet(command string) *flag.FlagSet {
	set := flag.NewFlagSet("wip "+command, flag.ContinueOnError)
	set.SetOutput(application.stderr)
	return set
}

func (application app) printHelp() {
	output := application.stdout
	_, _ = fmt.Fprintln(output, `wip safely captures exact staged subsets on agent-owned refs.

Usage:
  wip [--json] [--repo-dir DIR] <command> [options]

Commands:
  init        interactively install and initialize a shared or worktree lane
  status      show the active lane and leases
  env         print shell environment for a lane
  claim       claim additional paths
  renew       renew active path leases
  commit      capture one atomic split plan
  reconcile   finish metadata after an interrupted successful ref update
  release     release the lane and its leases
  abort       abort the lane and release its leases
  version     print the version

Run wip <command> --help for command options.`)
}

func (application app) printCommandHelp(command string) bool {
	usage := map[string]string{
		"init": `Usage: wip init [options]

Interactively initialize and optionally install wip. Important options:
  --mode shared|worktree       coordination mode
  --create-worktree PATH       explicitly create or reuse a linked worktree
  --lane ID                    short task slug
  --agent ID --session ID      owner identity
  --base-ref REF               starting commit (default HEAD)
  --path PATH                  path claim; repeatable
  --install --install-dir DIR  copy this binary without overwriting
  --non-interactive --yes      do not prompt
  --dry-run                    validate without changing Git or installing`,
		"commit": `Usage: wip commit [options]

Capture one all-or-nothing staged split plan. Important options:
  --plan FILE                  strict JSON plan, or - for stdin
  --single --message MESSAGE   explicit one-commit opt-out
  --path PATH                  staged scope for --single; repeatable
  --dry-run                    run gates without commits or a ref update
  --allow-wip                  authorize the wip: prefix
  --hook-timeout DURATION      hook limit
  --verify-timeout DURATION    default verify limit
  --lock-wait DURATION         lane-lock wait`,
		"status":    "Usage: wip status [--lane ID] [--agent ID] [--session ID]",
		"env":       "Usage: wip env --lane ID",
		"claim":     "Usage: wip claim [identity options] --path PATH [--path PATH...]",
		"renew":     "Usage: wip renew [--lane ID] [--agent ID] [--session ID]",
		"reconcile": "Usage: wip reconcile [identity options] --plan-id ID --plan-digest SHA256",
		"release":   "Usage: wip release [--lane ID] [--agent ID] [--session ID]",
		"abort":     "Usage: wip abort [--lane ID] [--agent ID] [--session ID]",
		"version":   "Usage: wip version",
	}
	if text, ok := usage[command]; ok {
		_, _ = fmt.Fprintln(application.stdout, text)
		return true
	}
	_, _ = fmt.Fprintf(application.stderr, "unknown command %q\n", command)
	return false
}

func noArgs(set *flag.FlagSet) error {
	if set.NArg() != 0 {
		return fail.Errorf("INVALID_ARGS", "unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	return nil
}

func envDefault(name string) string { return strings.TrimSpace(os.Getenv(name)) }
