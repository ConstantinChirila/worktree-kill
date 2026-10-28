package wt

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

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

	// No home: orphans directly in home are hidden, and these sit in root.
	found := scanAll(t, []string{root}, "")
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

func TestForgedEntryDoesNotHideRealWorktree(t *testing.T) {
	root := tempRoot(t)
	app := newRepo(t, root, "app")
	genuine := filepath.Join(root, "wt")
	run(t, app, "worktree", "add", "-q", "-b", "wt", genuine)

	// Another repo registers entries pointing at app's worktree and at a
	// plain folder.
	evil := newRepo(t, root, "evil")
	precious := filepath.Join(root, "precious")
	write(t, filepath.Join(precious, "keep.txt"), "x")
	for name, target := range map[string]string{"fake": genuine, "plain": precious} {
		admin := filepath.Join(evil, ".git", "worktrees", name)
		write(t, filepath.Join(admin, "gitdir"), filepath.Join(target, ".git")+"\n")
		write(t, filepath.Join(admin, "commondir"), "../..\n")
		write(t, filepath.Join(admin, "HEAD"), "ref: refs/heads/main\n")
	}

	// Scan evil first so its entries would win the path dedupe.
	found := scanAll(t, []string{evil, root}, root)
	if w := found[genuine]; w.RepoName != "app" {
		t.Errorf("real worktree shown as %+v", w)
	}

	if _, ok := found[precious]; ok {
		t.Error("forged entry for a plain folder listed")
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

func TestToolFor(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.FromSlash("/opt/codex"))
	cases := map[string]string{
		"/u/.claude/worktrees/x":            "Claude",
		"/u/proj/.claude/worktrees/x":       "Claude",
		"/u/.claude-worktrees/r/x":          "Claude",
		"/u/.cursor/worktrees/r/x":          "Cursor",
		"/u/.codex/worktrees/abc/r":         "Codex",
		"/opt/codex/worktrees/abc/r":        "Codex",
		"/u/conductor/workspaces/r":         "Conductor",
		"/u/code/conductor/r":               "git",
		"/u/proj/.conductor/tokyo":          "Conductor",
		"/u/.t3/worktrees/r/x":              "T3",
		"/u/dev/app-feature":                "git",
		"/u/.claude-worktrees-old/whatever": "git",
	}

	for path, want := range cases {
		if got := ToolFor(filepath.FromSlash(path), filepath.FromSlash("/u")); got != want {
			t.Errorf("ToolFor(%q) = %q, want %q", path, got, want)
		}
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

func TestRepoAtScanRootIsStillWalked(t *testing.T) {
	root := tempRoot(t)
	// A home folder that is itself a dotfiles repo.
	home := newRepo(t, root, "home")
	repo := newRepo(t, home, "dev/app")
	path := filepath.Join(home, "dev", "app-feature")
	run(t, repo, "worktree", "add", "-q", "-b", "feature", path)

	if _, ok := scanAll(t, []string{home}, home)[path]; !ok {
		t.Fatal("walk stopped at the repo at the scan root")
	}

	// Below the root, repos are still leaves.
	nested := newRepo(t, filepath.Join(root, "outside"), "lib")
	inside := filepath.Join(repo, "vendor", "lib-wt")
	run(t, nested, "worktree", "add", "-q", "-b", "x", inside)
	if _, ok := scanAll(t, []string{filepath.Join(home, "dev")}, home)[inside]; ok {
		t.Error("walked into a repo below the scan root")
	}
}
