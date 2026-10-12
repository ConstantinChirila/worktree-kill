// Command worktreekill finds git worktrees across your machine and lets you
// delete the ones you no longer need. Like npkill, for worktrees.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/ConstantinChirila/worktree-kill/internal/ui"
	"github.com/ConstantinChirila/worktree-kill/internal/wt"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `worktreekill: find and delete git worktrees, like npkill.

Usage
  worktreekill [dir...]

  With no dir, scans your home folder plus the places AI tools keep
  worktrees (~/.claude-worktrees, ~/.cursor/worktrees, ~/.codex/worktrees,
  $CODEX_HOME/worktrees, ~/conductor, ~/.t3/worktrees). With dirs, scans
  only those. When output is piped, prints the table instead of the UI.

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
		if errors.Is(err, flag.ErrHelp) {
			return
		}

		os.Exit(2)
	}

	if showVersion {
		fmt.Println(version)
		return
	}

	// Without a home folder, explicit dirs still work; only "~" and the
	// home-relative tool folders are lost.
	home, err := os.UserHomeDir()
	switch {
	case err == nil:
		home = wt.CanonicalPath(home)
	case flags.NArg() == 0:
		fmt.Fprintln(os.Stderr, "worktreekill: can't find your home folder; pass the folders to scan:", err)
		os.Exit(1)
	}

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

		abs = wt.CanonicalPath(abs)
		roots = append(roots, abs)
		names = append(names, ui.Tildify(abs))
	}

	return roots, strings.Join(names, ", ")
}

func runUI(ctx context.Context, opts wt.ScanOptions, title string) int {
	// Only used to mark the worktree you are in, so an error just skips that.
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	} else {
		cwd = wt.CanonicalPath(cwd)
	}

	var program *tea.Program
	scanner := wt.NewScanner(opts, func(w wt.Worktree) { program.Send(ui.FoundMsg{Worktree: w}) })
	model := ui.New(ctx, ui.Options{Title: title, Cwd: cwd, Protect: opts.Protection(), Visited: scanner.Visited})
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
		fmt.Fprintln(os.Stderr, "Failed:", ui.Sanitize(f))
	}

	if len(summary.Failures) > 0 {
		return 1
	}

	return 0
}
