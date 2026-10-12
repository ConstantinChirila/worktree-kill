package wt

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestForgedEntryDoesNotHideRealWorktree(t *testing.T) {
	root := tempRoot(t)
	app := newRepo(t, root, "app")
	real := filepath.Join(root, "wt")
	run(t, app, "worktree", "add", "-q", "-b", "wt", real)

	// Another repo registers entries pointing at app's worktree and at a
	// plain folder.
	evil := newRepo(t, root, "evil")
	precious := filepath.Join(root, "precious")
	write(t, filepath.Join(precious, "keep.txt"), "x")
	for name, target := range map[string]string{"fake": real, "plain": precious} {
		admin := filepath.Join(evil, ".git", "worktrees", name)
		write(t, filepath.Join(admin, "gitdir"), filepath.Join(target, ".git")+"\n")
		write(t, filepath.Join(admin, "commondir"), "../..\n")
		write(t, filepath.Join(admin, "HEAD"), "ref: refs/heads/main\n")
	}

	// Scan evil first so its entries would win the path dedupe.
	found := scanAll(t, []string{evil, root}, root)
	if w := found[real]; w.RepoName != "app" {
		t.Errorf("real worktree shown as %+v", w)
	}

	if _, ok := found[precious]; ok {
		t.Error("forged entry for a plain folder listed")
	}
}

func TestPathsCompareIgnoringCaseWhereTheFileSystemDoes(t *testing.T) {
	if !caseInsensitive {
		t.Skip("case-sensitive platform")
	}

	if !within(filepath.FromSlash("/Users/Me/dev/x"), filepath.FromSlash("/users/me")) || !samePath("Me", "me") {
		t.Error("case-insensitive comparison failed")
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

func TestIgnoredFilesThatCantBeRebuiltAreListed(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	write(t, filepath.Join(repo, ".gitignore"), ".env\nnode_modules/\ndist/\n*.log\nsecrets/\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-qm", "ignore")

	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path)
	for _, f := range []string{".env", "node_modules/a/index.js", "dist/app.js", "debug.log", "secrets/key.pem"} {
		write(t, filepath.Join(path, f), "x")
	}

	w, _ := fromDotGitFile(path)
	info, err := InspectGit(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}

	slices.Sort(info.Ignored)
	if want := []string{".env", "secrets/"}; !slices.Equal(info.Ignored, want) || info.Changes != 0 {
		t.Errorf("ignored %v, changes %d; want %v, 0", info.Ignored, info.Changes, want)
	}
}

func TestSkipDirLeavesOutCachesButNotGoSrc(t *testing.T) {
	home := "/u"
	cases := map[[2]string]bool{
		{"/u/go", "pkg"}:       true,
		{"/u/go", "src"}:       false,
		{"/u", "OrbStack"}:     true,
		{"/u", "miniconda3"}:   true,
		{"/u/dev", "pkg"}:      false,
		{"/u/dev", "OrbStack"}: false,
	}

	for c, want := range cases {
		if got := skipDir(filepath.FromSlash(c[0]), c[1], filepath.FromSlash(home)); got != want {
			t.Errorf("skipDir(%q, %q) = %v, want %v", c[0], c[1], got, want)
		}
	}
}

func TestRebuildable(t *testing.T) {
	for path, want := range map[string]bool{
		"node_modules/":        true,
		"packages/a/dist/":     true,
		"app.log":              true,
		".env":                 false,
		"secrets/":             false,
		`"weird name/.venv/"`:  true,
		"config/local.json":    false,
		strings.Repeat("x", 3): false,
	} {
		if got := rebuildable(path); got != want {
			t.Errorf("rebuildable(%q) = %v, want %v", path, got, want)
		}
	}
}
