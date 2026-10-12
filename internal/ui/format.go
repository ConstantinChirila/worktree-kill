package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/ConstantinChirila/worktree-kill/internal/wt"
)

// FormatBytes renders a byte count like "1.5 GB".
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	value, exp := float64(n), 0
	for value >= unit && exp < 4 {
		value /= unit
		exp++
	}

	suffix := []string{"B", "KB", "MB", "GB", "TB"}[exp]
	if value >= 100 {
		return fmt.Sprintf("%.0f %s", value, suffix)
	}

	return fmt.Sprintf("%.1f %s", value, suffix)
}

// FormatAge renders how long before now t was, like "3d ago".
func FormatAge(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
}

// home is the canonical home folder, or empty if it can't be found, in which
// case Tildify leaves paths alone.
var home = func() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return wt.CanonicalPath(dir)
}()

// Tildify shortens a path inside the home folder to start with "~".
func Tildify(path string) string {
	if home == "" {
		return path
	}

	if path == home {
		return "~"
	}

	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}

	return path
}

// Sanitize replaces control and bidi-override characters with "?", so a
// crafted path, branch or git message can't send escape codes to the terminal.
func Sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return '?'
		}

		return r
	}, s)
}

// StatusParts describes a worktree for the STATUS column, like
// ["locked", "2 changed"]. info is nil while git status is loading. No parts
// means clean.
func StatusParts(w wt.Worktree, info *wt.GitInfo) []string {
	var parts []string
	switch w.State {
	case wt.StateMissing:
		parts = append(parts, "dir missing")
	case wt.StateOrphaned:
		parts = append(parts, "orphaned")
	}

	if w.Locked {
		parts = append(parts, "locked")
	}

	if info != nil && info.Changes > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", info.Changes))
	}

	if info != nil && info.Unpushed > 0 {
		parts = append(parts, fmt.Sprintf("%d unpushed", info.Unpushed))
	}

	return parts
}

// LastUsed is the newer of the last file change and the last commit. Either
// may be nil while loading.
func LastUsed(usage *wt.Usage, info *wt.GitInfo) time.Time {
	var t time.Time
	if usage != nil {
		t = usage.LatestChange
	}

	if info != nil && info.LastCommit.After(t) {
		t = info.LastCommit
	}

	return t
}

// The helpers below measure in terminal cells, not runes: CJK characters and
// most emoji take two cells. They use the same width rules as lipgloss.

// truncEnd cuts the end of s to fit in width cells.
func truncEnd(s string, width int) string {
	if width <= 0 {
		return ""
	}

	return ansi.Truncate(s, width, "…")
}

// truncStart cuts the start of s to fit in width cells, keeping the
// informative tail of a path.
func truncStart(s string, width int) string {
	if width <= 0 {
		return ""
	}

	over := ansi.StringWidth(s) - width
	if over <= 0 {
		return s
	}

	// Cut one more cell for the "…". TruncateLeft keeps a wide character the
	// cut lands in the middle of, so cut further until it fits.
	out := ansi.TruncateLeft(s, over+1, "…")
	for n := over + 2; ansi.StringWidth(out) > width; n++ {
		out = ansi.TruncateLeft(s, n, "…")
	}

	return out
}

func padEnd(s string, width int) string {
	if n := ansi.StringWidth(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}

	return s
}

func padStart(s string, width int) string {
	if n := ansi.StringWidth(s); n < width {
		return strings.Repeat(" ", width-n) + s
	}

	return s
}
