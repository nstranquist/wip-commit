package engine

import (
	"context"
	"os"
	"testing"

	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
)

func TestCompleteIntentChainBindsEveryCaptureFromLaneBaseToTip(t *testing.T) {
	repo := engineTestRepo(t)
	base := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/cumulative"
	engineGit(t, repo.Root, "update-ref", target, base, "")

	first := completeTestCapture(t, repo, target, base, "core.txt", "first core\n", "fix(core): capture first segment")
	second := completeTestCapture(t, repo, target, first.FinalCommit, "docs.txt", "second docs\n", "docs(guide): capture second segment")

	chain, err := CompleteIntentChain(context.Background(), repo, target, base, second.FinalCommit)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 || chain[0].PlanID != first.PlanID || chain[1].PlanID != second.PlanID || chain[0].ExpectedOld != base || chain[0].ExpectedNew != chain[1].ExpectedOld || chain[1].ExpectedNew != second.FinalCommit {
		t.Fatalf("chain = %#v", chain)
	}
	for _, intent := range chain {
		if intent.SchemaVersion != intentSchemaVersion || intent.SourceMode != SourceStagedIndex || intent.UntrackedPolicy != UntrackedNotApplicable || len(intent.SourcePaths) != 1 {
			t.Fatalf("source-bound intent = %#v", intent)
		}
	}
}

func TestCompleteIntentChainFailsOnMissingSegment(t *testing.T) {
	repo := engineTestRepo(t)
	base := engineGit(t, repo.Root, "rev-parse", "HEAD")
	target := "refs/heads/wip/agent/cumulative-gap"
	engineGit(t, repo.Root, "update-ref", target, base, "")
	first := completeTestCapture(t, repo, target, base, "core.txt", "first core\n", "fix(core): capture first segment")
	second := completeTestCapture(t, repo, target, first.FinalCommit, "docs.txt", "second docs\n", "docs(guide): capture second segment")
	_, firstPath, err := LoadIntent(repo, first.PlanID, first.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(firstPath); err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteIntentChain(context.Background(), repo, target, base, second.FinalCommit); fail.Code(err) != "HANDOFF_CHAIN_INCOMPLETE" {
		t.Fatalf("error = %v, want HANDOFF_CHAIN_INCOMPLETE", err)
	}
}

func completeTestCapture(t *testing.T, repo gitx.Repo, target, expected, path, contents, message string) Result {
	t.Helper()
	write(t, repo.Root, path, contents)
	engineGit(t, repo.Root, "add", "--", path)
	result, err := Run(context.Background(), Options{
		Repo: repo, TargetRef: target, ExpectedRef: expected, ExpectedSourceHead: engineGit(t, repo.Root, "rev-parse", "HEAD"),
		AllowedPaths: []string{path}, Groups: []Group{{Message: message, Files: []string{path}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkIntent(repo, result.PlanID, result.PlanDigest, "complete"); err != nil {
		t.Fatal(err)
	}
	return result
}
