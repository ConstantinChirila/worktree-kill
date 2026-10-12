package wt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

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
	run(t, repo, "config", "filter."+driver+".clean", "touch "+marker+"; cat")
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

func TestRepoFilterIsNotRun(t *testing.T) {
	// A dot in the driver name checks that it is cut at the last dot.
	w, marker := trappedWorktree(t, "evil.x")
	if _, err := InspectGit(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repo-configured filter ran during status")
	}

	if _, err := Remove(context.Background(), w, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repo-configured filter ran during removal")
	}
}

func TestGlobalFilterStillRuns(t *testing.T) {
	root := tempRoot(t)
	marker := filepath.Join(root, "global-ran")
	global := filepath.Join(root, "gitconfig")
	write(t, global, "[filter \"mine\"]\n\tclean = touch "+marker+"; cat\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	repo := newRepo(t, root, "app")
	write(t, filepath.Join(repo, ".gitattributes"), "*.txt filter=mine\n")
	write(t, filepath.Join(repo, "a.txt"), "hi\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-qm", "files")
	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path)
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(path, "a.txt"), later, later); err != nil {
		t.Fatal(err)
	}

	w, _ := fromDotGitFile(path)
	if _, err := InspectGit(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Fatal("filter from global config was switched off")
	}
}

func TestRepoGpgProgramIsNotRun(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	path := filepath.Join(root, "wt")
	run(t, repo, "worktree", "add", "-q", "-b", "wt", path)

	// A commit with a (bogus) signature, so `git log` would verify it.
	tree := gitOut(t, path, "write-tree")
	commit := "tree " + tree + "\nauthor a <a@a> 1 +0000\ncommitter a <a@a> 1 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n -----END PGP SIGNATURE-----\n\nsigned\n"
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir = path
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		defer stdin.Close()
		stdin.Write([]byte(commit))
	}()

	hash, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	run(t, path, "update-ref", "HEAD", string(hash[:len(hash)-1]))

	marker := filepath.Join(root, "gpg-ran")
	gpg := filepath.Join(root, "gpg.sh")
	write(t, gpg, "#!/bin/sh\ntouch "+marker+"\nexit 1\n")
	if err := os.Chmod(gpg, 0o755); err != nil {
		t.Fatal(err)
	}

	run(t, repo, "config", "gpg.program", gpg)
	run(t, repo, "config", "log.showSignature", "true")

	w, _ := fromDotGitFile(path)
	if _, err := InspectGit(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repo-configured gpg.program ran")
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out := run(t, dir, args...)
	return out[:len(out)-1]
}
