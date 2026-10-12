package wt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}

	return string(out)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo creates <root>/<name> with one commit on main.
func newRepo(t *testing.T, root, name string) string {
	t.Helper()
	repo := filepath.Join(root, name)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	run(t, repo, "init", "-q")
	write(t, filepath.Join(repo, "README"), "hi\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-qm", "init")
	return repo
}

func tempRoot(t *testing.T) string {
	t.Helper()
	// Resolve symlinks (macOS /var -> /private/var) so paths compare equal.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return root
}

func scanAll(t *testing.T, roots []string, home string) map[string]Worktree {
	t.Helper()
	var mu sync.Mutex
	found := map[string]Worktree{}
	Scan(context.Background(), ScanOptions{Roots: roots, Home: home, Workers: 4}, func(w Worktree) {
		mu.Lock()
		defer mu.Unlock()
		if _, dup := found[w.Path]; dup {
			t.Errorf("worktree reported twice: %s", w.Path)
		}

		found[w.Path] = w
	})
	return found
}

func keys(m map[string]Worktree) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)
	return out
}

func TestScanFindsWorktreesInsideAndOutsideRepo(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	inside := filepath.Join(repo, ".claude", "worktrees", "feature")
	outside := filepath.Join(root, "elsewhere", "app-fix")
	run(t, repo, "worktree", "add", "-q", "-b", "feature", inside)
	run(t, repo, "worktree", "add", "-q", "-b", "fix", outside)

	found := scanAll(t, []string{root}, root)
	if want := []string{inside, outside}; !slices.Equal(keys(found), want) {
		t.Fatalf("found %v, want %v", keys(found), want)
	}

	w := found[inside]
	if w.Tool != "Claude" || w.Branch != "feature" || w.RepoName != "app" || w.RepoPath != repo || w.State != StateOK {
		t.Errorf("unexpected worktree: %+v", w)
	}
}

func TestScanSkipsHiddenDirsButWalksKnownRoots(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, filepath.Join(root, "outside-scan"), "lib")
	hidden := filepath.Join(root, "home", ".cursor", "worktrees", "lib", "abc")
	run(t, repo, "worktree", "add", "-q", "-b", "agent", hidden)

	home := filepath.Join(root, "home")
	if got := scanAll(t, []string{home}, home); len(got) != 0 {
		t.Fatalf("hidden dir should be skipped by the plain walk, found %v", keys(got))
	}

	found := scanAll(t, []string{filepath.Join(home, ".cursor", "worktrees"), home}, home)
	if w, ok := found[hidden]; !ok || w.Tool != "Cursor" {
		t.Fatalf("known root not scanned: %v", found)
	}
}

func TestScanStates(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	missing := filepath.Join(root, "wt-missing")
	locked := filepath.Join(root, "wt-locked")
	run(t, repo, "worktree", "add", "-q", "-b", "m", missing)
	run(t, repo, "worktree", "add", "-q", "-b", "l", locked)
	run(t, repo, "worktree", "lock", "--reason", "agent running", locked)
	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}

	// A worktree whose repo has been deleted.
	doomed := newRepo(t, root, "doomed")
	orphan := filepath.Join(root, "wt-orphan")
	run(t, doomed, "worktree", "add", "-q", "-b", "o", orphan)
	if err := os.RemoveAll(doomed); err != nil {
		t.Fatal(err)
	}

	// Submodule-style .git file: not a worktree.
	write(t, filepath.Join(root, "submodule", ".git"), "gitdir: ../app/.git/modules/sub\n")

	found := scanAll(t, []string{root}, root)
	if got := keys(found); !slices.Equal(got, []string{locked, missing, orphan}) {
		t.Fatalf("found %v", got)
	}

	if w := found[missing]; w.State != StateMissing {
		t.Errorf("missing: state %v", w.State)
	}

	if w := found[locked]; !w.Locked || w.LockReason != "agent running" {
		t.Errorf("locked: %+v", w)
	}

	if w := found[orphan]; w.State != StateOrphaned || w.RepoName != "doomed" {
		t.Errorf("orphan: %+v", w)
	}
}

func TestInspectGit(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "work", path)
	write(t, filepath.Join(path, "a.txt"), "a")
	run(t, path, "add", ".")
	run(t, path, "commit", "-qm", "local only")
	write(t, filepath.Join(path, "dirty.txt"), "x")

	w, err := fromDotGitFile(path)
	if err != nil {
		t.Fatal(err)
	}

	info, err := InspectGit(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}

	if info.Changes != 1 || info.Unpushed != 1 || info.LastCommit.IsZero() {
		t.Errorf("unexpected info: %+v", info)
	}
}

func TestRemove(t *testing.T) {
	ctx := context.Background()
	root := tempRoot(t)
	repo := newRepo(t, root, "app")

	clean := filepath.Join(root, "clean")
	run(t, repo, "worktree", "add", "-q", "-b", "clean", clean)
	w, _ := fromDotGitFile(clean)
	result, err := Remove(ctx, w, RemoveOptions{DeleteBranch: true})
	if err != nil || !result.BranchDeleted {
		t.Fatalf("clean remove: %+v %v", result, err)
	}

	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Errorf("clean worktree still on disk")
	}

	dirty := filepath.Join(root, "dirty")
	run(t, repo, "worktree", "add", "-q", "-b", "dirty", dirty)
	write(t, filepath.Join(dirty, "README"), "changed\n")
	w, _ = fromDotGitFile(dirty)
	if _, err := Remove(ctx, w, RemoveOptions{}); err == nil {
		t.Fatal("removing a dirty worktree without force should fail")
	}

	if _, err := Remove(ctx, w, RemoveOptions{Force: true}); err != nil {
		t.Fatalf("forced remove: %v", err)
	}

	// Unmerged branch survives `branch -d`.
	unmerged := filepath.Join(root, "unmerged")
	run(t, repo, "worktree", "add", "-q", "-b", "unmerged", unmerged)
	write(t, filepath.Join(unmerged, "b.txt"), "b")
	run(t, unmerged, "add", ".")
	run(t, unmerged, "commit", "-qm", "wip")
	w, _ = fromDotGitFile(unmerged)
	result, err = Remove(ctx, w, RemoveOptions{DeleteBranch: true})
	if err != nil || result.BranchDeleted || result.BranchError == nil {
		t.Fatalf("unmerged: %+v %v", result, err)
	}

	if out := run(t, repo, "branch", "--list", "unmerged"); out == "" {
		t.Error("unmerged branch was deleted")
	}
}

func TestMeasureUsageCountsHardLinksOnce(t *testing.T) {
	root := tempRoot(t)
	big := make([]byte, 64*1024)
	write(t, filepath.Join(root, "a"), string(big))
	before, err := MeasureUsage(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Link(filepath.Join(root, "a"), filepath.Join(root, "b")); err != nil {
		t.Fatal(err)
	}

	after, err := MeasureUsage(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	if before.Bytes < int64(len(big)) || after.Bytes != before.Bytes {
		t.Errorf("before %d, after %d", before.Bytes, after.Bytes)
	}

	if after.LatestChange.IsZero() {
		t.Error("no mtime recorded")
	}
}

func TestToolFor(t *testing.T) {
	cases := map[string]string{
		"/u/.claude/worktrees/x":    "Claude",
		"/u/.claude-worktrees/r/x":  "Claude",
		"/u/.cursor/worktrees/r/x":  "Cursor",
		"/u/.codex/worktrees/abc/r": "Codex",
		"/u/conductor/workspaces/r": "Conductor",
		"/u/proj/.conductor/tokyo":  "Conductor",
		"/u/.t3/worktrees/r/x":      "T3",
		"/u/dev/app-feature":        "git",
	}

	for path, want := range cases {
		if got := ToolFor(path); got != want {
			t.Errorf("ToolFor(%q) = %q, want %q", path, got, want)
		}
	}
}
