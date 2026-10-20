// Package ui is the interactive worktree list, in the spirit of npkill.
package ui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ConstantinChirila/worktree-kill/internal/limit"
	"github.com/ConstantinChirila/worktree-kill/internal/wt"
)

// Messages sent into the program from the scanner and background work.
type (
	// FoundMsg adds a worktree to the list.
	FoundMsg struct{ Worktree wt.Worktree }
	// ScanDoneMsg reports that the scan has finished.
	ScanDoneMsg struct{}
	usageMsg    struct {
		path  string
		usage wt.Usage
		err   error
	}
	gitMsg struct {
		path string
		info wt.GitInfo
		err  error
	}
	removedMsg struct {
		path   string
		result wt.RemoveResult
		err    error
	}
	tickMsg struct{}
)

type phase int

const (
	phaseIdle phase = iota
	// phaseQueued: waiting for, or running, its turn to be removed.
	phaseQueued
	phaseDeleted
	phaseFailed
)

type row struct {
	wt.Worktree
	usage  *wt.Usage
	git    *wt.GitInfo
	gitErr error
	phase  phase
	note   string
}

// active reports whether the row can still be acted on.
func (r *row) active() bool { return r.phase == phaseIdle || r.phase == phaseFailed }

// whyNotDeletable explains why an active row can't be deleted, or returns "".
// Deletion waits for git status so the prompt can warn about everything that
// would be lost.
func (r *row) whyNotDeletable() string {
	switch {
	case r.git == nil:
		return "still checking git status"
	case r.gitErr != nil:
		return "git status failed, so its changes can't be checked"
	case r.State == wt.StateMissing && r.Locked:
		return "locked and its folder is missing, maybe on an unmounted drive: run git worktree unlock first"
	}

	return ""
}

func (r *row) deletable() bool { return r.active() && r.whyNotDeletable() == "" }

func (r *row) lastUsed() time.Time { return LastUsed(r.usage, r.git) }

func (r *row) bytes() int64 {
	if r.usage == nil {
		return 0
	}

	return r.usage.Bytes
}

type sortKey int

const (
	sortSize sortKey = iota
	sortAge
	sortRepo
)

var sortLabels = []string{"largest", "oldest", "repo"}

// Summary is returned when the program exits.
type Summary struct {
	Removed  int
	Freed    int64
	Failures []string
}

// Options configures a Model.
type Options struct {
	// Title names what is being scanned, like "~".
	Title string
	// Cwd marks the worktree you are in. Empty if unknown.
	Cwd string
	// Protect is passed to wt.Remove.
	Protect wt.Protection
	// Visited reports how many folders the scan has read.
	Visited func() int64
}

// Model is the Bubble Tea model for the worktree list.
type Model struct {
	// ctx is kept because Bubble Tea commands have no other way to get one.
	ctx      context.Context
	opts     Options
	started  time.Time
	scanTook time.Duration
	scanning bool

	rows     []*row
	byPath   map[string]*row
	cursor   string
	selected map[string]bool

	sort         sortKey
	filter       string
	filtering    bool
	deleteBranch bool
	confirming   []*row
	notice       string
	quitting     bool
	frame        int

	width, height int
	summary       Summary

	// Limits for background work.
	usageLimit   limit.Limit
	gitLimit     limit.Limit
	removalLimit limit.Limit
	// One removal per repo at a time: concurrent `git worktree` calls on the
	// same repo contend for its locks.
	repoLocksMu sync.Mutex
	repoLocks   map[string]*sync.Mutex
}

// New returns a Model that fills in as FoundMsg messages arrive.
func New(ctx context.Context, opts Options) *Model {
	return &Model{
		ctx:          ctx,
		opts:         opts,
		started:      time.Now(),
		scanning:     true,
		byPath:       map[string]*row{},
		selected:     map[string]bool{},
		width:        100,
		height:       30,
		usageLimit:   limit.New(wt.MeasureConcurrency),
		gitLimit:     limit.New(wt.InspectConcurrency),
		removalLimit: limit.New(4),
		repoLocks:    map[string]*sync.Mutex{},
	}
}

// repoLock returns the lock serializing removals in one repo. Orphans have no
// repo and share the "" lock, which is fine.
func (m *Model) repoLock(commonDir string) *sync.Mutex {
	m.repoLocksMu.Lock()
	defer m.repoLocksMu.Unlock()
	if m.repoLocks[commonDir] == nil {
		m.repoLocks[commonDir] = &sync.Mutex{}
	}

	return m.repoLocks[commonDir]
}

// Summary reports what was deleted. Call it after the program exits.
func (m *Model) Summary() Summary { return m.summary }

// Init starts the spinner.
func (m *Model) Init() tea.Cmd { return tick() }

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update handles a message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tickMsg:
		// The spinner only shows while scanning; stop redrawing after.
		if !m.scanning {
			return m, nil
		}

		m.frame++
		return m, tick()

	case FoundMsg:
		return m, m.add(msg.Worktree)

	case ScanDoneMsg:
		m.scanning = false
		m.scanTook = time.Since(m.started)

	case usageMsg:
		if r := m.byPath[msg.path]; r != nil {
			r.usage = &msg.usage
		}

	case gitMsg:
		if r := m.byPath[msg.path]; r != nil {
			r.git, r.gitErr = &msg.info, msg.err
		}

	case removedMsg:
		m.finishRemoval(msg)
		if m.quitting && !m.busy() {
			return m, tea.Quit
		}

	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	}

	return m, nil
}

func (m *Model) add(w wt.Worktree) tea.Cmd {
	if m.byPath[w.Path] != nil {
		return nil
	}

	r := &row{Worktree: w}
	m.rows = append(m.rows, r)
	m.byPath[w.Path] = r
	if m.cursor == "" {
		m.cursor = w.Path
	}

	if w.State == wt.StateMissing {
		r.usage = &wt.Usage{}
		r.git = &wt.GitInfo{}
		return nil
	}

	ctx, path := m.ctx, w.Path
	cmds := []tea.Cmd{func() tea.Msg {
		msg := usageMsg{path: path}
		m.usageLimit.Do(func() { msg.usage, msg.err = wt.MeasureUsage(ctx, path) })
		return msg
	}}

	if w.State == wt.StateOK {
		cmds = append(cmds, func() tea.Msg {
			msg := gitMsg{path: path}
			m.gitLimit.Do(func() { msg.info, msg.err = wt.InspectGit(ctx, w) })
			return msg
		})
	} else {
		r.git = &wt.GitInfo{}
	}

	return tea.Batch(cmds...)
}

// visible returns rows matching the filter, in the current sort order.
func (m *Model) visible() []*row {
	out := make([]*row, 0, len(m.rows))
	needle := strings.ToLower(m.filter)
	for _, r := range m.rows {
		if needle == "" || strings.Contains(strings.ToLower(r.Path+" "+r.Branch+" "+r.RepoName+" "+r.Tool), needle) {
			out = append(out, r)
		}
	}

	slices.SortStableFunc(out, func(a, b *row) int {
		switch m.sort {
		case sortSize:
			// Unmeasured rows sort last so they don't jump around above.
			return compareDesc(a.bytes(), b.bytes(), a.usage == nil, b.usage == nil)
		case sortAge:
			at, bt := a.lastUsed(), b.lastUsed()
			if at.IsZero() != bt.IsZero() {
				if at.IsZero() {
					return 1
				}

				return -1
			}

			return at.Compare(bt)
		default:
			return cmp.Or(strings.Compare(a.RepoName, b.RepoName), strings.Compare(a.Path, b.Path))
		}
	})
	return out
}

func compareDesc(a, b int64, aUnknown, bUnknown bool) int {
	switch {
	case aUnknown != bUnknown:
		if aUnknown {
			return 1
		}

		return -1
	case a > b:
		return -1
	case a < b:
		return 1
	default:
		return 0
	}
}

func (m *Model) cursorIndex(rows []*row) int {
	for i, r := range rows {
		if r.Path == m.cursor {
			return i
		}
	}

	return 0
}

// listHeight is how many rows fit: the screen minus the title and column
// header lines, the details block and the footer.
func (m *Model) listHeight() int {
	// While confirming, the warnings matter more than the rows.
	minRows := 3
	if m.confirming != nil {
		minRows = 1
	}

	return max(minRows, m.height-2-m.detailsHeight()-m.footerHeight())
}

// detailsLines is the height of the details block under the list: the full
// branch, the full path (up to two lines) and one line of facts the table
// leaves out. It has a fixed height so the list doesn't shift as the cursor
// moves.
const detailsLines = 4

// detailsHeight is 0 when showing the details would leave fewer than three
// rows, where the rows matter more.
func (m *Model) detailsHeight() int {
	if m.height-2-m.footerHeight()-detailsLines < 3 {
		return 0
	}

	return detailsLines
}

func (m *Model) footerHeight() int { return strings.Count(m.footer(), "\n") + 1 }

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	m.notice = ""

	if m.confirming != nil {
		targets := m.confirming
		m.confirming = nil
		if key == "y" || key == "Y" {
			return m.remove(targets)
		}

		return nil
	}

	if m.filtering {
		switch key {
		case "enter":
			m.filtering = false
		case "esc":
			m.filtering, m.filter = false, ""
		case "backspace":
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		default:
			if msg.Text != "" {
				m.filter += msg.Text
			}
		}

		return nil
	}

	rows := m.visible()
	idx := m.cursorIndex(rows)
	move := func(delta int) {
		if len(rows) > 0 {
			m.cursor = rows[min(len(rows)-1, max(0, idx+delta))].Path
		}
	}

	switch key {
	case "q", "esc", "ctrl+c":
		if m.filter != "" && key == "esc" {
			m.filter = ""
			return nil
		}

		if !m.busy() {
			return tea.Quit
		}

		m.quitting = true
	case "up", "k":
		move(-1)
	case "down", "j":
		move(1)
	case "pgup":
		move(-m.listHeight())
	case "pgdown":
		move(m.listHeight())
	case "home", "g":
		move(-len(rows))
	case "end", "G":
		move(len(rows))
	case "s":
		m.sort = (m.sort + 1) % sortKey(len(sortLabels))
	case "b":
		m.deleteBranch = !m.deleteBranch
	case "/":
		m.filtering = true
	case "space":
		if len(rows) == 0 || !rows[idx].active() {
			break
		}

		p := rows[idx].Path
		switch {
		case m.selected[p]:
			delete(m.selected, p)
		case rows[idx].deletable():
			m.selected[p] = true
		default:
			m.notice = rows[idx].whyNotDeletable()
		}
	case "a":
		// Toggle the shown rows only; selections the filter hides stay.
		all := true
		for _, r := range rows {
			if r.deletable() && !m.selected[r.Path] {
				all = false
			}
		}

		for _, r := range rows {
			switch {
			case all:
				delete(m.selected, r.Path)
			case r.deletable():
				m.selected[r.Path] = true
			}
		}
	case "enter", "d", "delete", "backspace":
		var targets []*row
		for _, r := range m.rows {
			if m.selected[r.Path] && r.deletable() {
				targets = append(targets, r)
			}
		}

		if len(targets) == 0 && len(rows) > 0 && rows[idx].active() {
			if reason := rows[idx].whyNotDeletable(); reason != "" {
				m.notice = reason
				break
			}

			targets = []*row{rows[idx]}
		}

		if len(targets) > 0 {
			m.confirming = targets
		}
	}

	return nil
}

func (m *Model) busy() bool {
	for _, r := range m.rows {
		if r.phase == phaseQueued {
			return true
		}
	}

	return false
}

func (m *Model) remove(targets []*row) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(targets))
	for _, r := range targets {
		delete(m.selected, r.Path)
		r.phase, r.note = phaseQueued, ""
		// Read model state here, on the update loop, not in the command.
		w, confirmed, deleteBranch := r.Worktree, *r.git, m.deleteBranch
		cmds = append(cmds, func() tea.Msg {
			var msg removedMsg
			m.removalLimit.Do(func() { msg = m.removeOne(w, confirmed, deleteBranch) })
			return msg
		})
	}

	return tea.Batch(cmds...)
}

// removeOne deletes one worktree whose git status was confirmed on screen.
// It runs in the background, after the prompt, so it only touches the model's
// fixed settings and its locks.
func (m *Model) removeOne(w wt.Worktree, confirmed wt.GitInfo, deleteBranch bool) removedMsg {
	lock := m.repoLock(w.CommonDir)
	lock.Lock()
	defer lock.Unlock()

	// Deletion was confirmed, so finish it even if the program is quitting.
	ctx := context.WithoutCancel(m.ctx)
	opts := wt.RemoveOptions{DeleteBranch: deleteBranch, Protect: m.opts.Protect}
	// Discarding changes and submodules was confirmed with them on screen.
	opts.Force = confirmed.NeedsForce()
	if opts.Force {
		// The prompt may have been up for a while; don't discard changes that
		// weren't there when it was confirmed.
		fresh, err := wt.InspectGit(ctx, w)
		if err == nil && !fresh.SameChanges(confirmed) {
			err = errors.New("its changes changed since you confirmed; not deleted")
		}

		if err != nil {
			return removedMsg{w.Path, wt.RemoveResult{}, err}
		}
	}

	result, err := wt.Remove(ctx, w, opts)
	return removedMsg{w.Path, result, err}
}

func (m *Model) finishRemoval(msg removedMsg) {
	r := m.byPath[msg.path]
	if r == nil {
		return
	}

	if msg.err != nil {
		r.phase, r.note = phaseFailed, msg.err.Error()
		m.summary.Failures = append(m.summary.Failures, fmt.Sprintf("%s: %v", Tildify(r.Path), msg.err))
		return
	}

	r.phase = phaseDeleted
	switch {
	case msg.result.BranchDeleted:
		r.note = "branch deleted"
	case msg.result.BranchError != nil:
		r.note = msg.result.BranchError.Error()
	}

	m.summary.Removed++
	m.summary.Freed += r.bytes()
}
