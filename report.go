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
	var mu sync.Mutex
	var worktrees []wt.Worktree
	wt.NewScanner(opts, func(w wt.Worktree) {
		mu.Lock()
		worktrees = append(worktrees, w)
		mu.Unlock()
	}).Run(ctx)

	records := make([]record, len(worktrees))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for i, w := range worktrees {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			records[i] = measure(ctx, w)
		})
	}

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

func measure(ctx context.Context, w wt.Worktree) record {
	r := record{Path: w.Path, Repo: w.RepoPath, RepoName: w.RepoName, Branch: w.Branch, Tool: w.Tool, State: w.State.String(), Locked: w.Locked}
	var usage *wt.Usage
	if w.State != wt.StateMissing {
		if u, err := wt.MeasureUsage(ctx, w.Path); err == nil {
			usage = &u
			r.SizeBytes = u.Bytes
		}
	}

	var info *wt.GitInfo
	gitErr := ""
	if i, err := wt.InspectGit(ctx, w); err == nil {
		info = &i
		r.Changes, r.Unpushed = i.Changes, i.Unpushed
	} else {
		gitErr = "git error: " + err.Error()
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
