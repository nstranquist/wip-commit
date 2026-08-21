package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/store"
)

func TestInitAbortRequiresReviewedDigestAndRemovesOnlyPreLaneIntent(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	base := cliGit(t, directory, "rev-parse", "HEAD")
	intent, intentPath, err := beginInitIntent(laneStore, initIntent{
		Lane: "failed-setup", Agent: "agent", Session: "session", Mode: store.ModeShared,
		BaseSHA: base, Worktree: directory, Paths: []string{"core.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err = markInitStep(intentPath, intent, "worktree-ready")
	if err != nil {
		t.Fatal(err)
	}

	preview := runCLI(t, []string{"--json", "--repo-dir", directory, "init-abort", "--intent", intent.ID}, "")
	if preview.code != 0 || !preview.envelope.OK {
		t.Fatalf("preview: %#v stderr=%s", preview.envelope, preview.stderr)
	}
	var previewResult initAbortResult
	decodeData(t, preview.envelope.Data, &previewResult)
	if !previewResult.CanAbort || previewResult.Applied || previewResult.IntentDigest != intent.Digest {
		t.Fatalf("preview result = %#v", previewResult)
	}

	moved := runCLI(t, []string{"--json", "--repo-dir", directory, "init-abort", "--intent", intent.ID, "--intent-digest", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--apply", "--yes"}, "")
	if moved.code == 0 || moved.envelope.Error == nil || moved.envelope.Error.Code != "INIT_INTENT_MOVED" {
		t.Fatalf("moved result = %#v", moved.envelope)
	}
	if _, err := os.Stat(intentPath); err != nil {
		t.Fatalf("intent changed after rejected digest: %v", err)
	}

	aborted := runCLI(t, []string{"--json", "--repo-dir", directory, "init-abort", "--intent", intent.ID, "--intent-digest", intent.Digest, "--apply", "--yes"}, "")
	if aborted.code != 0 || !aborted.envelope.OK {
		t.Fatalf("abort: %#v stderr=%s", aborted.envelope, aborted.stderr)
	}
	var abortedResult initAbortResult
	decodeData(t, aborted.envelope.Data, &abortedResult)
	if !abortedResult.CanAbort || !abortedResult.Applied {
		t.Fatalf("abort result = %#v", abortedResult)
	}
	if abortedResult.ArchiveRef == "" || abortedResult.ArchiveObject == "" {
		t.Fatalf("missing archive evidence: %#v", abortedResult)
	}
	if _, err := os.Stat(intentPath); !os.IsNotExist(err) {
		t.Fatalf("intent still exists: %v", err)
	}
	if got := cliGit(t, directory, "rev-parse", "--verify", abortedResult.ArchiveRef+"^{blob}"); got != abortedResult.ArchiveObject {
		t.Fatalf("archive ref = %q, want %q", got, abortedResult.ArchiveObject)
	}

	retry := runCLI(t, []string{"--json", "--repo-dir", directory, "init-abort", "--intent", intent.ID, "--intent-digest", intent.Digest, "--apply", "--yes"}, "")
	if retry.code != 0 || !retry.envelope.OK {
		t.Fatalf("idempotent retry: %#v stderr=%s", retry.envelope, retry.stderr)
	}
	var retryResult initAbortResult
	decodeData(t, retry.envelope.Data, &retryResult)
	if !retryResult.Applied || retryResult.ArchiveObject != abortedResult.ArchiveObject {
		t.Fatalf("retry result = %#v", retryResult)
	}
}

func TestInitAbortRefusesIntentAfterLaneCreation(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	base := cliGit(t, directory, "rev-parse", "HEAD")
	intent, intentPath, err := beginInitIntent(laneStore, initIntent{
		Lane: "created-lane", Agent: "agent", Session: "session", Mode: store.ModeShared,
		BaseSHA: base, Worktree: directory, Paths: []string{"core.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err = markInitStep(intentPath, intent, "worktree-ready")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laneStore.Create(context.Background(), store.CreateOptions{
		ID: intent.Lane, Agent: intent.Agent, Session: intent.Session,
		BaseRef: base, Mode: store.ModeShared, Worktree: directory,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := inspectInitAbort(context.Background(), laneStore, intent.ID, intent.Digest, true)
	if fail.Code(err) != "INIT_ABORT_REFUSED" || result.Applied {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if _, err := os.Stat(intentPath); err != nil {
		t.Fatalf("intent changed after refusal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(laneStore.Root, "lanes", intent.Lane+".json")); err != nil {
		t.Fatalf("lane changed after refusal: %v", err)
	}
}

func TestInitAbortWaitsForInitTransactionBeforeLaneDecision(t *testing.T) {
	directory := cliTestRepo(t)
	repo, err := gitx.Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	laneStore, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	base := cliGit(t, directory, "rev-parse", "HEAD")
	intent, intentPath, err := beginInitIntent(laneStore, initIntent{
		Lane: "racing-setup", Agent: "agent", Session: "session", Mode: store.ModeShared,
		BaseSHA: base, Worktree: directory, Paths: []string{"core.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err = markInitStep(intentPath, intent, "worktree-ready")
	if err != nil {
		t.Fatal(err)
	}

	transactionStarted := make(chan struct{})
	allowLaneCreation := make(chan struct{})
	transactionDone := make(chan error, 1)
	go func() {
		_, transactionErr := runInitTransaction(intentPath, intent, func(transaction *initTransaction) error {
			close(transactionStarted)
			<-allowLaneCreation
			if _, createErr := laneStore.Create(context.Background(), store.CreateOptions{
				ID: intent.Lane, Agent: intent.Agent, Session: intent.Session,
				BaseRef: base, Mode: store.ModeShared, Worktree: directory,
			}); createErr != nil {
				return createErr
			}
			_, markErr := transaction.mark("lane-ready")
			return markErr
		})
		transactionDone <- transactionErr
	}()
	select {
	case <-transactionStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("initialization transaction did not start")
	}

	type abortOutcome struct {
		result initAbortResult
		err    error
	}
	abortStarted := make(chan struct{})
	abortDone := make(chan abortOutcome, 1)
	go func() {
		close(abortStarted)
		result, abortErr := inspectInitAbort(context.Background(), laneStore, intent.ID, intent.Digest, true)
		abortDone <- abortOutcome{result: result, err: abortErr}
	}()
	<-abortStarted

	var premature *abortOutcome
	select {
	case outcome := <-abortDone:
		premature = &outcome
	case <-time.After(100 * time.Millisecond):
	}
	if _, statErr := os.Stat(intentPath); statErr != nil {
		close(allowLaneCreation)
		t.Fatalf("abort changed the intent during initialization: %v", statErr)
	}
	close(allowLaneCreation)
	select {
	case transactionErr := <-transactionDone:
		if transactionErr != nil {
			t.Fatalf("initialization transaction: %v", transactionErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initialization transaction did not finish")
	}
	if premature != nil {
		t.Fatalf("abort completed inside the initialization transaction: result=%#v err=%v", premature.result, premature.err)
	}
	var outcome abortOutcome
	select {
	case outcome = <-abortDone:
	case <-time.After(2 * time.Second):
		t.Fatal("abort did not finish after initialization released its lock")
	}
	if fail.Code(outcome.err) != "INIT_ABORT_REFUSED" || outcome.result.Applied {
		t.Fatalf("abort result = %#v, err = %v", outcome.result, outcome.err)
	}
	if _, statErr := os.Stat(intentPath); statErr != nil {
		t.Fatalf("refused abort changed the intent: %v", statErr)
	}
}
