package wt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
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
	NewScanner(ScanOptions{Roots: roots, Home: home}, func(w Worktree) {
		mu.Lock()
		defer mu.Unlock()
		if _, dup := found[w.Path]; dup {
			t.Errorf("worktree reported twice: %s", w.Path)
		}

		found[w.Path] = w
	}).Run(context.Background())
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

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out := run(t, dir, args...)
	return out[:len(out)-1]
}

// trappedWorktree returns a worktree whose repo config defines a filter that
// creates marker when run, with a tracked file whose timestamp changed so
// `git status` has to re-read it through the filter.
func trappedWorktree(t *testing.T, driver string) (w Worktree, marker string) {
	t.Helper()
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	marker = filepath.Join(root, "filter-ran")
	write(t, filepath.Join(repo, ".gitattributes"), "*.txt filter="+driver+"\n")
	write(t, filepath.Join(repo, "a.txt"), "hi\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-qm", "files")

	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path)
	run(t, repo, "config", "filter."+driver+".clean", "touch "+filepath.ToSlash(marker)+"; cat")
	run(t, repo, "config", "filter."+driver+".required", "true")

	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(path, "a.txt"), later, later); err != nil {
		t.Fatal(err)
	}

	w, err := fromDotGitFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return w, marker
}
