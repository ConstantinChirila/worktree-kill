package wt

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// safeConfig switches off repo config that would make the commands this
// package runs start other programs. Repos found by a scan may be untrusted
// (an extracted archive, say), and git reads each repo's own config:
//   - core.fsmonitor runs on every `git status`. An empty value disables it
//     on both old and new git.
//   - core.hooksPath at an empty location skips the repo's hooks, such as
//     reference-transaction during `git branch -d`.
//
// Network access is blocked through gitEnv, filters (which are per-repo)
// through gitFor, and signature checks per command (`log --no-show-signature`).
var safeConfig = []string{
	"-c", "core.fsmonitor=",
	"-c", "core.hooksPath=" + os.DevNull,
}

// gitEnv returns the environment for git: ours, minus variables that point
// git at another repo (set when worktreekill runs from a git hook, say).
func gitEnv() []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_PREFIX", "GIT_ALLOW_PROTOCOL":
			return true
		}

		return false
	})

	// GIT_OPTIONAL_LOCKS=0 keeps read-only commands like `status` from taking
	// the index lock.
	//
	// None of our commands need the network, but a "partial clone" repo
	// fetches missing objects on demand, through its own remote: an ext::
	// URL or core.sshCommand runs whatever the repo says. An empty
	// GIT_ALLOW_PROTOCOL allows no transport at all and, unlike
	// protocol.allow=never, overrides the repo's protocol.<name>.allow.
	// GIT_NO_LAZY_FETCH (git 2.45+) also stops the fetch from being tried.
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_ALLOW_PROTOCOL=", "GIT_NO_LAZY_FETCH=1")
}

// git runs a git command with safeConfig. Commands that read files in a
// worktree go through gitFor instead, which also switches off its filters.
func git(ctx context.Context, args ...string) (string, error) {
	cmd, stderr := gitCommand(ctx, args)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", gitFailure(err, stderr)
	}

	return stdout.String(), nil
}

// gitEach runs git like git does and calls fn with each NUL-terminated record
// of its output (use -z), without holding all of it in memory.
func gitEach(ctx context.Context, args []string, fn func(record []byte)) error {
	cmd, stderr := gitCommand(ctx, args)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, 0); i >= 0 {
			return i + 1, data[:i], nil
		}

		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}

		return 0, nil, nil
	})

	for scanner.Scan() {
		fn(scanner.Bytes())
	}

	// Drain whatever is left so git can exit, even if a record was too long.
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		return gitFailure(err, stderr)
	}

	return scanner.Err()
}

func gitCommand(ctx context.Context, args []string) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.CommandContext(ctx, "git", append(slices.Clone(safeConfig), args...)...)
	cmd.Env = gitEnv()
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	return cmd, stderr
}

// gitFailure turns a failed run into git's own message.
func gitFailure(err error, stderr *bytes.Buffer) error {
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		return err
	}

	// Git prefixes errors with "fatal:" or "error:"; keep the first line only.
	msg, _, _ = strings.Cut(msg, "\n")
	msg = strings.TrimPrefix(strings.TrimPrefix(msg, "fatal: "), "error: ")
	return &gitError{msg: msg, err: err}
}

// gitError is git's own message, wrapping the *exec.ExitError so callers can
// still check the exit code with errors.As.
type gitError struct {
	msg string
	err error
}

func (e *gitError) Error() string { return e.msg }
func (e *gitError) Unwrap() error { return e.err }

// gitFor returns a git runner for a worktree that switches off the filter
// drivers its repo's own config defines (`git status` runs a filter's clean
// command on files whose timestamps changed). Filters from your global or
// system config, like git-lfs, keep working. A repo that sets one up only in
// its own config may show files as changed that aren't, which errs towards
// warning. Needs git 2.26+ for --show-scope.
func gitFor(ctx context.Context, w Worktree) (repoGit, error) {
	out, err := git(ctx, "-C", w.Path, "config", "-z", "--show-scope", "--get-regexp", `^filter\.`)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		// No filters configured anywhere.
		out, err = "", nil
	}

	if err != nil {
		return repoGit{}, fmt.Errorf("reading repo config: %w", err)
	}

	// With -z, entries are "<scope>\0<key>\n<value>\0", so values can't
	// smuggle in fake entries.
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var overrides []string
	seen := map[string]bool{}
	for i := 0; i+1 < len(fields); i += 2 {
		scope := fields[i]
		key, _, _ := strings.Cut(fields[i+1], "\n")
		if scope == "global" || scope == "system" || scope == "command" {
			continue
		}

		// Driver names may contain dots, so cut at the last one.
		driver := strings.TrimPrefix(key, "filter.")
		if i := strings.LastIndex(driver, "."); i > 0 {
			driver = driver[:i]
		}

		if strings.Contains(driver, "=") {
			// `-c` splits at the first "=", so this one can't be overridden.
			return repoGit{}, fmt.Errorf("repo config defines a filter git can't be told to skip: %q", driver)
		}

		if seen[driver] {
			continue
		}

		seen[driver] = true
		for _, setting := range []string{"clean=", "smudge=", "process=", "required=false"} {
			overrides = append(overrides, "-c", "filter."+driver+"."+setting)
		}
	}

	return repoGit{ctx: ctx, overrides: overrides}, nil
}

// repoGit runs git for one worktree with its repo's filters switched off.
// See gitFor.
type repoGit struct {
	ctx       context.Context
	overrides []string
}

func (g repoGit) run(args ...string) (string, error) {
	return git(g.ctx, append(slices.Clone(g.overrides), args...)...)
}

func (g repoGit) each(fn func(record []byte), args ...string) error {
	return gitEach(g.ctx, append(slices.Clone(g.overrides), args...), fn)
}
