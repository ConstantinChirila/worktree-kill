// Command worktreekill finds git worktrees across your machine and lets you
// delete the ones you no longer need. Like npkill, for worktrees.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ConstantinChirila/worktree-kill/internal/ui"
	"github.com/ConstantinChirila/worktree-kill/internal/wt"
)

// Set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `worktreekill: find and delete git worktrees, like npkill.

Usage
  worktreekill [dir...]

  With no dir, scans your home folder plus the places AI tools keep
  worktrees (~/.claude-worktrees, ~/.cursor/worktrees, ~/.codex/worktrees,
  ~/conductor, ~/.t3/worktrees). With dirs, scans only those.

Options
  -l, --list      print a table and exit (no UI)
      --json      print JSON and exit
  -v, --version   print version
  -h, --help      show this help

Keys
  ↑/↓ j/k  move            space  select        a  select all
  enter/d  delete          b      also delete branch (git branch -d)
  s        cycle sort      /      filter        q  quit
`

func main() {
	var list, asJSON, showVersion bool
	flags := flag.NewFlagSet("worktreekill", flag.ContinueOnError)
	flags.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flags.BoolVar(&list, "list", false, "")
	flags.BoolVar(&list, "l", false, "")
	flags.BoolVar(&asJSON, "json", false, "")
	flags.BoolVar(&showVersion, "version", false, "")
	flags.BoolVar(&showVersion, "v", false, "")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return
		}

		os.Exit(2)
	}

	if showVersion {
		fmt.Println(version)
		return
	}

	home, _ := os.UserHomeDir()
	roots, title := scanRoots(flags.Args(), home)
	opts := wt.ScanOptions{Roots: roots, Home: home}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	interactive := !list && !asJSON && isTerminal(os.Stdout) && isTerminal(os.Stdin)
	if !interactive {
		if err := report(ctx, opts, asJSON); err != nil {
			fmt.Fprintln(os.Stderr, "worktreekill:", err)
			os.Exit(1)
		}

		return
	}

	os.Exit(runUI(ctx, opts, title))
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func scanRoots(args []string, home string) (roots []string, title string) {
	if len(args) == 0 {
		return append(wt.KnownDirs(home), home), "~"
	}

	names := make([]string, 0, len(args))
	for _, arg := range args {
		abs, err := filepath.Abs(arg)
		if err != nil {
			abs = arg
		}

		roots = append(roots, abs)
		names = append(names, ui.Tildify(abs))
	}

	return roots, strings.Join(names, ", ")
}

func runUI(ctx context.Context, opts wt.ScanOptions, title string) int {
	cwd, _ := os.Getwd()
	var scanner *wt.Scanner
	var program *tea.Program
	scanner = wt.NewScanner(opts, func(w wt.Worktree) { program.Send(ui.FoundMsg{Worktree: w}) })
	model := ui.New(ctx, title, cwd, scanner.Visited)
	// Ctrl+C is handled by the model so in-flight deletions can finish.
	program = tea.NewProgram(model, tea.WithContext(ctx), tea.WithoutSignalHandler())

	go func() {
		scanner.Run(ctx)
		program.Send(ui.ScanDoneMsg{})
	}()

	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "worktreekill:", err)
		return 1
	}

	summary := model.Summary()
	if summary.Removed > 0 {
		fmt.Printf("Deleted %d worktree(s), freed %s.\n", summary.Removed, ui.FormatBytes(summary.Freed))
	}

	for _, f := range summary.Failures {
		fmt.Fprintln(os.Stderr, "Failed:", f)
	}

	if len(summary.Failures) > 0 {
		return 1
	}

	return 0
}

type entry struct {
	Path      string    `json:"path"`
	Repo      string    `json:"repo"`
	RepoName  string    `json:"repoName"`
	Branch    string    `json:"branch,omitempty"`
	Tool      string    `json:"tool"`
	State     string    `json:"state"`
	Locked    bool      `json:"locked"`
	SizeBytes int64     `json:"sizeBytes"`
	LastUsed  time.Time `json:"lastUsed,omitzero"`
	Changes   int       `json:"uncommittedChanges"`
	Unpushed  int       `json:"unpushedCommits"`
}

var stateNames = map[wt.State]string{wt.StateOK: "ok", wt.StateMissing: "missing", wt.StateOrphaned: "orphaned"}

func report(ctx context.Context, opts wt.ScanOptions, asJSON bool) error {
	var mu sync.Mutex
	var worktrees []wt.Worktree
	wt.Scan(ctx, opts, func(w wt.Worktree) {
		mu.Lock()
		worktrees = append(worktrees, w)
		mu.Unlock()
	})

	entries := make([]entry, len(worktrees))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for i, w := range worktrees {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			e := entry{Path: w.Path, Repo: w.RepoPath, RepoName: w.RepoName, Branch: w.Branch, Tool: w.Tool, State: stateNames[w.State], Locked: w.Locked}
			if w.State != wt.StateMissing {
				if usage, err := wt.MeasureUsage(ctx, w.Path); err == nil {
					e.SizeBytes, e.LastUsed = usage.Bytes, usage.LatestChange
				}
			}

			if info, err := wt.InspectGit(ctx, w); err == nil {
				e.Changes, e.Unpushed = info.Changes, info.Unpushed
				if info.LastCommit.After(e.LastUsed) {
					e.LastUsed = info.LastCommit
				}
			}

			entries[i] = e
		})
	}

	wg.Wait()
	slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(b.SizeBytes, a.SizeBytes) })

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TOOL\tREPO\tBRANCH\tPATH\tSIZE\tLAST USED\tSTATUS")
	for _, e := range entries {
		last := "-"
		if !e.LastUsed.IsZero() {
			last = ui.FormatAge(e.LastUsed, time.Now())
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Tool, e.RepoName, e.Branch,
			ui.Tildify(e.Path), ui.FormatBytes(e.SizeBytes), last, statusText(e))
	}

	return tw.Flush()
}

func statusText(e entry) string {
	var parts []string
	if e.State != "ok" {
		parts = append(parts, e.State)
	}

	if e.Locked {
		parts = append(parts, "locked")
	}

	if e.Changes > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", e.Changes))
	}

	if e.Unpushed > 0 {
		parts = append(parts, fmt.Sprintf("%d unpushed", e.Unpushed))
	}

	if len(parts) == 0 {
		return "clean"
	}

	return strings.Join(parts, ", ")
}
