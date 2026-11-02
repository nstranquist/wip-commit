package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDiscoverAndNormalizePathsFailClosed(t *testing.T) {
	directory := t.TempDir()
	runGit(t, directory, "init", "-b", "main")
	repo, err := Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := repo.NormalizePaths([]string{"src/file.go", "src/file.go"})
	if err != nil || len(paths) != 1 || paths[0] != "src/file.go" {
		t.Fatalf("paths = %#v, %v", paths, err)
	}
	outside := filepath.Join(filepath.Dir(repo.Root), "outside.txt")
	invalid := [][]string{{"."}, {".GIT/config"}, {outside}, {"bad\x00path"}, {string([]byte{0xff})}}
	for _, input := range invalid {
		if _, err := repo.NormalizePaths(input); err == nil {
			t.Errorf("invalid path passed: %q", input)
		}
	}
	if _, err := repo.NormalizePaths([]string{"Docs/CAFÉ.md", "docs/cafe\u0301.md"}); err == nil {
		t.Fatal("portable Unicode aliases passed")
	}
	if _, err := os.Stat(repo.GitDir); err != nil {
		t.Fatal(err)
	}
}

func TestCheckStagedDiffDistinguishesWhitespaceFromGitFailure(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		repo, file := initializedRepo(t)
		if err := os.WriteFile(file, []byte("clean\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo.Root, "add", "tracked.txt")

		passed, err := repo.CheckStagedDiff(context.Background())
		if err != nil || !passed {
			t.Fatalf("CheckStagedDiff() = %v, %v; want true, nil", passed, err)
		}
	})

	t.Run("whitespace", func(t *testing.T) {
		repo, file := initializedRepo(t)
		if err := os.WriteFile(file, []byte("trailing whitespace \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo.Root, "add", "tracked.txt")

		passed, err := repo.CheckStagedDiff(context.Background())
		if err != nil || passed {
			t.Fatalf("CheckStagedDiff() = %v, %v; want false, nil", passed, err)
		}
	})

	t.Run("git failure", func(t *testing.T) {
		repo, _ := initializedRepo(t)
		indexPath := filepath.Join(repo.GitDir, "index")
		if err := os.Remove(indexPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(indexPath, 0o700); err != nil {
			t.Fatal(err)
		}

		passed, err := repo.CheckStagedDiff(context.Background())
		if err == nil || passed {
			t.Fatalf("CheckStagedDiff() = %v, %v; want false and an error", passed, err)
		}
	})
}

func TestDiscoverIgnoresInheritedRepositoryRoutingEnvironment(t *testing.T) {
	first, _ := initializedRepo(t)
	second, _ := initializedRepo(t)
	firstHead := gitText(t, first.Root, "rev-parse", "HEAD")

	t.Setenv("GIT_DIR", second.GitDir)
	t.Setenv("GIT_WORK_TREE", second.Root)
	t.Setenv("GIT_COMMON_DIR", second.CommonDir)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(second.CommonDir, "objects"))
	t.Setenv("GIT_NAMESPACE", "foreign")
	t.Setenv("GIT_GLOB_PATHSPECS", "1")
	t.Setenv("GIT_ICASE_PATHSPECS", "1")
	t.Setenv("GIT_LITERAL_PATHSPECS", "1")
	t.Setenv("GIT_NOGLOB_PATHSPECS", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.worktree")
	t.Setenv("GIT_CONFIG_VALUE_0", second.Root)

	discovered, err := Discover(context.Background(), first.Root)
	if err != nil {
		t.Fatal(err)
	}
	if discovered.Root != first.Root || discovered.CommonDir != first.CommonDir || discovered.GitDir != first.GitDir {
		t.Fatalf("Discover() = %#v, want %#v", discovered, first)
	}
	head, err := discovered.Text(context.Background(), nil, "rev-parse", "HEAD")
	if err != nil || head != firstHead {
		t.Fatalf("repository HEAD = %q, %v; want %q", head, err, firstHead)
	}
	for _, entry := range Environment(nil) {
		if isolatesRepositoryEnvironment(environmentName(entry)) {
			t.Fatalf("isolated variable survived: %s", strings.SplitN(entry, "=", 2)[0])
		}
	}
}

func TestRepoCommandsUseTrustedPrivateIndexWithoutChangingCallerIndex(t *testing.T) {
	repo, _ := initializedRepo(t)
	ctx := context.Background()
	callerBefore, err := repo.Raw(ctx, nil, "ls-files", "-s", "-z")
	if err != nil {
		t.Fatal(err)
	}
	privateIndex := filepath.Join(t.TempDir(), "capture.index")
	environment := []string{
		"GIT_INDEX_FILE=" + privateIndex,
		"GIT_DIR=/tmp/forbidden.git",
		"WIP_TARGET_REF=refs/heads/wip/test/private-index",
	}
	if _, err := repo.Text(ctx, environment, "read-tree", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Text(ctx, environment, "update-index", "--force-remove", "--", "tracked.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(privateIndex); err != nil {
		t.Fatalf("trusted private index was not created: %v", err)
	}
	callerAfter, err := repo.Raw(ctx, nil, "ls-files", "-s", "-z")
	if err != nil {
		t.Fatal(err)
	}
	if callerAfter != callerBefore {
		t.Fatalf("caller index changed through private-index operation:\nwant %q\n got %q", callerBefore, callerAfter)
	}
	privateEntries, err := repo.Raw(ctx, []string{"GIT_INDEX_FILE=" + privateIndex}, "ls-files", "-s", "-z")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(privateEntries, "tracked.txt") {
		t.Fatalf("private index update did not take effect: %q", privateEntries)
	}
	commandEnvironment := mergedEnvironment(environment)
	for _, entry := range commandEnvironment {
		name := environmentName(entry)
		if name == "GIT_DIR" {
			t.Fatalf("untrusted repository override survived: %q", entry)
		}
	}
	if !containsEnvironment(commandEnvironment, "GIT_INDEX_FILE", privateIndex) || !containsEnvironment(commandEnvironment, "WIP_TARGET_REF", "refs/heads/wip/test/private-index") {
		t.Fatalf("trusted command environment was not preserved: %q", commandEnvironment)
	}
}

func containsEnvironment(environment []string, name, value string) bool {
	wanted := name + "=" + value
	for _, entry := range environment {
		if entry == wanted {
			return true
		}
	}
	return false
}

func TestCommandOutputHelpersPreserveExactGitResults(t *testing.T) {
	repo, _ := initializedRepo(t)
	ctx := context.Background()

	raw, err := repo.Raw(ctx, nil, "show", "HEAD:tracked.txt")
	if err != nil || raw != "base\n" {
		t.Fatalf("Raw() = %q, %v; want exact file bytes", raw, err)
	}
	lines, err := repo.Lines(ctx, nil, "for-each-ref", "--format=%(refname)", "refs/heads")
	if err != nil || len(lines) != 1 || lines[0] != "refs/heads/main" {
		t.Fatalf("Lines() = %#v, %v", lines, err)
	}
	empty, err := repo.Lines(ctx, nil, "for-each-ref", "--format=%(refname)", "refs/heads/missing")
	if err != nil || empty != nil {
		t.Fatalf("empty Lines() = %#v, %v; want nil, nil", empty, err)
	}
	if code, err := repo.Exit(ctx, nil, "diff", "--quiet", "HEAD", "--"); err != nil || code != 0 {
		t.Fatalf("clean Exit() = %d, %v; want 0, nil", code, err)
	}

	name := "path with spaces.txt"
	if runtime.GOOS != "windows" {
		name = "path\nwith newline.txt"
	}
	if err := os.WriteFile(filepath.Join(repo.Root, name), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo.Root, "add", "--", name)
	paths, err := repo.NULPaths(ctx, nil, "ls-files", "-z")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, path := range paths {
		if path == name {
			found = true
		}
	}
	if !found {
		t.Fatalf("NULPaths() = %#v; missing %q", paths, name)
	}
	entries, err := repo.IndexEntries(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := repo.Raw(ctx, nil, "ls-files", "-s", "-z", "--", name)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(strings.TrimSuffix(row, "\x00"), "\t", 2)
	if len(parts) != 2 || entries[name] != parts[0] {
		t.Fatalf("IndexEntries()[%q] = %q, want row %q", name, entries[name], row)
	}
	if code, err := repo.Exit(ctx, nil, "diff", "--cached", "--quiet", "HEAD", "--"); err != nil || code != 1 {
		t.Fatalf("dirty Exit() = %d, %v; want 1, nil", code, err)
	}
}

func TestTreeEntriesMatchACleanIndex(t *testing.T) {
	repo, _ := initializedRepo(t)
	ctx := context.Background()
	index, err := repo.IndexEntries(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repo.TreeEntries(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != len(index) {
		t.Fatalf("TreeEntries(HEAD) has %d entries, want %d: %#v", len(tree), len(index), tree)
	}
	for path, want := range index {
		if got := tree[path]; got != want {
			t.Errorf("TreeEntries(HEAD)[%q] = %q, want %q", path, got, want)
		}
	}
}

func initializedRepo(t *testing.T) (Repo, string) {
	t.Helper()
	directory := t.TempDir()
	runGit(t, directory, "init", "-b", "main")
	runGit(t, directory, "config", "user.name", "WIP Test")
	runGit(t, directory, "config", "user.email", "wip-test@example.invalid")
	file := filepath.Join(directory, "tracked.txt")
	if err := os.WriteFile(file, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, directory, "add", "tracked.txt")
	runGit(t, directory, "commit", "-m", "base")
	repo, err := Discover(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	return repo, file
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func gitText(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
