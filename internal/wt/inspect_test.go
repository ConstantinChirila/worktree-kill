package wt

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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

func TestIgnoredFilesThatCantBeRebuiltAreListed(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	write(t, filepath.Join(repo, ".gitignore"), ".env\nnode_modules/\ndist/\n*.log\nsecrets/\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-qm", "ignore")

	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path)
	// logs/ itself isn't ignored, only the .log files in it.
	for _, f := range []string{".env", "node_modules/a/index.js", "dist/app.js", "debug.log", "secrets/key.pem", "logs/a.log"} {
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
