package wt

import (
	"context"
	"sync"
)

// walkWorkers is how many directories one walk reads at once. Reading is
// almost all syscalls, and past a handful of workers they contend in the
// kernel instead of overlapping.
const walkWorkers = 8

// walk calls visit for every directory reachable from roots, from walkWorkers
// goroutines. visit reads a directory and calls push for each subdirectory to
// walk next. walk returns when every pushed directory is visited, or when ctx
// is cancelled.
func walk(ctx context.Context, roots []string, visit func(dir string, push func(string))) {
	w := &walker{}
	w.cond = sync.NewCond(&w.mu)
	// The stack is LIFO, so push in reverse to read the first root first.
	for i := len(roots) - 1; i >= 0; i-- {
		w.push(roots[i])
	}

	stop := context.AfterFunc(ctx, func() {
		w.mu.Lock()
		w.cond.Broadcast()
		w.mu.Unlock()
	})
	defer stop()

	var wg sync.WaitGroup
	for range walkWorkers {
		wg.Go(func() {
			for {
				dir, ok := w.pop(ctx)
				if !ok {
					return
				}

				visit(dir, w.push)
				w.done()
			}
		})
	}

	wg.Wait()
}

// walker is walk's shared stack of directories still to read.
type walker struct {
	mu      sync.Mutex
	cond    *sync.Cond
	stack   []string
	pending int
}

func (w *walker) push(dir string) {
	w.mu.Lock()
	w.stack = append(w.stack, dir)
	w.pending++
	w.mu.Unlock()
	w.cond.Signal()
}

func (w *walker) pop(ctx context.Context) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(w.stack) == 0 {
		if w.pending == 0 || ctx.Err() != nil {
			return "", false
		}

		w.cond.Wait()
	}

	if ctx.Err() != nil {
		return "", false
	}

	dir := w.stack[len(w.stack)-1]
	w.stack = w.stack[:len(w.stack)-1]
	return dir, true
}

func (w *walker) done() {
	w.mu.Lock()
	w.pending--
	finished := w.pending == 0
	w.mu.Unlock()
	if finished {
		w.cond.Broadcast()
	}
}
