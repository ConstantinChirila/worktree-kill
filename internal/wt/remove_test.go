package wt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

	// git prints forward slashes on Windows too.
	list := filepath.ToSlash(run(t, repo, "worktree", "list"))
	if strings.Contains(list, filepath.ToSlash(filepath.Join(root, "one"))) || !strings.Contains(list, filepath.ToSlash(filepath.Join(root, "two"))) {
		t.Fatalf("wrong entries pruned:\n%s", list)
	}

	if _, err := Remove(ctx, found[filepath.Join(root, "locked")], RemoveOptions{}); err == nil {
		t.Fatal("removed a locked worktree whose folder is missing")
	}
}

func TestStaleEntryRemovalKeepsAFolderThatCameBack(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path)
	w, _ := fromDotGitFile(path)
	w.State = StateMissing

	// The folder is back, with a change, after Remove saw it missing.
	write(t, filepath.Join(path, "README"), "changed\n")
	if err := removeStaleEntry(context.Background(), w); err == nil {
		t.Fatal("removed a folder with changes")
	}

	if content, err := os.ReadFile(filepath.Join(path, "README")); err != nil || string(content) != "changed\n" {
		t.Fatalf("change lost: %q %v", content, err)
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

func TestPopulatedSubmodulesAreCountedAndForced(t *testing.T) {
	ctx := context.Background()
	root := tempRoot(t)
	sub := newRepo(t, root, "sub")
	app := newRepo(t, root, "app")
	run(t, app, "-c", "protocol.file.allow=always", "submodule", "-q", "add", sub, "sub")
	// An embedded repo committed without .gitmodules is a gitlink too.
	embedded := newRepo(t, app, "embedded")
	run(t, app, "add", "embedded")
	run(t, app, "commit", "-qm", "gitlinks")

	inspect := func(path string) (Worktree, GitInfo) {
		t.Helper()
		w, err := fromDotGitFile(path)
		if err != nil {
			t.Fatal(err)
		}

		info, err := InspectGit(ctx, w)
		if err != nil {
			t.Fatal(err)
		}

		return w, info
	}

	// A new worktree's gitlinks are empty, so it deletes like any other.
	fresh := filepath.Join(root, "fresh")
	run(t, app, "worktree", "add", "-q", "-b", "fresh", fresh)
	w, info := inspect(fresh)
	if info.Submodules != 0 || info.NeedsForce() {
		t.Fatalf("fresh worktree: %+v", info)
	}

	if _, err := Remove(ctx, w, RemoveOptions{}); err != nil {
		t.Fatalf("fresh remove: %v", err)
	}

	populated := filepath.Join(root, "populated")
	run(t, app, "worktree", "add", "-q", "-b", "populated", populated)
	run(t, populated, "-c", "protocol.file.allow=always", "submodule", "-q", "update", "--init", "--", "sub")
	run(t, embedded, "worktree", "add", "-q", "--detach", filepath.Join(populated, "embedded"))
	w, info = inspect(populated)
	if info.Submodules != 2 || info.Changes != 0 || !info.NeedsForce() {
		t.Fatalf("populated worktree: %+v", info)
	}

	if _, err := Remove(ctx, w, RemoveOptions{}); err == nil {
		t.Fatal("git should refuse without --force")
	}

	if _, err := Remove(ctx, w, RemoveOptions{Force: info.NeedsForce()}); err != nil {
		t.Fatalf("forced remove: %v", err)
	}
}
