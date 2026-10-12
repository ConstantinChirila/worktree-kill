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
	"runtime"
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

// String returns the state's identifier, as used in JSON output.
func (s State) String() string {
	switch s {
	case StateMissing:
		return "missing"
	case StateOrphaned:
		return "orphaned"
	default:
		return "ok"
	}
}

// Worktree is a linked worktree, found on disk or in a repo's metadata.
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
	Branch string
	// Head is the commit hash when HEAD is detached.
	Head   string
	Locked bool
	// LockReason is restored if a locked worktree fails to be removed.
	LockReason string
	State      State
	// Tool guesses which tool made the worktree. Set by the scanner.
	Tool string
}

var errNotLinkedWorktree = errors.New("not a linked worktree")

// fromDotGitFile resolves the worktree whose `.git` file lives in dir.
// Submodules also use a `.git` file, but their gitdir has no `commondir`.
func fromDotGitFile(dir string) (Worktree, error) {
	content, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return Worktree{}, err
	}

	gitDir, ok := parseDotGitFile(content, dir)
	if !ok {
		return Worktree{}, errNotLinkedWorktree
	}

	if _, err := os.Stat(gitDir); errors.Is(err, os.ErrNotExist) {
		if !repoDeleted(dir, gitDir) {
			return Worktree{}, errNotLinkedWorktree
		}

		return Worktree{
			Path:     dir,
			GitDir:   gitDir,
			RepoName: repoNameFromCommonDir(filepath.Dir(filepath.Dir(gitDir))),
			State:    StateOrphaned,
		}, nil
	}

	return fromGitDir(dir, gitDir)
}

// parseDotGitFile returns the admin dir named by the `.git` file of the
// worktree in dir ("gitdir: <path>", relative paths are relative to dir).
func parseDotGitFile(content []byte, dir string) (string, bool) {
	target, ok := strings.CutPrefix(strings.TrimSpace(string(content)), "gitdir:")
	if !ok {
		return "", false
	}

	gitDir := strings.TrimSpace(target)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}

	return filepath.Clean(gitDir), true
}

// repoDeleted reports whether the missing gitDir of the worktree in dir
// belonged to a repository that was deleted, as opposed to one that is only
// out of reach. Orphans are deleted outright, so this errs towards "no":
//   - gitDir must look like a worktree admin dir (<repo>/.git/worktrees/<name>);
//   - the repo must have lived outside dir (a stray .git file pointing inside
//     its own folder is not a worktree);
//   - the repo's .git dir must be gone (if it exists, only git's entry for
//     this worktree was removed and `git worktree repair` can relink it);
//   - the folder the repo lived in must still exist. If that is gone too, the
//     repo is more likely on an unmounted drive or inside a container.
func repoDeleted(dir, gitDir string) bool {
	adminRoot := filepath.Dir(gitDir)
	if filepath.Base(adminRoot) != "worktrees" {
		return false
	}

	commonDir := filepath.Dir(adminRoot)
	if within(repoPathFromCommonDir(commonDir), dir) {
		return false
	}

	if _, err := os.Lstat(commonDir); !errors.Is(err, os.ErrNotExist) {
		return false
	}

	info, err := os.Stat(filepath.Dir(repoPathFromCommonDir(commonDir)))
	return err == nil && info.IsDir()
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
	}

	if head, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
		if ref, ok := branchRef(head); ok {
			w.Branch = strings.TrimPrefix(ref, "refs/heads/")
		} else if value := strings.TrimSpace(string(head)); !strings.HasPrefix(value, "ref:") {
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

// branchRef returns the branch a HEAD file points at, like "refs/heads/main".
// It reports false for a detached HEAD or a ref outside refs/heads/, so a
// crafted HEAD can't smuggle git options into later commands.
func branchRef(head []byte) (string, bool) {
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: ")
	if !ok || !strings.HasPrefix(ref, "refs/heads/") {
		return "", false
	}

	return ref, true
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

		// The gitdir file holds the path of the worktree's `.git` file. With
		// worktree.useRelativePaths (git 2.48+) it is relative to gitDir.
		dotGit := strings.TrimSpace(string(pointer))
		if !filepath.IsAbs(dotGit) {
			dotGit = filepath.Join(gitDir, dotGit)
		}

		path := filepath.Dir(filepath.Clean(dotGit))
		if !pointsBack(path, gitDir) {
			continue
		}

		if w, err := fromGitDir(path, gitDir); err == nil {
			result = append(result, w)
		}
	}

	return result
}

// pointsBack reports whether the worktree folder at path is really the one
// registered at gitDir: its `.git` file must name gitDir. Otherwise the entry
// is forged or stale (git would refuse to remove it), and listing it could
// hide the real worktree at that path. A missing folder can't be checked and
// is reported as missing.
func pointsBack(path, gitDir string) bool {
	content, err := os.ReadFile(filepath.Join(path, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		_, statErr := os.Lstat(path)
		return errors.Is(statErr, os.ErrNotExist)
	}

	if err != nil {
		return false
	}

	named, ok := parseDotGitFile(content, path)
	return ok && samePath(CanonicalPath(named), CanonicalPath(gitDir))
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

// CanonicalPath resolves symlinks in path, falling back to cleaning it when
// it can't be resolved. Scanned paths are canonical, so compare against these.
func CanonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	return filepath.Clean(path)
}

// caseInsensitive is set where the default file system ignores case, so
// "C:\Users" and "c:\users" (or "/Users" and "/users" on macOS) are the same
// folder. On a case-sensitive volume this only makes the checks that use it
// stricter.
var caseInsensitive = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// samePath reports whether two clean paths name the same folder.
func samePath(a, b string) bool {
	if caseInsensitive {
		return strings.EqualFold(a, b)
	}

	return a == b
}

// within reports whether path is dir or inside it. Both must be clean.
func within(path, dir string) bool {
	prefix := strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator)
	return samePath(path, dir) || (len(path) >= len(prefix) && samePath(path[:len(prefix)], prefix))
}
