package wt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrphanThatContainsHomeIsNeverListedOrDeleted(t *testing.T) {
	root := tempRoot(t)
	home := filepath.Join(root, "home")
	// A stray .git file in home pointing into a deleted repo.
	write(t, filepath.Join(home, ".git"), "gitdir: "+filepath.Join(root, "gone", ".git", "worktrees", "y")+"\n")
	write(t, filepath.Join(home, "precious.txt"), "keep me")

	if found := scanAll(t, []string{home}, home); len(found) != 0 {
		t.Fatalf("home listed as a worktree: %v", keys(found))
	}

	w, err := fromDotGitFile(home)
	if err != nil || w.State != StateOrphaned {
		t.Fatalf("setup: %+v %v", w, err)
	}

	opts := ScanOptions{Roots: []string{home}, Home: home}
	if _, err := Remove(context.Background(), w, RemoveOptions{Protected: opts.Protected()}); err == nil {
		t.Fatal("removing home should fail")
	}

	if _, err := os.Stat(filepath.Join(home, "precious.txt")); err != nil {
		t.Fatal("home was deleted")
	}
}

func TestUnreachableRepoIsNotOrphaned(t *testing.T) {
	root := tempRoot(t)
	// Like a repo on an unmounted drive, or in a devcontainer.
	dir := filepath.Join(root, "wt")
	write(t, filepath.Join(dir, ".git"), "gitdir: /Volumes/NotMounted/repo/.git/worktrees/wt\n")

	if found := scanAll(t, []string{root}, root); len(found) != 0 {
		t.Fatalf("unreachable repo treated as orphan: %v", keys(found))
	}
}

func TestOrphanIsCheckedAgainBeforeDeleting(t *testing.T) {
	root := tempRoot(t)
	doomed := newRepo(t, root, "doomed")
	orphan := filepath.Join(root, "wt-orphan")
	run(t, doomed, "worktree", "add", "-q", "-b", "o", orphan)
	if err := os.RemoveAll(doomed); err != nil {
		t.Fatal(err)
	}

	w := scanAll(t, []string{root}, root)[orphan]
	if w.State != StateOrphaned {
		t.Fatalf("setup: %+v", w)
	}

	// The repo comes back (say, a drive was remounted) before deletion.
	newRepo(t, root, "doomed")
	if err := os.MkdirAll(w.GitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Remove(context.Background(), w, RemoveOptions{}); err == nil {
		t.Fatal("removed a folder that is no longer orphaned")
	}

	if _, err := os.Stat(orphan); err != nil {
		t.Fatal("folder deleted")
	}
}

func TestOrphanInsideRepoToolFolderIsFound(t *testing.T) {
	root := tempRoot(t)
	app := newRepo(t, root, "app")
	doomed := newRepo(t, root, "doomed")
	orphan := filepath.Join(app, ".claude", "worktrees", "old")
	run(t, doomed, "worktree", "add", "-q", "-b", "old", orphan)
	if err := os.RemoveAll(doomed); err != nil {
		t.Fatal(err)
	}

	w, ok := scanAll(t, []string{root}, root)[orphan]
	if !ok || w.State != StateOrphaned || w.Tool != "Claude" {
		t.Fatalf("orphan in .claude/worktrees not found: %+v", w)
	}
}

func TestRelativeGitdirPointers(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "rel", path)
	// What worktree.useRelativePaths (git 2.48+) writes.
	write(t, filepath.Join(repo, ".git", "worktrees", "wt", "gitdir"), "../../../../wt/.git\n")

	found := worktreesOfRepo(filepath.Join(repo, ".git"))
	if len(found) != 1 || found[0].Path != path || found[0].State != StateOK {
		t.Fatalf("got %+v", found)
	}
}

func TestRemoveMissingDropsOnlyThatEntry(t *testing.T) {
	ctx := context.Background()
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	for _, name := range []string{"one", "two", "locked"} {
		run(t, repo, "worktree", "add", "-q", "-b", name, filepath.Join(root, name))
	}

	run(t, repo, "worktree", "lock", filepath.Join(root, "locked"))
	for _, name := range []string{"one", "two", "locked"} {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}

	found := scanAll(t, []string{root}, root)
	if _, err := Remove(ctx, found[filepath.Join(root, "one")], RemoveOptions{}); err != nil {
		t.Fatal(err)
	}

	list := run(t, repo, "worktree", "list")
	if strings.Contains(list, filepath.Join(root, "one")) || !strings.Contains(list, filepath.Join(root, "two")) {
		t.Fatalf("wrong entries pruned:\n%s", list)
	}

	if _, err := Remove(ctx, found[filepath.Join(root, "locked")], RemoveOptions{}); err == nil {
		t.Fatal("removed a locked worktree whose folder is missing")
	}
}

func TestRemoveLockedKeepsDirtyCheck(t *testing.T) {
	ctx := context.Background()
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	path := filepath.Join(root, "agent")
	run(t, repo, "worktree", "add", "-q", "-b", "agent", path)
	run(t, repo, "worktree", "lock", "--reason", "agent running", path)
	write(t, filepath.Join(path, "README"), "changed\n")

	w, _ := fromDotGitFile(path)
	if _, err := Remove(ctx, w, RemoveOptions{}); err == nil {
		t.Fatal("removed a locked worktree with changes without Force")
	}

	if reason, err := os.ReadFile(filepath.Join(w.GitDir, "locked")); err != nil || strings.TrimSpace(string(reason)) != "agent running" {
		t.Fatalf("lock not restored: %q %v", reason, err)
	}

	if _, err := Remove(ctx, w, RemoveOptions{Force: true}); err != nil {
		t.Fatalf("forced remove: %v", err)
	}
}

func TestForgedHeadIsNotUsedAsBranch(t *testing.T) {
	ctx := context.Background()
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "keep", path)
	write(t, filepath.Join(repo, ".git", "worktrees", "wt", "HEAD"), "ref: --force\n")

	w, _ := fromDotGitFile(path)
	if w.Branch != "" || w.Head != "" {
		t.Fatalf("forged HEAD parsed: %+v", w)
	}

	// Git may refuse the removal itself; either way no branch is touched.
	_, _ = Remove(ctx, w, RemoveOptions{Force: true, DeleteBranch: true})
	if out := run(t, repo, "branch", "--list", "keep"); out == "" {
		t.Error("branch was deleted")
	}
}

func TestUnpushedIsMeasuredAgainstMain(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	// The main checkout sits on a feature branch with a commit main lacks.
	run(t, repo, "switch", "-q", "-c", "feature")
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "feature work")
	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path, "feature")

	w, _ := fromDotGitFile(path)
	info, err := InspectGit(context.Background(), w)
	if err != nil || info.Unpushed != 1 {
		t.Fatalf("unpushed = %d (%v), want 1", info.Unpushed, err)
	}
}

func TestRepoFsmonitorIsNotRun(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	marker := filepath.Join(root, "ran")
	hook := filepath.Join(root, "hook.sh")
	write(t, hook, "#!/bin/sh\ntouch "+marker+"\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path)
	run(t, repo, "config", "core.fsmonitor", hook)

	w, _ := fromDotGitFile(path)
	if _, err := InspectGit(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repo-configured fsmonitor hook ran")
	}
}
