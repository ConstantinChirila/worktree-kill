package wt

// Tests that a repo's own config can't make the git commands we run start
// other programs, and that git errors keep their details.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepoFsmonitorIsNotRun(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	marker := filepath.Join(root, "ran")
	hook := filepath.Join(root, "hook.sh")
	write(t, hook, "#!/bin/sh\ntouch "+filepath.ToSlash(marker)+"\nexit 1\n")
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
	write(t, global, "[filter \"mine\"]\n\tclean = touch "+filepath.ToSlash(marker)+"; cat\n")
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
	write(t, gpg, "#!/bin/sh\ntouch "+filepath.ToSlash(marker)+"\nexit 1\n")
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

func TestLazyFetchRunsNoRemoteCommand(t *testing.T) {
	// The sandbox and some setups export GIT_SSH_COMMAND, which would hide this.
	t.Setenv("GIT_SSH_COMMAND", "")
	os.Unsetenv("GIT_SSH_COMMAND")

	// Each case is a partial clone whose remote runs a command of the repo's
	// choosing when git fetches a missing object.
	cases := map[string]func(cmd string) [][2]string{
		"core.sshCommand": func(cmd string) [][2]string {
			return [][2]string{{"remote.origin.url", "ssh://example.invalid/repo"}, {"core.sshCommand", cmd}}
		},
		// protocol.<name>.allow in the repo beats a protocol.allow default.
		"ext:: allowed by the repo": func(cmd string) [][2]string {
			return [][2]string{{"remote.origin.url", "ext::" + cmd}, {"protocol.ext.allow", "always"}}
		},
	}

	for name, remote := range cases {
		t.Run(name, func(t *testing.T) {
			root := tempRoot(t)
			repo := newRepo(t, root, "app")
			path := filepath.Join(root, "wt")
			run(t, repo, "worktree", "add", "-q", "-b", "wt", path)

			marker := filepath.Join(root, "remote-ran")
			cmd := filepath.ToSlash(filepath.Join(root, "remote.sh"))
			script(t, filepath.Join(root, "remote.sh"), marker)
			settings := append([][2]string{
				{"core.repositoryformatversion", "1"},
				{"extensions.partialClone", "origin"},
				{"remote.origin.promisor", "true"},
			}, remote(cmd)...)
			for _, kv := range settings {
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
			assertNotRan(t, marker, name)
		})
	}
}

func TestUsersAllowProtocolIsOverridden(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "ext:ssh")
	for _, kv := range gitEnv() {
		if strings.HasPrefix(kv, "GIT_ALLOW_PROTOCOL=") && kv != "GIT_ALLOW_PROTOCOL=" {
			t.Fatalf("inherited %s", kv)
		}
	}
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
	if info.Submodules != 1 {
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

func TestGitErrorsKeepTheExitCode(t *testing.T) {
	root := tempRoot(t)
	_, err := git(context.Background(), "-C", root, "rev-parse", "--verify", "no-such-ref")
	var exitErr *exec.ExitError
	if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("exit code lost: %v", err)
	}

	if strings.Contains(err.Error(), "exit status") {
		t.Errorf("message should be git's own, got %q", err)
	}
}
