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
	stdin                  io.Reader
	stdout, stderr         io.Writer
	jsonMode               bool
	repoDir                string
	leaseHeartbeat         func(context.Context, store.Store, store.Lane, []string) (context.Context, func() error, error)
	beforeFinalRelease     func()
	afterHandoffValidation func()
	migrationProducer      migrationProducerFunc
}

type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	OK      bool          `json:"ok"`
	Action  string        `json:"action"`
	Data    any           `json:"data,omitempty"`
	Context *laneContext  `json:"context,omitempty"`
	Error   *errorPayload `json:"error,omitempty"`
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
		build := currentVersionInfo()
		return application.success("version", build, build.human())
	case "contract":
		result, contractErr := currentContractResult()
		if contractErr != nil {
			return application.failure("contract", fail.Wrap("CONTRACT_FAILED", contractErr), nil, 1)
		}
		return application.success("contract", result, result.human())
	}
	if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
		if application.printCommandHelp(command) {
			return 0
		}
		return 2
	}
	if command == "upgrade" {
		return application.runUpgrade(ctx, rest)
	}
	if command == "capture" {
		if err := application.preflightCapturePlan(rest); err != nil {
			return application.failure(command, err, nil, 2)
		}
	}
	repo, err := gitx.Discover(ctx, application.repoDir)
	if err != nil {
		return application.failure(command, err, nil, 1)
	}
	if command == "init" {
		return application.runInit(ctx, repo, rest)
	}
	if command == "doctor" {
		return application.runDoctor(ctx, repo, rest)
	}
	if command == "archive" {
		return application.runArchive(ctx, repo, rest)
	}
	if command == "migrate" {
		return application.runMigrate(ctx, repo, rest)
	}
	if command == "init-abort" {
		return application.runInitAbort(ctx, repo, rest)
	}
	readOnly := false
	switch command {
	case "env":
		readOnly = envObserveRequested(rest)
	case "handoff":
		// Handoff renews an active lane after it validates the capture receipt.
		// Keep the repository writer fence for that complete transaction.
		readOnly = false
	case "status":
		readOnly = statusObserveRequested(rest)
	case "plan":
		readOnly = planObserveRequested(rest)
	case "commit":
		readOnly = booleanFlagRequested(rest, "dry-run")
	case "claim", "renew", "capture", "reconcile", "release", "abort":
	default:
		return application.failure(command, fail.Errorf("INVALID_ARGS", "unknown command %q", command), nil, 2)
	}
	laneStore, initialized, err := store.Inspect(repo)
	if err == nil && !initialized {
		err = fail.New("LANE_NOT_ACTIVE", "wip state is not initialized; run wip init before using lane commands")
	}
	if err != nil {
		return application.failure(command, err, nil, 1)
	}
	if !readOnly {
		writerFence, fenceErr := store.AcquireWriterFence(repo, store.DefaultWriterFenceWait)
		if fenceErr != nil {
			return application.failure(command, fenceErr, nil, 1)
		}
		defer func() { _ = writerFence.Release() }()
		laneStore, err = store.OpenWithWriterFence(repo, writerFence)
	}
	if err != nil {
		return application.failure(command, err, nil, 1)
	}
	switch command {
	case "status":
		return application.runStatus(ctx, laneStore, rest)
	case "env":
		return application.runEnv(ctx, laneStore, rest)
	case "claim":
		return application.runClaim(ctx, laneStore, rest)
	case "renew":
		return application.runRenew(ctx, laneStore, rest)
	case "plan":
		return application.runPlan(ctx, laneStore, rest)
	case "commit":
		return application.runCommit(ctx, laneStore, rest)
	case "capture":
		return application.runCapture(ctx, laneStore, rest)
	case "handoff":
		return application.runHandoff(ctx, laneStore, rest)
	case "reconcile":
		return application.runReconcile(ctx, laneStore, rest)
	case "release":
		return application.runRelease(ctx, laneStore, rest, false)
	case "abort":
		return application.runRelease(ctx, laneStore, rest, true)
	}
	return application.failure(command, fail.Errorf("INVALID_ARGS", "unknown command %q", command), nil, 2)
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
	return application.successWithContext(action, data, human, nil)
}

func (application app) successWithContext(action string, data any, human string, laneContext *laneContext) int {
	if application.jsonMode {
		_ = json.NewEncoder(application.stdout).Encode(envelope{OK: true, Action: action, Data: data, Context: laneContext})
	} else if human != "" {
		_, _ = fmt.Fprintln(application.stdout, human)
	}
	return 0
}

func (application app) failure(action string, err error, data any, code int) int {
	return application.failureWithContext(action, err, data, code, nil)
}

func (application app) failureWithContext(action string, err error, data any, code int, laneContext *laneContext) int {
	payload := &errorPayload{Code: fail.Code(err), Message: err.Error()}
	if application.jsonMode {
		_ = json.NewEncoder(application.stdout).Encode(envelope{OK: false, Action: action, Data: data, Context: laneContext, Error: payload})
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
	_, _ = fmt.Fprintln(output, `wip safely captures exact private sources on agent-owned refs.

Usage:
  wip [--json] [--repo-dir DIR] <command> [options]

Commands:
  init        interactively install and initialize a shared or worktree lane
  init-abort  preview or abort one safe pre-lane initialization intent
  doctor      audit bounded state, refs, and recovery requirements
  archive     preview or apply recoverable state-record archival
  upgrade     preview or apply one digest-bound state, binary, or skill upgrade
  migrate     preview or apply one digest-bound coordination migration
  status      classify staged state and show lane, lease, ref, and intent health
  env         print shell environment for a lane
  claim       claim additional paths
  renew       renew active path leases
  plan        create one bound draft for selected leased source paths
  commit      capture one atomic split plan
  capture     capture one reviewed bound plan and emit a typed handoff
  handoff     validate and print a capture-only handoff
  reconcile   finish metadata after an interrupted successful ref update
  release     release the lane and its leases
  abort       abort the lane and release its leases
  version     print the version
  contract    print the machine-readable command and skill contract

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
  --message-policy ID          override the repository message policy
  --path PATH                  path claim; repeatable
  --install --install-dir DIR  copy this binary without overwriting
  --install-skill --skill-dir DIR
                               install the embedded portable skill safely
  --non-interactive --yes      do not prompt
  --dry-run                    validate without changing Git or installing`,
		"init-abort": `Usage: wip init-abort --intent ID [options]

Preview is the default. Apply requires the digest from that preview.
  --intent ID                  pending initialization intent
  --intent-digest SHA256       exact digest from the reviewed preview
  --apply --yes                remove a safe intent before lane creation`,
		"commit": `Usage: wip commit [options]

Capture one all-or-nothing private-source split plan. Important options:
  --plan FILE                  strict JSON plan, or - for stdin
  --single --message MESSAGE   explicit one-commit compatibility path
  --path PATH                  staged scope for --single; repeatable
  --dry-run                    validate structure without hooks, verify commands, commits, or a ref update
  --allow-wip                  authorize the wip: prefix
  --hook-timeout DURATION      hook limit
  --verify-timeout DURATION    default verify limit
  --lock-wait DURATION         lane-lock wait; zero fails fast`,
		"capture": `Usage: wip capture --plan FILE [options]

Capture one reviewed v3 plan and emit a validated cumulative capture-only handoff.
  --plan FILE                  reviewed bound plan from wip plan
  --review-plan-digest SHA256 exact canonical digest supplied by the reviewer
  --final                      release the lane after the handoff validates
  --allow-wip                  authorize the wip: prefix
  --hook-timeout DURATION      hook limit
  --verify-timeout DURATION    default verify limit
  --lock-wait DURATION         lane-lock wait; zero fails fast`,
		"handoff": "Usage: wip handoff [identity options] [--plan-id ID --plan-digest SHA256]\n\nValidate and output one cumulative, capture-only, non-authorizing handoff. Active-lane use renews leases.",
		"status":  "Usage: wip status [--lane ID] [--agent ID] [--session ID] [--observe]\n\nStatus renews active leases by default. Use --observe for a pure read.",
		"doctor":  "Usage: wip doctor\n\nRead-only audit of bounded state records, refs, and recovery intents.",
		"archive": `Usage: wip archive [options]

Preview is the default. Important options:
  --older-than DURATION       minimum released-lane age (default 720h)
  --lane ID                   narrow to an eligible lane; repeatable
  --cutoff TIME --plan-digest SHA256
                              exact values from the reviewed preview
                              repeat every preview --lane value on apply
  --apply --yes               apply the exact reviewed archive plan
  --resume ARCHIVE_ID         resume a prepared receipt; requires --apply --yes
  --restore ARCHIVE_ID        restore records; also requires --apply --yes`,
		"env":   "Usage: wip env --lane ID [--observe]",
		"claim": "Usage: wip claim [identity options] --path PATH [--path PATH...]",
		"renew": "Usage: wip renew [--lane ID] [--agent ID] [--session ID]",
		"plan": `Usage: wip plan [identity options] [--path PATH...] [--out FILE]

Create one bound private-source draft. Add messages and set its state to reviewed before capture.
  --source staged_index|worktree_snapshot
                              select the exact source model
  --untracked not_applicable|forbid|include
                              select explicit untracked behavior
  --observe                   do not renew leases or write an output file`,
		"reconcile": "Usage: wip reconcile [identity options] --plan-id ID --plan-digest SHA256",
		"release":   "Usage: wip release [--lane ID] [--agent ID] [--session ID]",
		"abort":     "Usage: wip abort [--lane ID] [--agent ID] [--session ID]",
		"version":   "Usage: wip version",
		"contract":  "Usage: wip contract\n\nPrint the command protocol, capabilities, and portable skill digest.",
		"upgrade": `Usage: wip upgrade (--state | --binary | --skill) [options]

Preview is the default. Apply one exact reviewed target at a time.
  --plan-digest SHA256         exact digest from the reviewed preview
  --apply --yes                apply only that byte-bound plan
  --install-dir DIR            binary install directory (default ~/.local/bin)
  --skill-dir DIR              agent skill directory (default ~/.agents/skills)`,
		"migrate": `Usage: wip migrate (--preview | --status | --apply | --resume | --rollback) [options]

Experimental coordination-domain migration. Preview and status are read-only.
  --preview                    emit the complete immutable migration plan
  --status [--migration-id ID] inspect journal, receipt, and archive state
  --apply|--resume|--rollback  select one exact mutation mode
  --plan FILE                  strict reviewed plan; use - for stdin
  --plan-digest SHA256 --yes   bind and confirm the exact reviewed plan
  --lock-wait DURATION         common writer-lock wait; zero fails fast`,
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
