package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ConstantinChirila/worktree-kill/internal/wt"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF5F87"))
	dim         = lipgloss.NewStyle().Faint(true)
	headerStyle = lipgloss.NewStyle().Bold(true).Faint(true)
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

// View renders the list.
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
	for _, line := range m.details(rows) {
		b.WriteString(line + "\n")
	}

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

	scan := fmt.Sprintf("scanned %s in %.1fs", m.opts.Title, m.scanTook.Seconds())
	if m.scanning {
		scan = fmt.Sprintf("%s scanning %s · %d dirs", spinner[m.frame%len(spinner)], m.opts.Title, m.opts.Visited())
	}

	parts := []string{titleStyle.Render("worktreekill"), dim.Render(Sanitize(scan)), fmt.Sprintf("%d worktrees · %s", count, FormatBytes(total))}
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
		parts = append(parts, yellow.Render(fmt.Sprintf("filter: %s (%d)", Sanitize(m.filter), len(rows))))
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

	status, statusStyle := r.status(m.opts.Cwd)
	cells := []struct {
		text  string
		style lipgloss.Style
	}{
		{mark, cyan},
		{padEnd(truncEnd(r.Tool, l.tool), l.tool) + " ", magenta},
		{padEnd(truncEnd(Sanitize(r.RepoName), l.repo), l.repo) + " ", lipgloss.NewStyle().Bold(true)},
		{padEnd(truncEnd(Sanitize(branch), l.branch), l.branch) + " ", blue},
		{padEnd(truncStart(Sanitize(Tildify(r.Path)), l.path), l.path) + " ", lipgloss.NewStyle()},
		{padStart(size, l.size) + " ", sizeStyle},
		{padStart(age, l.age) + " ", ageStyle},
		{truncEnd(Sanitize(status), l.status), statusStyle},
	}

	var b strings.Builder
	for _, c := range cells {
		style := c.style
		if r.phase == phaseDeleted {
			style = dim
		}

		if isCursor {
			style = style.Reverse(true)
		}

		b.WriteString(style.Render(c.text))
	}

	return b.String()
}

// details returns detailsHeight lines describing the highlighted row in full:
// its branch, its path, wrapped, then what the table cuts off or doesn't show.
func (m *Model) details(rows []*row) []string {
	lines := make([]string, m.detailsHeight())
	if len(lines) == 0 || len(rows) == 0 {
		return lines
	}

	r := rows[m.cursorIndex(rows)]
	width := max(20, m.width-2)
	// The branch gets its own line: the table cuts it, and branch names often
	// differ only at the end.
	switch {
	case r.Branch != "":
		lines[0] = "  " + dim.Render("branch ") + blue.Render(truncEnd(Sanitize(r.Branch), width-len("branch ")))
	case r.Head != "":
		lines[0] = "  " + dim.Render("detached at "+r.Head[:min(7, len(r.Head))])
	}

	// A path longer than two lines keeps its end, the informative part.
	path := truncStart(Sanitize(Tildify(r.Path)), 2*width)
	wrapped := strings.Split(ansi.Hardwrap(path, width, true), "\n")
	n := min(2, len(wrapped))
	for i := range n {
		lines[1+i] = "  " + wrapped[i]
	}

	// Facts go right under the path; a short path leaves the last line blank.
	lines[1+n] = "  " + dim.Render(truncEnd(Sanitize(strings.Join(r.facts(), " · ")), width))
	return lines
}

// facts lists what the details line shows about a row: anything the table
// cuts off or leaves out, most important first (the line is cut to fit), then
// where it comes from.
func (r *row) facts() []string {
	var facts []string
	if r.Locked && r.LockReason != "" {
		facts = append(facts, "locked: "+r.LockReason)
	}

	if r.phase == phaseFailed {
		facts = append(facts, "failed: "+r.note)
	}

	if r.gitErr != nil {
		facts = append(facts, "git error: "+r.gitErr.Error())
	}

	if r.git != nil && len(r.git.Ignored) > 0 {
		facts = append(facts, "ignored: "+strings.Join(r.git.Ignored, ", "))
	}

	if r.git != nil && r.git.Submodules > 0 {
		facts = append(facts, fmt.Sprintf("%d populated submodules", r.git.Submodules))
	}

	if why := r.whyNotDeletable(); r.active() && why != "" && r.gitErr == nil {
		facts = append(facts, why)
	}

	switch {
	case r.RepoPath != "":
		facts = append(facts, "repo "+Tildify(r.RepoPath))
	case r.State == wt.StateOrphaned:
		facts = append(facts, "repo "+r.RepoName+" (gone)")
	}

	return facts
}

func (r *row) status(cwd string) (string, lipgloss.Style) {
	switch r.phase {
	case phaseQueued:
		return "deleting…", yellow
	case phaseDeleted:
		if r.note != "" {
			return "deleted, " + r.note, green
		}

		return "deleted", green
	case phaseFailed:
		return "failed: " + r.note, red
	}

	if r.gitErr != nil {
		return "git error: " + r.gitErr.Error(), red
	}

	var parts []string
	if cwd != "" && (r.Path == cwd || strings.HasPrefix(cwd, r.Path+string(filepath.Separator))) {
		parts = append(parts, "you are here")
	}

	parts = append(parts, StatusParts(r.Worktree, r.git)...)
	switch {
	case r.git == nil && len(parts) == 0:
		return "…", dim
	case len(parts) == 0:
		return "clean", green
	case r.State == wt.StateMissing && !r.Locked:
		return strings.Join(parts, " · "), dim
	default:
		return strings.Join(parts, " · "), yellow
	}
}

func (m *Model) footer() string {
	if m.quitting {
		return yellow.Render("Finishing deletions in progress…") + "\n"
	}

	if m.confirming != nil {
		return m.confirmPrompt()
	}

	// Every footer line is cut to the screen width, so it never wraps and
	// listHeight can count its lines.
	width := max(20, m.width)
	if m.filtering {
		return "\n" + truncEnd(yellow.Render("/"+Sanitize(m.filter)+"█")+dim.Render("  enter apply · esc clear"), width)
	}

	notice := ""
	if m.notice != "" {
		notice = yellow.Render(truncEnd(Sanitize(m.notice), width))
	}

	branch := "off"
	if m.deleteBranch {
		branch = yellow.Render("on")
	}

	help := fmt.Sprintf("↑↓ move · space select · a all · enter delete · b delete branch: %s · s sort: %s · / filter · q quit",
		branch, sortLabels[m.sort])
	return notice + "\n" + dim.Render(truncEnd(help, width))
}

func (m *Model) confirmPrompt() string {
	var bytes int64
	for _, r := range m.confirming {
		bytes += r.bytes()
	}

	warnings := m.deleteWarnings(m.confirming)
	target := fmt.Sprintf("%d worktrees", len(m.confirming))
	if len(m.confirming) == 1 {
		target = Sanitize(Tildify(m.confirming[0].Path))
	}

	branches := ""
	if m.deleteBranch {
		branches = " and branches"
	}

	// Warnings wrap (each one matters); the question stays on one line, with
	// a long path cut at the start.
	width := max(20, m.width)
	question := func(target string) string {
		return fmt.Sprintf("Delete %s%s (%s)?", target, branches, FormatBytes(bytes))
	}

	target = truncStart(target, max(10, width-ansi.StringWidth(question("")+" [y/N]")))
	warningLine := ""
	style := yellow
	if len(warnings) > 0 {
		warningLine = red.Render(ansi.Wordwrap("⚠ "+strings.Join(warnings, " · "), width, ""))
		style = red
	}

	return warningLine + "\n" + style.Bold(true).Render(question(target)) + dim.Render(" [y/N]")
}

// deleteWarnings lists what deleting targets would lose or skip, each with
// how many targets it applies to, like "2 with uncommitted changes (...)".
func (m *Model) deleteWarnings(targets []*row) []string {
	visible := map[*row]bool{}
	for _, r := range m.visible() {
		visible[r] = true
	}

	// Name each ignored file once, even if several targets have it.
	var ignored []string
	for _, r := range targets {
		for _, name := range r.git.Ignored {
			if !slices.Contains(ignored, name) {
				ignored = append(ignored, name)
			}
		}
	}

	ignoredNames := strings.Join(ignored[:min(2, len(ignored))], ", ")
	if len(ignored) > 2 {
		ignoredNames += ", …"
	}

	checks := []struct {
		applies func(r *row) bool
		// text has a %d for the count.
		text string
	}{
		{func(r *row) bool { return r.git.Changes > 0 }, "%d with uncommitted changes (will be lost)"},
		{func(r *row) bool { return r.git.Unpushed > 0 && r.Branch == "" }, "%d with commits on no branch (will be lost)"},
		{func(r *row) bool { return m.deleteBranch && r.git.Unpushed > 0 && r.Branch != "" }, "%d with unpushed commits (branch kept if unmerged)"},
		{func(r *row) bool { return r.Locked }, "%d locked (a tool may be using it)"},
		{func(r *row) bool { return r.State == wt.StateOrphaned }, "%d orphaned (folder deleted outright, changes can't be checked)"},
		{func(r *row) bool { return len(r.git.Ignored) > 0 }, "%d with ignored files (will be lost: " + strings.ReplaceAll(Sanitize(ignoredNames), "%", "%%") + ")"},
		{func(r *row) bool { return r.git.Submodules > 0 }, "%d with submodules or embedded repos (deleted with their changes and unpushed commits, which aren't checked)"},
		{func(r *row) bool { return !visible[r] }, "%d hidden by the filter"},
	}

	var warnings []string
	for _, c := range checks {
		count := 0
		for _, r := range targets {
			if c.applies(r) {
				count++
			}
		}

		if count > 0 {
			warnings = append(warnings, fmt.Sprintf(c.text, count))
		}
	}

	return warnings
}
