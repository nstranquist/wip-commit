package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nstranquist/wip-commit/internal/atomicfile"
	"github.com/nstranquist/wip-commit/internal/fail"
	"github.com/nstranquist/wip-commit/internal/gitx"
	"github.com/nstranquist/wip-commit/internal/pathid"
	"github.com/nstranquist/wip-commit/internal/recordjson"
	"github.com/nstranquist/wip-commit/internal/safeio"
	"github.com/nstranquist/wip-commit/internal/strictjson"
)

const (
	intentSchemaVersion = 1
	maxIntentBytes      = 1 << 20
)

var planIDPattern = regexp.MustCompile(`^plan-[0-9a-f]{24}$`)

var (
	intentIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	intentObjectPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	intentDigestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Intent struct {
	SchemaVersion     int             `json:"schema_version"`
	PlanID            string          `json:"plan_id"`
	PlanDigest        string          `json:"plan_digest"`
	State             string          `json:"state"`
	TargetRef         string          `json:"target_ref"`
	ExpectedOld       string          `json:"expected_old"`
	ExpectedNew       string          `json:"expected_new"`
	SourceHead        string          `json:"source_head"`
	SourceIndexDigest string          `json:"source_index_digest"`
	FinalTree         string          `json:"final_tree"`
	HookDigest        string          `json:"hook_digest"`
	RequestedPaths    []string        `json:"requested_paths"`
	AllowedPaths      []string        `json:"allowed_paths"`
	Commits           []PlannedCommit `json:"commits"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type ReconcileResult struct {
	PlanID       string `json:"plan_id"`
	PlanDigest   string `json:"plan_digest"`
	Commit       string `json:"commit"`
	IntentState  string `json:"intent_state"`
	AlreadyClean bool   `json:"already_clean"`
}

func newIntent(result Result, allowed []string) (Intent, error) {
	now := time.Now().UTC()
	intent := Intent{
		SchemaVersion:     intentSchemaVersion,
		State:             "prepared",
		TargetRef:         result.TargetRef,
		ExpectedOld:       result.ExpectedRef,
		ExpectedNew:       result.FinalCommit,
		SourceHead:        result.SourceHead,
		SourceIndexDigest: result.SourceIndexDigest,
		FinalTree:         result.FinalTree,
		HookDigest:        result.HookDigest,
		RequestedPaths:    append([]string(nil), result.RequestedPaths...),
		AllowedPaths:      append([]string(nil), allowed...),
		Commits:           append([]PlannedCommit(nil), result.Commits...),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	intent.PlanID = canonicalIntentID(intent.TargetRef, intent.ExpectedOld, intent.ExpectedNew, intent.SourceIndexDigest)
	sort.Strings(intent.AllowedPaths)
	digest, err := digestIntent(intent)
	if err != nil {
		return Intent{}, err
	}
	intent.PlanDigest = digest
	if err := validateIntentRecordCapacity(intent); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

func digestIntent(intent Intent) (string, error) {
	intent.PlanDigest = ""
	intent.State = ""
	intent.UpdatedAt = time.Time{}
	body, err := json.Marshal(intent)
	if err != nil {
		return "", fail.Wrap("INTENT_WRITE_FAILED", err)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func createIntent(repo gitx.Repo, intent Intent) (string, error) {
	path, err := intentPath(repo, intent.PlanID)
	if err != nil {
		return "", err
	}
	body, err := marshalIntentRecord(intent)
	if err != nil {
		return "", err
	}
	err = atomicfile.CreateWithTempDir(path, filepath.Join(repo.CommonDir, "wip", "v1", "locks"), body, 0o600)
	if err == nil {
		return path, nil
	}
	if !errors.Is(err, atomicfile.ErrExists) {
		return "", fail.Wrap("INTENT_WRITE_FAILED", err)
	}
	existing, _, loadErr := LoadIntent(repo, intent.PlanID, intent.PlanDigest)
	if loadErr != nil {
		return "", loadErr
	}
	if existing.State != "prepared" {
		return "", fail.Errorf("INTENT_EXISTS", "plan intent already exists in state %s", existing.State)
	}
	return path, nil
}

func LoadIntent(repo gitx.Repo, planID, expectedDigest string) (Intent, string, error) {
	var intent Intent
	path, err := intentPath(repo, planID)
	if err != nil {
		return intent, "", err
	}
	body, err := safeio.ReadRegular(path, maxIntentBytes)
	if errors.Is(err, os.ErrNotExist) {
		return intent, path, fail.New("INTENT_NOT_FOUND", "plan intent does not exist")
	}
	if err != nil {
		return intent, path, fail.Wrap("INTENT_READ_FAILED", err)
	}
	if err := strictjson.Decode(body, &intent); err != nil {
		return intent, path, fail.Wrap("INTENT_READ_FAILED", err)
	}
	if intent.SchemaVersion != intentSchemaVersion {
		return intent, path, fail.Errorf("MIGRATION_REQUIRED", "intent schema version %d is unsupported; this wip release supports version %d", intent.SchemaVersion, intentSchemaVersion)
	}
	if intent.PlanID != planID || !planIDPattern.MatchString(intent.PlanID) {
		return intent, path, fail.New("INVALID_INTENT", "plan intent identity is invalid")
	}
	digest, err := digestIntent(intent)
	if err != nil {
		return intent, path, err
	}
	if digest != intent.PlanDigest || strings.TrimSpace(expectedDigest) != "" && digest != strings.TrimSpace(expectedDigest) {
		return intent, path, fail.New("INTENT_DIGEST_MISMATCH", "plan intent digest does not match immutable content")
	}
	if intent.State != "prepared" && intent.State != "applied" && intent.State != "complete" {
		return intent, path, fail.New("INVALID_INTENT", "plan intent state is invalid")
	}
	if err := validateIntentRecord(repo, intent); err != nil {
		return intent, path, err
	}
	return intent, path, nil
}

func validateIntentRecord(repo gitx.Repo, intent Intent) error {
	if !intentDigestPattern.MatchString(intent.PlanDigest) || !intentDigestPattern.MatchString(intent.SourceIndexDigest) || !intentDigestPattern.MatchString(intent.HookDigest) {
		return fail.New("INVALID_INTENT", "plan intent digest fields are invalid")
	}
	if !intentObjectPattern.MatchString(intent.ExpectedOld) || !intentObjectPattern.MatchString(intent.ExpectedNew) || !intentObjectPattern.MatchString(intent.SourceHead) || !intentObjectPattern.MatchString(intent.FinalTree) || intent.ExpectedOld == intent.ExpectedNew {
		return fail.New("INVALID_INTENT", "plan intent object identities are invalid")
	}
	if !validIntentTargetRef(intent.TargetRef) {
		return fail.New("INVALID_INTENT", "plan intent target ref is invalid")
	}
	if intent.CreatedAt.IsZero() || intent.UpdatedAt.IsZero() || intent.UpdatedAt.Before(intent.CreatedAt) {
		return fail.New("INVALID_INTENT", "plan intent timestamps are invalid")
	}
	wantedID := canonicalIntentID(intent.TargetRef, intent.ExpectedOld, intent.ExpectedNew, intent.SourceIndexDigest)
	if intent.PlanID != wantedID {
		return fail.New("INVALID_INTENT", "plan intent id does not match its immutable publication identity")
	}
	if err := validateIntentPaths(repo, intent.RequestedPaths, "requested"); err != nil {
		return err
	}
	if err := validateIntentPaths(repo, intent.AllowedPaths, "allowed"); err != nil {
		return err
	}
	for _, requested := range intent.RequestedPaths {
		if !pathid.Covered(requested, intent.AllowedPaths) {
			return fail.New("INVALID_INTENT", "plan intent requested paths exceed its allowed paths")
		}
	}
	if len(intent.Commits) == 0 || len(intent.Commits) > maxCommitGroups {
		return fail.New("INVALID_INTENT", "plan intent commit count is outside the safety bounds")
	}
	cursor := intent.ExpectedOld
	seenPaths := map[string]bool{}
	for index, planned := range intent.Commits {
		if planned.Index != index+1 || planned.Parent != cursor || !intentObjectPattern.MatchString(planned.Parent) || !intentObjectPattern.MatchString(planned.Commit) || !intentObjectPattern.MatchString(planned.Tree) {
			return fail.Errorf("INVALID_INTENT", "plan intent commit %d identity or parent chain is invalid", index+1)
		}
		if err := ValidateMessage(planned.Message, true); err != nil {
			return fail.Wrap("INVALID_INTENT", fmt.Errorf("commit %d message: %w", index+1, err))
		}
		if planned.VerifyCount < 0 || planned.VerifyCount > maxVerifyCommands || planned.Repairs < 0 {
			return fail.Errorf("INVALID_INTENT", "plan intent commit %d counters are invalid", index+1)
		}
		if err := validateIntentPaths(repo, planned.ChangedPaths, fmt.Sprintf("commit %d changed", index+1)); err != nil {
			return err
		}
		for _, path := range planned.ChangedPaths {
			key := pathid.Key(path)
			if seenPaths[key] || !pathid.Covered(path, intent.RequestedPaths) || !pathid.Covered(path, intent.AllowedPaths) {
				return fail.Errorf("INVALID_INTENT", "plan intent commit %d path set is duplicated or out of scope", index+1)
			}
			seenPaths[key] = true
		}
		cursor = planned.Commit
	}
	last := intent.Commits[len(intent.Commits)-1]
	if cursor != intent.ExpectedNew || last.Tree != intent.FinalTree {
		return fail.New("INVALID_INTENT", "plan intent result does not match its final commit")
	}
	return nil
}

func validateIntentPaths(repo gitx.Repo, paths []string, label string) error {
	if len(paths) == 0 || len(paths) > maxPlannedPaths {
		return fail.New("INVALID_INTENT", "plan intent "+label+" path count is outside the safety bounds")
	}
	canonical, err := repo.NormalizePaths(paths)
	if err != nil || !sameOrderedStrings(canonical, paths) {
		return fail.New("INVALID_INTENT", "plan intent "+label+" paths are not canonical")
	}
	return nil
}

func validIntentTargetRef(ref string) bool {
	remainder := strings.TrimPrefix(ref, "refs/heads/wip/")
	if remainder == ref {
		return false
	}
	parts := strings.Split(remainder, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if !intentIdentifierPattern.MatchString(part) || strings.Contains(part, "..") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func canonicalIntentID(targetRef, expectedOld, expectedNew, sourceIndexDigest string) string {
	entropy := strings.Join([]string{targetRef, expectedOld, expectedNew, sourceIndexDigest}, "\x00")
	sum := sha256.Sum256([]byte(entropy))
	return "plan-" + hex.EncodeToString(sum[:12])
}

func sameOrderedStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func MarkIntent(repo gitx.Repo, planID, expectedDigest, state string) (Intent, error) {
	intent, path, err := LoadIntent(repo, planID, expectedDigest)
	if err != nil {
		return Intent{}, err
	}
	valid := intent.State == state || intent.State == "prepared" && state == "applied" || intent.State == "applied" && state == "complete"
	if !valid {
		return Intent{}, fail.Errorf("INVALID_INTENT_STATE", "cannot move plan intent from %s to %s", intent.State, state)
	}
	intent.State, intent.UpdatedAt = state, time.Now().UTC()
	body, err := marshalIntentRecord(intent)
	if err != nil {
		return Intent{}, err
	}
	if err := atomicfile.WriteWithTempDir(path, filepath.Join(repo.CommonDir, "wip", "v1", "locks"), body, 0o600); err != nil {
		return Intent{}, fail.Wrap("INTENT_WRITE_FAILED", err)
	}
	return intent, nil
}

func marshalIntentRecord(intent Intent) ([]byte, error) {
	return recordjson.Marshal(intent, maxIntentBytes, "INTENT_WRITE_FAILED", "plan intent")
}

func validateIntentRecordCapacity(intent Intent) error {
	maximum := intent
	maximum.State = "complete"
	maximum.CreatedAt = maximumRecordTimestamp()
	maximum.UpdatedAt = maximum.CreatedAt
	_, err := marshalIntentRecord(maximum)
	return err
}

func maximumRecordTimestamp() time.Time {
	return time.Date(2000, time.December, 31, 23, 59, 59, 123456789, time.UTC)
}

// ValidateApplied verifies every immutable receipt field against Git objects.
// Callers must hold the lane lock while they validate and update lane metadata.
func ValidateApplied(ctx context.Context, repo gitx.Repo, planID, planDigest, targetRef, laneCursor string) (Intent, bool, error) {
	intent, _, err := LoadIntent(repo, planID, planDigest)
	if err != nil {
		return Intent{}, false, err
	}
	if intent.TargetRef != targetRef {
		return Intent{}, false, fail.New("CAPTURE_RECEIPT_MISMATCH", "plan intent targets a different lane ref")
	}
	if intent.ExpectedOld == "" || intent.ExpectedNew == "" || len(intent.Commits) == 0 {
		return Intent{}, false, fail.New("CAPTURE_RECEIPT_MISMATCH", "plan intent has an invalid or empty commit chain")
	}
	if laneCursor != intent.ExpectedOld && laneCursor != intent.ExpectedNew {
		return Intent{}, false, fail.New("CAPTURE_RECEIPT_MISMATCH", "lane cursor does not match the plan old or new commit")
	}
	actual, err := repo.Text(ctx, nil, "rev-parse", "--verify", targetRef+"^{commit}")
	if err != nil {
		return Intent{}, false, fail.Wrap("REF_NOT_FOUND", err)
	}
	if actual == intent.ExpectedOld {
		if intent.State == "prepared" && laneCursor == intent.ExpectedOld {
			return Intent{}, false, fail.New("PLAN_NOT_APPLIED", "plan ref update did not apply; rerun the plan from current source state")
		}
		return Intent{}, false, fail.New("REF_MOVED", "lane ref moved back to the plan starting commit")
	}
	if actual != intent.ExpectedNew {
		return Intent{}, false, fail.Errorf("REF_MOVED", "lane ref is %s, not plan result %s", actual, intent.ExpectedNew)
	}
	if laneCursor == intent.ExpectedNew && intent.State == "complete" {
		return intent, true, nil
	}
	cursor := intent.ExpectedOld
	for index, planned := range intent.Commits {
		if planned.Commit == "" || planned.Parent != cursor {
			return Intent{}, false, receiptMismatch(index, "has an invalid parent chain")
		}
		parent, parentErr := repo.Text(ctx, nil, "rev-parse", "--verify", planned.Commit+"^1")
		if parentErr != nil || parent != cursor {
			return Intent{}, false, receiptMismatch(index, "does not continue its receipt parent")
		}
		tree, treeErr := repo.Text(ctx, nil, "show", "-s", "--format=%T", planned.Commit)
		if treeErr != nil || tree != planned.Tree {
			return Intent{}, false, receiptMismatch(index, "tree does not match its receipt")
		}
		message, messageErr := repo.Text(ctx, nil, "show", "-s", "--format=%B", planned.Commit)
		if messageErr != nil || strings.TrimSpace(message) != strings.TrimSpace(planned.Message) {
			return Intent{}, false, receiptMismatch(index, "message does not match its receipt")
		}
		changed, changedErr := repo.NULPaths(ctx, nil, "diff", "--no-renames", "--name-only", "-z", cursor, planned.Commit)
		if changedErr != nil || !samePathSet(changed, planned.ChangedPaths) {
			return Intent{}, false, receiptMismatch(index, "path set does not match its receipt")
		}
		for _, path := range changed {
			if !pathid.Covered(path, intent.RequestedPaths) || !pathid.Covered(path, intent.AllowedPaths) {
				return Intent{}, false, receiptMismatch(index, fmt.Sprintf("contains out-of-scope path %q", path))
			}
		}
		cursor = planned.Commit
	}
	if cursor != intent.ExpectedNew {
		return Intent{}, false, fail.New("CAPTURE_RECEIPT_MISMATCH", "plan receipt does not end at the applied ref")
	}
	if intent.FinalTree == "" || intent.FinalTree != intent.Commits[len(intent.Commits)-1].Tree {
		return Intent{}, false, fail.New("CAPTURE_RECEIPT_MISMATCH", "plan final tree does not match its last commit")
	}
	finalTree, err := repo.Text(ctx, nil, "show", "-s", "--format=%T", intent.ExpectedNew)
	if err != nil || finalTree != intent.FinalTree {
		return Intent{}, false, fail.New("CAPTURE_RECEIPT_MISMATCH", "applied commit does not contain the receipt final tree")
	}
	return intent, false, nil
}

func intentPath(repo gitx.Repo, planID string) (string, error) {
	if !planIDPattern.MatchString(planID) {
		return "", fail.New("INVALID_PLAN_ID", "plan id must be plan- followed by 24 lowercase hexadecimal characters")
	}
	return filepath.Join(repo.CommonDir, "wip", "v1", "intents", planID+".json"), nil
}

func receiptMismatch(index int, detail string) error {
	return fail.Errorf("CAPTURE_RECEIPT_MISMATCH", "plan commit %d %s", index+1, detail)
}

func samePathSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
