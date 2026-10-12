// Package ui is the interactive worktree list, in the spirit of npkill.
package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ConstantinChirila/worktree-kill/internal/wt"
)

// Messages sent into the program from the scanner and background work.
type (
	FoundMsg    struct{ Worktree wt.Worktree }
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
	phaseQueued
	phaseDeleting
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

func (r *row) active() bool { return r.phase == phaseIdle || r.phase == phaseFailed }

func (r *row) lastUsed() time.Time {
	var t time.Time
	if r.usage != nil {
		t = r.usage.LatestChange
	}

	if r.git != nil && r.git.LastCommit.After(t) {
		t = r.git.LastCommit
	}

	return t
}

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

type Model struct {
	ctx      context.Context
	title    string
	cwd      string
	visited  func() int64
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
	quitting     bool
	frame        int

	width, height int
	summary       Summary
}

// Limits for background work; size walks are IO-heavy so they get fewer slots.
var (
	usageSlots  = make(chan struct{}, 4)
	gitSlots    = make(chan struct{}, 8)
	removalLock sync.Mutex
)

func New(ctx context.Context, title, cwd string, visited func() int64) *Model {
	return &Model{
		ctx:      ctx,
		title:    title,
		cwd:      cwd,
		visited:  visited,
		started:  time.Now(),
		scanning: true,
		byPath:   map[string]*row{},
		selected: map[string]bool{},
		width:    100,
		height:   30,
	}
}

func (m *Model) Summary() Summary { return m.summary }

func (m *Model) Init() tea.Cmd { return tick() }

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tickMsg:
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

	if w.State != wt.StateOK && w.State != wt.StateOrphaned {
		r.usage = &wt.Usage{}
		r.git = &wt.GitInfo{}
		return nil
	}

	ctx, path := m.ctx, w.Path
	cmds := []tea.Cmd{func() tea.Msg {
		usageSlots <- struct{}{}
		defer func() { <-usageSlots }()
		usage, err := wt.MeasureUsage(ctx, path)
		return usageMsg{path, usage, err}
	}}

	if w.State == wt.StateOK {
		cmds = append(cmds, func() tea.Msg {
			gitSlots <- struct{}{}
			defer func() { <-gitSlots }()
			info, err := wt.InspectGit(ctx, w)
			return gitMsg{path, info, err}
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
			return strings.Compare(a.RepoName+a.Path, b.RepoName+b.Path)
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

func (m *Model) listHeight() int { return max(3, m.height-6) }

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()

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
		case "enter", "esc":
			m.filtering = false
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
		if len(rows) > 0 && rows[idx].active() {
			p := rows[idx].Path
			if m.selected[p] {
				delete(m.selected, p)
			} else {
				m.selected[p] = true
			}
		}
	case "a":
		all := true
		for _, r := range rows {
			if r.active() && !m.selected[r.Path] {
				all = false
			}
		}

		clear(m.selected)
		if !all {
			for _, r := range rows {
				if r.active() {
					m.selected[r.Path] = true
				}
			}
		}
	case "enter", "d", "delete", "backspace":
		var targets []*row
		for _, r := range m.rows {
			if m.selected[r.Path] && r.active() {
				targets = append(targets, r)
			}
		}

		if len(targets) == 0 && len(rows) > 0 && rows[idx].active() {
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
		if r.phase == phaseQueued || r.phase == phaseDeleting {
			return true
		}
	}

	return false
}

func (m *Model) remove(targets []*row) tea.Cmd {
	opts := wt.RemoveOptions{DeleteBranch: m.deleteBranch}
	cmds := make([]tea.Cmd, 0, len(targets))
	for _, r := range targets {
		delete(m.selected, r.Path)
		r.phase, r.note = phaseQueued, ""
		w, o := r.Worktree, opts
		// Discarding changes was confirmed with the change count on screen.
		o.Force = r.git != nil && r.git.Changes > 0
		cmds = append(cmds, func() tea.Msg {
			// One removal at a time: concurrent `git worktree` calls on the
			// same repo contend for its locks.
			removalLock.Lock()
			defer removalLock.Unlock()
			result, err := wt.Remove(context.WithoutCancel(m.ctx), w, o)
			return removedMsg{w.Path, result, err}
		})
	}

	return tea.Batch(cmds...)
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

// ---- rendering ----

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF5F87"))
	dim         = lipgloss.NewStyle().Faint(true)
	headerStyle = lipgloss.NewStyle().Bold(true).Faint(true)
	goneStyle   = lipgloss.NewStyle().Faint(true)
	red         = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	green       = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellow      = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	blue        = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
	magenta     = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	cyan        = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	spinner     = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
)

type layout struct{ tool, repo, branch, path, size, age, status int }

func (m *Model) layout() layout {
	l := layout{tool: 9, size: 8, age: 9}
	// 2 for the selection mark, 7 single-space gaps.
	flex := max(40, m.width-2-l.tool-l.size-l.age-7)
	l.repo = min(18, flex*15/100)
	l.branch = min(30, flex*25/100)
	l.status = min(28, flex*22/100)
	l.path = flex - l.repo - l.branch - l.status
	return l
}

func (m *Model) View() tea.View {
	var b strings.Builder
	rows := m.visible()
	l := m.layout()

	b.WriteString(m.headerLine(rows) + "\n")
	b.WriteString(headerStyle.Render(strings.Join([]string{
		"  " + padEnd("TOOL", l.tool), padEnd("REPO", l.repo), padEnd("BRANCH", l.branch),
		padEnd("PATH", l.path), padStart("SIZE", l.size), padStart("LAST USED", l.age), "STATUS",
	}, " ")) + "\n")

	height := m.listHeight()
	idx := m.cursorIndex(rows)
	offset := min(max(0, idx-height/2), max(0, len(rows)-height))
	end := min(len(rows), offset+height)
	for i := offset; i < end; i++ {
		b.WriteString(m.renderRow(rows[i], l, i == idx) + "\n")
	}

	if len(rows) == 0 {
		msg := "Looking for worktrees…"
		if !m.scanning {
			msg = "No worktrees found."
		}

		if m.filter != "" {
			msg = "No worktrees match the filter."
		}

		b.WriteString(dim.Render("  "+msg) + "\n")
		end = offset + 1
	}

	b.WriteString(strings.Repeat("\n", max(0, height-(end-offset))))
	b.WriteString(m.footer())

	v := tea.NewView(b.String())
	v.AltScreen = true
	v.WindowTitle = "worktreekill"
	return v
}

func (m *Model) headerLine(rows []*row) string {
	var total int64
	count := 0
	for _, r := range m.rows {
		if r.active() {
			total += r.bytes()
			count++
		}
	}

	scan := fmt.Sprintf("scanned %s in %.1fs", m.title, m.scanTook.Seconds())
	if m.scanning {
		scan = fmt.Sprintf("%s scanning %s · %d dirs", spinner[m.frame%len(spinner)], m.title, m.visited())
	}

	parts := []string{titleStyle.Render("worktreekill"), dim.Render(scan), fmt.Sprintf("%d worktrees · %s", count, FormatBytes(total))}
	if n := len(m.selected); n > 0 {
		var sel int64
		for p := range m.selected {
			sel += m.byPath[p].bytes()
		}

		parts = append(parts, cyan.Render(fmt.Sprintf("%d selected (%s)", n, FormatBytes(sel))))
	}

	if m.summary.Removed > 0 {
		parts = append(parts, green.Render("freed "+FormatBytes(m.summary.Freed)))
	}

	if m.filter != "" || m.filtering {
		parts = append(parts, yellow.Render(fmt.Sprintf("filter: %s (%d)", m.filter, len(rows))))
	}

	return strings.Join(parts, "  ")
}

func (m *Model) renderRow(r *row, l layout, isCursor bool) string {
	mark := "○ "
	switch {
	case m.selected[r.Path]:
		mark = "◉ "
	case r.phase == phaseDeleted:
		mark = "  "
	}

	branch := r.Branch
	if branch == "" && r.Head != "" {
		branch = "(" + r.Head[:min(7, len(r.Head))] + ")"
	}

	size, sizeStyle := "…", dim
	if r.usage != nil {
		size, sizeStyle = FormatBytes(r.usage.Bytes), lipgloss.NewStyle()
		switch {
		case r.usage.Bytes > 1<<30:
			sizeStyle = red
		case r.usage.Bytes > 100<<20:
			sizeStyle = yellow
		}
	}

	age, ageStyle := "…", dim
	if last := r.lastUsed(); !last.IsZero() {
		age = FormatAge(last, time.Now())
		switch days := time.Since(last).Hours() / 24; {
		case days > 30:
			ageStyle = red
		case days > 7:
			ageStyle = yellow
		default:
			ageStyle = green
		}
	} else if r.usage != nil && r.git != nil {
		age = "-"
	}

	status, statusStyle := m.status(r)
	cells := []struct {
		text  string
		style lipgloss.Style
	}{
		{mark, cyan},
		{padEnd(truncEnd(r.Tool, l.tool), l.tool) + " ", magenta},
		{padEnd(truncEnd(r.RepoName, l.repo), l.repo) + " ", lipgloss.NewStyle().Bold(true)},
		{padEnd(truncEnd(branch, l.branch), l.branch) + " ", blue},
		{padEnd(truncStart(Tildify(r.Path), l.path), l.path) + " ", lipgloss.NewStyle()},
		{padStart(size, l.size) + " ", sizeStyle},
		{padStart(age, l.age) + " ", ageStyle},
		{truncEnd(status, l.status), statusStyle},
	}

	var b strings.Builder
	for _, c := range cells {
		style := c.style
		if r.phase == phaseDeleted {
			style = goneStyle
		}

		if isCursor {
			style = style.Reverse(true)
		}

		b.WriteString(style.Render(c.text))
	}

	return b.String()
}

func (m *Model) status(r *row) (string, lipgloss.Style) {
	switch r.phase {
	case phaseQueued:
		return "queued", yellow
	case phaseDeleting:
		return "deleting…", yellow
	case phaseDeleted:
		if r.note != "" {
			return "deleted, " + r.note, green
		}

		return "deleted", green
	case phaseFailed:
		return "failed: " + r.note, red
	}

	var parts []string
	if r.Path == m.cwd || strings.HasPrefix(m.cwd, r.Path+string(filepath.Separator)) {
		parts = append(parts, "you are here")
	}

	switch r.State {
	case wt.StateMissing:
		return "dir missing (stale)", dim
	case wt.StateOrphaned:
		return "orphaned (repo gone)", yellow
	}

	if r.Locked {
		parts = append(parts, "locked")
	}

	if r.gitErr != nil {
		return "git error: " + r.gitErr.Error(), red
	}

	if r.git == nil {
		if len(parts) > 0 {
			return strings.Join(parts, " · "), yellow
		}

		return "…", dim
	}

	if r.git.Changes > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", r.git.Changes))
	}

	if r.git.Unpushed > 0 {
		parts = append(parts, fmt.Sprintf("%d unpushed", r.git.Unpushed))
	}

	if len(parts) == 0 {
		return "clean", green
	}

	return strings.Join(parts, " · "), yellow
}

func (m *Model) footer() string {
	if m.quitting {
		return yellow.Render("Finishing deletions in progress…") + "\n"
	}

	if m.confirming != nil {
		return m.confirmPrompt()
	}

	if m.filtering {
		return "\n" + yellow.Render("/"+m.filter+"█") + dim.Render("  enter apply · esc done")
	}

	branch := "off"
	if m.deleteBranch {
		branch = yellow.Render("on")
	}

	help := fmt.Sprintf("↑↓ move · space select · a all · enter delete · b delete branch: %s · s sort: %s · / filter · q quit",
		branch, sortLabels[m.sort])
	return "\n" + dim.Render(help)
}

func (m *Model) confirmPrompt() string {
	var bytes int64
	var dirty, locked, unpushed, orphaned int
	for _, r := range m.confirming {
		bytes += r.bytes()
		if r.git != nil && r.git.Changes > 0 {
			dirty++
		}

		if r.git != nil && r.git.Unpushed > 0 {
			unpushed++
		}

		if r.Locked {
			locked++
		}

		if r.State == wt.StateOrphaned {
			orphaned++
		}
	}

	var warnings []string
	if dirty > 0 {
		warnings = append(warnings, fmt.Sprintf("%d with uncommitted changes (will be lost)", dirty))
	}

	if locked > 0 {
		warnings = append(warnings, fmt.Sprintf("%d locked (a tool may be using it)", locked))
	}

	if orphaned > 0 {
		warnings = append(warnings, fmt.Sprintf("%d orphaned (directory deleted outright)", orphaned))
	}

	if unpushed > 0 && m.deleteBranch {
		warnings = append(warnings, fmt.Sprintf("%d with unpushed commits (branch kept if unmerged)", unpushed))
	}

	what := fmt.Sprintf("%d worktrees", len(m.confirming))
	if len(m.confirming) == 1 {
		what = Tildify(m.confirming[0].Path)
	}

	if m.deleteBranch {
		what += " and branches"
	}

	line1 := ""
	style := yellow
	if len(warnings) > 0 {
		line1 = red.Render("⚠ " + strings.Join(warnings, " · "))
		style = red
	}

	return line1 + "\n" + style.Bold(true).Render(fmt.Sprintf("Delete %s (%s)?", what, FormatBytes(bytes))) + dim.Render(" [y/N]")
}
