package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/ConstantinChirila/worktree-kill/internal/ui"
	"github.com/ConstantinChirila/worktree-kill/internal/wt"
)

// record is one worktree in --list and --json output.
type record struct {
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
	// status is the STATUS column text.
	status string
}

// report scans, measures every worktree and prints them, largest first.
func report(ctx context.Context, opts wt.ScanOptions, asJSON bool) error {
	l := limits{usage: make(chan struct{}, 2), git: make(chan struct{}, 8)}
	var mu sync.Mutex
	var records []record
	var wg sync.WaitGroup
	// Measure each worktree as soon as it is found, while the scan goes on.
	wt.NewScanner(opts, func(w wt.Worktree) {
		wg.Go(func() {
			r := measure(ctx, w, l)
			mu.Lock()
			records = append(records, r)
			mu.Unlock()
		})
	}).Run(ctx)

	wg.Wait()
	slices.SortFunc(records, func(a, b record) int { return cmp.Compare(b.SizeBytes, a.SizeBytes) })

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(records)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TOOL\tREPO\tBRANCH\tPATH\tSIZE\tLAST USED\tSTATUS")
	for _, r := range records {
		last := "-"
		if !r.LastUsed.IsZero() {
			last = ui.FormatAge(r.LastUsed, time.Now())
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Tool, ui.Sanitize(r.RepoName), ui.Sanitize(r.Branch),
			ui.Sanitize(ui.Tildify(r.Path)), ui.FormatBytes(r.SizeBytes), last, ui.Sanitize(r.status))
	}

	return tw.Flush()
}

// limits caps concurrent size walks (each already reads several folders at
// once) and git inspections.
type limits struct{ usage, git chan struct{} }

func withSlot(slots chan struct{}, f func()) {
	slots <- struct{}{}
	defer func() { <-slots }()
	f()
}

// measure gets a worktree's size and git status, in parallel.
func measure(ctx context.Context, w wt.Worktree, l limits) record {
	r := record{Path: w.Path, Repo: w.RepoPath, RepoName: w.RepoName, Branch: w.Branch, Tool: w.Tool, State: w.State.String(), Locked: w.Locked}
	var usage *wt.Usage
	var info *wt.GitInfo
	gitErr := ""
	var wg sync.WaitGroup
	if w.State != wt.StateMissing {
		wg.Go(func() {
			withSlot(l.usage, func() {
				if u, err := wt.MeasureUsage(ctx, w.Path); err == nil {
					usage = &u
				}
			})
		})
	}

	wg.Go(func() {
		withSlot(l.git, func() {
			if i, err := wt.InspectGit(ctx, w); err == nil {
				info = &i
			} else {
				gitErr = "git error: " + err.Error()
			}
		})
	})

	wg.Wait()
	if usage != nil {
		r.SizeBytes = usage.Bytes
	}

	if info != nil {
		r.Changes, r.Unpushed = info.Changes, info.Unpushed
	}

	r.LastUsed = ui.LastUsed(usage, info)
	parts := ui.StatusParts(w, info)
	switch {
	case gitErr != "":
		r.status = gitErr
	case len(parts) == 0:
		r.status = "clean"
	default:
		r.status = strings.Join(parts, ", ")
	}

	return r
}
