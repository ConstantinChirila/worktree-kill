package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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

var home, _ = os.UserHomeDir()

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

// truncEnd cuts the end of s to fit width display cells.
func truncEnd(s string, width int) string {
	r := []rune(s)
	if width <= 0 {
		return ""
	}

	if len(r) <= width {
		return s
	}

	return string(r[:width-1]) + "…"
}

// truncStart cuts the start of s, keeping the informative tail of a path.
func truncStart(s string, width int) string {
	r := []rune(s)
	if width <= 0 {
		return ""
	}

	if len(r) <= width {
		return s
	}

	return "…" + string(r[len(r)-width+1:])
}

func padEnd(s string, width int) string {
	if n := len([]rune(s)); n < width {
		return s + strings.Repeat(" ", width-n)
	}

	return s
}

func padStart(s string, width int) string {
	if n := len([]rune(s)); n < width {
		return strings.Repeat(" ", width-n) + s
	}

	return s
}
