package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ConstantinChirila/worktree-kill/internal/wt"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// fixture creates a repo with a clean and a dirty worktree and returns them.
func fixture(t *testing.T) (clean, dirty wt.Worktree) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(root, "app")
	os.MkdirAll(repo, 0o755)
	git(t, repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "init")
	git(t, repo, "worktree", "add", "-q", "-b", "clean-branch", filepath.Join(root, "a-clean"))
	git(t, repo, "worktree", "add", "-q", "-b", "dirty-branch", filepath.Join(root, "b-dirty"))
	os.WriteFile(filepath.Join(root, "b-dirty", "README"), []byte("edited\n"), 0o644)

	var mu sync.Mutex
	found := map[string]wt.Worktree{}
	wt.NewScanner(wt.ScanOptions{Roots: []string{root}, Home: root}, func(w wt.Worktree) {
		mu.Lock()
		defer mu.Unlock()
		found[filepath.Base(w.Path)] = w
	}).Run(context.Background())
	return found["a-clean"], found["b-dirty"]
}

// drive feeds a message to the model and synchronously runs every command
// it produces, feeding results back in, the way the Bubble Tea runtime would.
func drive(m *Model, msg tea.Msg) {
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if batch, ok := next.(tea.BatchMsg); ok {
			for _, cmd := range batch {
				if cmd != nil {
					queue = append(queue, cmd())
				}
			}

			continue
		}

		_, cmd := m.Update(next)
		if cmd != nil {
			queue = append(queue, cmd())
		}
	}
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}

	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// screen is the rendered view without styling escape codes.
func screen(m *Model) string { return ansi.Strip(m.View().Content) }

func newModel(t *testing.T, worktrees ...wt.Worktree) *Model {
	t.Helper()
	m := New(context.Background(), Options{Title: "test", Cwd: "/", Visited: func() int64 { return 0 }})
	drive(m, tea.WindowSizeMsg{Width: 160, Height: 20})
	m.sort = sortRepo
	for _, w := range worktrees {
		drive(m, FoundMsg{Worktree: w})
	}

	drive(m, ScanDoneMsg{})
	return m
}

func TestListShowsStatsAndStatus(t *testing.T) {
	clean, dirty := fixture(t)
	m := newModel(t, clean, dirty)
	view := screen(m)

	for _, want := range []string{"clean-branch", "dirty-branch", "clean", "1 changed", "2 worktrees", "scanned test"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestDeleteRequiresConfirmation(t *testing.T) {
	clean, dirty := fixture(t)
	m := newModel(t, clean, dirty)

	drive(m, key("enter"))
	if !strings.Contains(screen(m), "Delete "+Tildify(clean.Path)) {
		t.Fatalf("no confirmation prompt:\n%s", screen(m))
	}

	drive(m, key("n"))
	if _, err := os.Stat(clean.Path); err != nil {
		t.Fatal("declining the prompt still deleted the worktree")
	}

	drive(m, key("enter"))
	drive(m, key("y"))
	if _, err := os.Stat(clean.Path); !os.IsNotExist(err) {
		t.Fatal("worktree not deleted after confirming")
	}

	if s := m.Summary(); s.Removed != 1 || s.Freed == 0 {
		t.Errorf("summary %+v", s)
	}

	if !strings.Contains(screen(m), "deleted") {
		t.Errorf("row not marked deleted:\n%s", screen(m))
	}
}

func TestDeleteDirtyWorktreeWarnsAndForces(t *testing.T) {
	clean, dirty := fixture(t)
	m := newModel(t, clean, dirty)

	drive(m, key("down"))
	drive(m, key("enter"))
	if view := screen(m); !strings.Contains(view, "uncommitted changes (will be lost)") {
		t.Fatalf("no dirty warning:\n%s", view)
	}

	drive(m, key("y"))
	if _, err := os.Stat(dirty.Path); !os.IsNotExist(err) {
		t.Fatalf("dirty worktree not removed; failures: %v", m.Summary().Failures)
	}
}

func TestSelectAllThenDeleteWithBranches(t *testing.T) {
	clean, dirty := fixture(t)
	m := newModel(t, clean, dirty)

	drive(m, key("a"))
	drive(m, key("b"))
	drive(m, key("enter"))
	if view := screen(m); !strings.Contains(view, "Delete 2 worktrees") || !strings.Contains(view, "and branches") {
		t.Fatalf("unexpected prompt:\n%s", view)
	}

	drive(m, key("y"))
	if s := m.Summary(); s.Removed != 2 || len(s.Failures) != 0 {
		t.Fatalf("summary %+v", s)
	}

	if !strings.Contains(screen(m), "branch deleted") {
		t.Errorf("branch deletion not reported:\n%s", screen(m))
	}
}

func TestFilter(t *testing.T) {
	clean, dirty := fixture(t)
	m := newModel(t, clean, dirty)

	drive(m, key("/"))
	for _, r := range "dirty" {
		drive(m, key(string(r)))
	}

	drive(m, key("enter"))
	if rows := m.visible(); len(rows) != 1 || rows[0].Branch != "dirty-branch" {
		t.Fatalf("filter kept %d rows", len(rows))
	}

	// Delete acts on the filtered cursor row, not the hidden first row.
	drive(m, key("enter"))
	if !strings.Contains(screen(m), "Delete "+Tildify(dirty.Path)) {
		t.Fatalf("wrong delete target:\n%s", screen(m))
	}
}

func TestDeleteWaitsForGitStatus(t *testing.T) {
	clean, _ := fixture(t)
	m := newModel(t)
	// Add the row without running its background commands.
	m.Update(FoundMsg{Worktree: clean})

	drive(m, key("enter"))
	if m.confirming != nil {
		t.Fatal("delete prompt shown before git status loaded")
	}

	if !strings.Contains(screen(m), "still checking git status") {
		t.Errorf("no notice:\n%s", screen(m))
	}
}

func TestPromptCountsSelectionsHiddenByFilter(t *testing.T) {
	clean, dirty := fixture(t)
	m := newModel(t, clean, dirty)

	drive(m, key("space"))
	drive(m, key("down"))
	drive(m, key("space"))
	drive(m, key("/"))
	for _, r := range "dirty" {
		drive(m, key(string(r)))
	}

	drive(m, key("enter"))
	drive(m, key("enter"))
	if view := screen(m); !strings.Contains(view, "Delete 2 worktrees") || !strings.Contains(view, "1 hidden by the filter") {
		t.Fatalf("hidden selection not shown:\n%s", view)
	}
}

func TestPromptWarnsAboutCommitsOnNoBranch(t *testing.T) {
	m := newModel(t)
	w := wt.Worktree{Path: "/tmp/detached", RepoName: "app", Head: "abc1234def"}
	m.Update(FoundMsg{Worktree: w})
	m.Update(usageMsg{path: w.Path})
	m.Update(gitMsg{path: w.Path, info: wt.GitInfo{Unpushed: 2}})

	drive(m, key("enter"))
	if view := screen(m); !strings.Contains(view, "1 with commits on no branch (will be lost)") {
		t.Fatalf("no warning:\n%s", view)
	}
}

func TestSanitize(t *testing.T) {
	if got := Sanitize("a\x1b[2Jb‮c\td"); got != "a?[2Jb?c?d" {
		t.Errorf("Sanitize = %q", got)
	}
}

func TestPromptWarnsAboutIgnoredFiles(t *testing.T) {
	m := newModel(t)
	w := wt.Worktree{Path: "/tmp/with-env", RepoName: "app", Branch: "x"}
	m.Update(FoundMsg{Worktree: w})
	m.Update(usageMsg{path: w.Path})
	m.Update(gitMsg{path: w.Path, info: wt.GitInfo{Ignored: []string{".env", "secrets/", "key.pem"}}})

	drive(m, key("enter"))
	if view := screen(m); !strings.Contains(view, "1 with ignored files (will be lost: .env, secrets/, …)") {
		t.Fatalf("no warning:\n%s", view)
	}
}
