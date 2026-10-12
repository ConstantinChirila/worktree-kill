// Package wt finds linked git worktrees on disk and inspects or removes them.
//
// Discovery reads git's on-disk metadata directly instead of shelling out, so
// scanning a whole home directory stays fast. Git itself is only invoked for
// status checks and removal.
package wt

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// State describes whether a worktree's directory and git metadata agree.
type State int

const (
	// StateOK: directory and git admin entry both exist.
	StateOK State = iota
	// StateMissing: git still tracks the worktree but its directory is gone.
	StateMissing
	// StateOrphaned: the directory exists but the repo it belonged to is gone.
	StateOrphaned
)

type Worktree struct {
	Path string
	// GitDir is the per-worktree admin dir, usually <repo>/.git/worktrees/<name>.
	GitDir string
	// CommonDir is the repository's shared .git directory. Empty when orphaned.
	CommonDir string
	// RepoPath is the main checkout (or the bare repo dir). Empty when orphaned.
	RepoPath string
	RepoName string
	// Branch is the short branch name, empty when HEAD is detached.
	Branch     string
	Head       string
	Locked     bool
	LockReason string
	State      State
	Tool       string
}

var errNotLinkedWorktree = errors.New("not a linked worktree")

// fromDotGitFile resolves the worktree whose `.git` file lives in dir.
// Submodules also use a `.git` file, but their gitdir has no `commondir`.
func fromDotGitFile(dir string) (Worktree, error) {
	content, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return Worktree{}, err
	}

	target, ok := strings.CutPrefix(strings.TrimSpace(string(content)), "gitdir:")
	if !ok {
		return Worktree{}, errNotLinkedWorktree
	}

	gitDir := strings.TrimSpace(target)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}

	gitDir = filepath.Clean(gitDir)
	if _, err := os.Stat(gitDir); errors.Is(err, os.ErrNotExist) {
		// Only call it orphaned if the path looks like a worktree admin dir.
		if filepath.Base(filepath.Dir(gitDir)) != "worktrees" {
			return Worktree{}, errNotLinkedWorktree
		}

		return Worktree{
			Path:     dir,
			GitDir:   gitDir,
			RepoName: repoNameFromCommonDir(filepath.Dir(filepath.Dir(gitDir))),
			State:    StateOrphaned,
			Tool:     ToolFor(dir),
		}, nil
	}

	return fromGitDir(dir, gitDir)
}

// fromGitDir builds a Worktree from its admin dir. path is the worktree checkout.
func fromGitDir(path, gitDir string) (Worktree, error) {
	commonRel, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return Worktree{}, errNotLinkedWorktree
	}

	commonDir := strings.TrimSpace(string(commonRel))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}

	commonDir = filepath.Clean(commonDir)
	w := Worktree{
		Path:      path,
		GitDir:    gitDir,
		CommonDir: commonDir,
		RepoPath:  repoPathFromCommonDir(commonDir),
		RepoName:  repoNameFromCommonDir(commonDir),
		Tool:      ToolFor(path),
	}

	if head, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
		value := strings.TrimSpace(string(head))
		if ref, ok := strings.CutPrefix(value, "ref: "); ok {
			w.Branch = strings.TrimPrefix(ref, "refs/heads/")
		} else {
			w.Head = value
		}
	}

	if reason, err := os.ReadFile(filepath.Join(gitDir, "locked")); err == nil {
		w.Locked = true
		w.LockReason = strings.TrimSpace(string(reason))
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		w.State = StateMissing
	}

	return w, nil
}

// worktreesOfRepo lists linked worktrees registered in a repo's shared .git dir.
func worktreesOfRepo(commonDir string) []Worktree {
	adminRoot := filepath.Join(commonDir, "worktrees")
	entries, err := os.ReadDir(adminRoot)
	if err != nil {
		return nil
	}

	var result []Worktree
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		gitDir := filepath.Join(adminRoot, entry.Name())
		pointer, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
		if err != nil {
			continue
		}

		// The gitdir file holds the path of the worktree's `.git` file.
		path := filepath.Dir(filepath.Clean(strings.TrimSpace(string(pointer))))
		if w, err := fromGitDir(path, gitDir); err == nil {
			result = append(result, w)
		}
	}

	return result
}

func repoPathFromCommonDir(commonDir string) string {
	if filepath.Base(commonDir) == ".git" {
		return filepath.Dir(commonDir)
	}

	return commonDir
}

func repoNameFromCommonDir(commonDir string) string {
	return strings.TrimSuffix(filepath.Base(repoPathFromCommonDir(commonDir)), ".git")
}

// ToolFor guesses which tool created a worktree from where it lives.
func ToolFor(path string) string {
	p := filepath.ToSlash(path) + "/"
	switch {
	case strings.Contains(p, "/.claude/worktrees/"), strings.Contains(p, "/.claude-worktrees/"):
		return "Claude"
	case strings.Contains(p, "/.cursor/worktrees/"):
		return "Cursor"
	case strings.Contains(p, "/.codex/worktrees/"):
		return "Codex"
	case strings.Contains(p, "/conductor/"), strings.Contains(p, "/.conductor/"):
		return "Conductor"
	case strings.Contains(p, "/.t3/worktrees/"):
		return "T3"
	default:
		return "git"
	}
}
