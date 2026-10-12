// Package limit caps how many tasks run at once.
package limit

// Limit lets a fixed number of tasks run at a time.
type Limit chan struct{}

// New returns a Limit of n tasks at a time.
func New(n int) Limit { return make(Limit, n) }

// Do runs f, first waiting until fewer than the limit are running.
func (l Limit) Do(f func()) {
	l <- struct{}{}
	defer func() { <-l }()
	f()
}
