package wt

import (
	"context"
	"os"
	"path/filepath"
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
	if _, err := Remove(context.Background(), w, RemoveOptions{Protect: opts.Protection()}); err == nil {
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

	w := scanAll(t, []string{root}, "")[orphan] // root as home would hide it
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
	if _, err := Remove(ctx, inHome, RemoveOptions{Protect: Protection{Home: home}}); err == nil {
		t.Error("deleted a folder directly in home")
	}

	// Holding a live repo or worktree.
	nested := orphanOf(filepath.Join(home, "dev", "old"))
	write(t, filepath.Join(nested.Path, ".claude", "worktrees", "live", ".git"), "gitdir: /somewhere\n")
	if _, err := Remove(ctx, nested, RemoveOptions{Protect: Protection{Home: home}}); err == nil {
		t.Error("deleted an orphan holding another worktree")
	}

	for _, w := range []Worktree{inHome, nested} {
		if _, err := os.Stat(w.Path); err != nil {
			t.Errorf("%s was deleted", w.Path)
		}
	}

	plain := orphanOf(filepath.Join(home, "dev", "plain"))
	if _, err := Remove(ctx, plain, RemoveOptions{Protect: Protection{Home: home}}); err != nil {
		t.Errorf("plain orphan: %v", err)
	}
}

func TestOrphanSwappedForSymlinkIsNotDeleted(t *testing.T) {
	root := tempRoot(t)
	dir := filepath.Join(root, "wt")
	pointer := "gitdir: " + filepath.Join(root, "gone", ".git", "worktrees", "w") + "\n"
	write(t, filepath.Join(dir, ".git"), pointer)
	w, err := fromDotGitFile(dir)
	if err != nil || w.State != StateOrphaned {
		t.Fatalf("setup: %+v %v", w, err)
	}

	// After the checks, the folder becomes a symlink to one that looks the same.
	target := filepath.Join(root, "target")
	write(t, filepath.Join(target, ".git"), pointer)
	write(t, filepath.Join(target, "keep.txt"), "x")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, dir); err != nil {
		// Windows needs Developer Mode or admin rights for symlinks.
		t.Skip("can't create symlinks here:", err)
	}

	if err := removeOrphan(w, RemoveOptions{}); err == nil {
		t.Fatal("deleted through a symlink")
	}

	if _, err := os.Stat(filepath.Join(target, "keep.txt")); err != nil {
		t.Fatal("symlink target was deleted")
	}
}

func TestOrphanDirectlyInHomeIsNotListed(t *testing.T) {
	root := tempRoot(t)
	home := filepath.Join(root, "home")
	write(t, filepath.Join(home, "Downloads", ".git"), "gitdir: "+filepath.Join(root, "gone", ".git", "worktrees", "w")+"\n")
	if found := scanAll(t, []string{home}, home); len(found) != 0 {
		t.Fatalf("listed %v", keys(found))
	}
}
