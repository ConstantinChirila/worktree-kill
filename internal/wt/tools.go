package wt

import (
	"os"
	"path/filepath"
	"strings"
)

// tool is an AI coding tool and the places it keeps worktrees. Adding a tool
// means adding a row here (and to the README).
type tool struct {
	name string
	// homeDirs are relative to the home folder. They are scanned as extra
	// roots, because the main walk skips hidden folders.
	homeDirs []string
	// repoDirs are relative to a repo's root. The walk enters them even
	// though it otherwise stops at repos, to find orphans git has forgotten.
	repoDirs []string
}

var tools = []tool{
	{name: "Claude", homeDirs: []string{".claude-worktrees"}, repoDirs: []string{".claude/worktrees"}},
	{name: "Cursor", homeDirs: []string{".cursor/worktrees"}},
	{name: "Codex", homeDirs: []string{".codex/worktrees"}},
	{name: "Conductor", homeDirs: []string{"conductor"}, repoDirs: []string{".conductor"}},
	{name: "T3", homeDirs: []string{".t3/worktrees"}},
}

// toolDir is an absolute folder where a tool keeps worktrees.
type toolDir struct{ tool, dir string }

func toolDirs(home string) []toolDir {
	var dirs []toolDir
	if home != "" {
		for _, t := range tools {
			for _, d := range t.homeDirs {
				dirs = append(dirs, toolDir{t.name, filepath.Join(home, filepath.FromSlash(d))})
			}
		}
	}

	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		dirs = append(dirs, toolDir{"Codex", filepath.Join(CanonicalPath(codexHome), "worktrees")})
	}

	return dirs
}

// KnownDirs returns the folders where AI coding tools keep worktrees, including
// $CODEX_HOME/worktrees. They are scanned first so results appear immediately.
func KnownDirs(home string) []string {
	var dirs []string
	for _, d := range toolDirs(home) {
		dirs = append(dirs, d.dir)
	}

	return dirs
}

// repoToolDirs are the in-repo worktree folders, in OS path form.
var repoToolDirs = func() []string {
	var dirs []string
	for _, t := range tools {
		for _, d := range t.repoDirs {
			dirs = append(dirs, filepath.FromSlash(d))
		}
	}

	return dirs
}()

// ToolFor guesses which tool created the worktree at path from where it lives,
// returning "git" when no tool matches.
func ToolFor(path, home string) string {
	for _, d := range toolDirs(home) {
		if within(path, d.dir) {
			return d.tool
		}
	}

	p := filepath.ToSlash(path) + "/"
	for _, t := range tools {
		for _, d := range t.repoDirs {
			if strings.Contains(p, "/"+d+"/") {
				return t.name
			}
		}
	}

	return "git"
}
