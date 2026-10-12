package wt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// script writes an executable shell script that creates marker when run.
func script(t *testing.T, path, marker string) {
	t.Helper()
	write(t, path, "#!/bin/sh\ntouch "+filepath.ToSlash(marker)+"\nexit 1\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertNotRan(t *testing.T, marker, what string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("%s ran", what)
	}
}

func TestLazyFetchDoesNotRunSSHCommand(t *testing.T) {
	// The sandbox and some setups export GIT_SSH_COMMAND, which would hide this.
	t.Setenv("GIT_SSH_COMMAND", "")
	os.Unsetenv("GIT_SSH_COMMAND")

	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path)

	marker := filepath.Join(root, "ssh-ran")
	script(t, filepath.Join(root, "ssh.sh"), marker)
	for _, kv := range [][2]string{
		{"core.repositoryformatversion", "1"},
		{"extensions.partialClone", "origin"},
		{"remote.origin.promisor", "true"},
		{"remote.origin.url", "ssh://example.invalid/repo"},
		{"core.sshCommand", filepath.Join(root, "ssh.sh")},
	} {
		run(t, repo, "config", kv[0], kv[1])
	}

	// Delete HEAD's tree object and the index, so status must fetch it.
	tree := gitOut(t, path, "rev-parse", "HEAD^{tree}")
	if err := os.Remove(filepath.Join(repo, ".git", "objects", tree[:2], tree[2:])); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(repo, ".git", "worktrees", "wt", "index")); err != nil {
		t.Fatal(err)
	}

	w, _ := fromDotGitFile(path)
	_, _ = InspectGit(context.Background(), w)
	assertNotRan(t, marker, "repo-configured core.sshCommand")
}

func TestSubmoduleFilterIsNotRun(t *testing.T) {
	root := tempRoot(t)
	sub := newRepo(t, root, "sub")
	write(t, filepath.Join(sub, ".gitattributes"), "* filter=evil\n")
	run(t, sub, "add", ".")
	run(t, sub, "commit", "-qm", "attrs")

	app := newRepo(t, root, "app")
	run(t, app, "-c", "protocol.file.allow=always", "submodule", "-q", "add", sub, "sub")
	run(t, app, "commit", "-qm", "add sub")

	marker := filepath.Join(root, "filter-ran")
	run(t, filepath.Join(app, "sub"), "config", "filter.evil.clean", "touch "+filepath.ToSlash(marker)+"; cat")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(app, "sub", "README"), later, later); err != nil {
		t.Fatal(err)
	}

	// Inspect the main checkout as if it were a worktree.
	w := Worktree{Path: app, CommonDir: filepath.Join(app, ".git"), Branch: "main"}
	info, err := InspectGit(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}

	assertNotRan(t, marker, "submodule filter")
	if !info.Submodules {
		t.Error("submodules not detected")
	}
}

func TestRepoHooksDoNotRunOnBranchDelete(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path)

	marker := filepath.Join(root, "hook-ran")
	script(t, filepath.Join(repo, ".git", "hooks", "reference-transaction"), marker)

	w, _ := fromDotGitFile(path)
	result, err := Remove(context.Background(), w, RemoveOptions{DeleteBranch: true})
	if err != nil || !result.BranchDeleted {
		t.Fatalf("remove: %+v %v", result, err)
	}

	assertNotRan(t, marker, "repo hook")
}

func TestStrayDotGitPointingInsideItsFolderIsNotOrphaned(t *testing.T) {
	root := tempRoot(t)
	downloads := filepath.Join(root, "Downloads")
	write(t, filepath.Join(downloads, ".git"), "gitdir: gone/.git/worktrees/w\n")
	if _, err := fromDotGitFile(downloads); err == nil {
		t.Fatal("folder treated as an orphan of a repo inside itself")
	}
}

func TestOrphanRemovalRefusals(t *testing.T) {
	ctx := context.Background()
	root := tempRoot(t)
	home := filepath.Join(root, "home")
	orphanOf := func(dir string) Worktree {
		t.Helper()
		write(t, filepath.Join(dir, ".git"), "gitdir: "+filepath.Join(root, "gone", ".git", "worktrees", "w")+"\n")
		w, err := fromDotGitFile(dir)
		if err != nil || w.State != StateOrphaned {
			t.Fatalf("setup %s: %+v %v", dir, w, err)
		}

		return w
	}

	// Directly in home, like ~/Downloads.
	inHome := orphanOf(filepath.Join(home, "Downloads"))
	if _, err := Remove(ctx, inHome, RemoveOptions{Home: home}); err == nil {
		t.Error("deleted a folder directly in home")
	}

	// Holding a live repo or worktree.
	nested := orphanOf(filepath.Join(home, "dev", "old"))
	write(t, filepath.Join(nested.Path, ".claude", "worktrees", "live", ".git"), "gitdir: /somewhere\n")
	if _, err := Remove(ctx, nested, RemoveOptions{Home: home}); err == nil {
		t.Error("deleted an orphan holding another worktree")
	}

	for _, w := range []Worktree{inHome, nested} {
		if _, err := os.Stat(w.Path); err != nil {
			t.Errorf("%s was deleted", w.Path)
		}
	}

	plain := orphanOf(filepath.Join(home, "dev", "plain"))
	if _, err := Remove(ctx, plain, RemoveOptions{Home: home}); err != nil {
		t.Errorf("plain orphan: %v", err)
	}
}

func TestLeakedGitDirIsIgnored(t *testing.T) {
	root := tempRoot(t)
	other := newRepo(t, root, "other")
	repo := newRepo(t, root, "app")
	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path)
	write(t, filepath.Join(path, "dirty.txt"), "x")

	// As if worktreekill ran from a hook in another repo.
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	w, _ := fromDotGitFile(path)
	info, err := InspectGit(context.Background(), w)
	if err != nil || info.Changes != 1 {
		t.Fatalf("changes = %d (%v), want 1", info.Changes, err)
	}
}

func TestFilterValueWithNewlinesIsHandled(t *testing.T) {
	w, marker := trappedWorktree(t, "evil")
	// A value that, read line by line, looks like an entry that can't be
	// overridden, which would make status fail.
	run(t, w.RepoPath, "config", "filter.evil.smudge", "cat\nlocal\tfilter.a=b.clean x")
	if _, err := InspectGit(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	assertNotRan(t, marker, "repo-configured filter")
}

func TestSameChangesComparesContentNotCount(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path)
	w, _ := fromDotGitFile(path)
	ctx := context.Background()

	write(t, filepath.Join(path, "a.txt"), "a")
	before, _ := InspectGit(ctx, w)
	if err := os.Rename(filepath.Join(path, "a.txt"), filepath.Join(path, "b.txt")); err != nil {
		t.Fatal(err)
	}

	after, _ := InspectGit(ctx, w)
	if before.Changes != after.Changes || before.SameChanges(after) {
		t.Fatalf("before %d, after %d, same %v", before.Changes, after.Changes, before.SameChanges(after))
	}
}

func TestMeasureUsageCountsNestedTree(t *testing.T) {
	root := tempRoot(t)
	const files = 60
	for i := range files {
		dir := filepath.Join(root, strings.Repeat("d/", i%5))
		write(t, filepath.Join(dir, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), string(make([]byte, 8192)))
	}

	usage, err := MeasureUsage(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	if usage.Bytes < files*8192 {
		t.Errorf("bytes = %d, want at least %d", usage.Bytes, files*8192)
	}
}
