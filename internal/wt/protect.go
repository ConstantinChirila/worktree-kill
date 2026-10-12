package wt

import (
	"fmt"
	"path/filepath"
	"slices"
)

// Protection names folders that are never deleted outright. Orphans are the
// only worktrees deleted without git, so they are checked against it.
type Protection struct {
	// Paths are never deleted, and neither is any folder containing one.
	Paths []string
	// Home's direct children (like ~/Downloads) are never deleted either.
	Home string
}

// Protection returns the scan roots and home folder as a Protection.
func (o ScanOptions) Protection() Protection {
	paths := slices.Clone(o.Roots)
	if o.Home != "" {
		paths = append(paths, o.Home)
	}

	return Protection{Paths: paths, Home: o.Home}
}

// check returns why dir must not be deleted outright, or nil.
func (p Protection) check(dir string) error {
	if filepath.Dir(dir) == dir {
		return fmt.Errorf("refusing to delete %s: it is a filesystem root", dir)
	}

	for _, path := range p.Paths {
		if within(CanonicalPath(path), dir) {
			return fmt.Errorf("refusing to delete %s: it is or contains a scanned folder or your home folder", dir)
		}
	}

	if p.Home != "" && samePath(filepath.Dir(dir), CanonicalPath(p.Home)) {
		return fmt.Errorf("refusing to delete %s: it sits directly in your home folder", dir)
	}

	return nil
}
