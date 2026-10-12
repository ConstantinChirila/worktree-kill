package wt

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMeasureUsageLeavesOutHardLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("link counts aren't read on Windows; sizes include hard-linked files")
	}

	root := tempRoot(t)
	big := make([]byte, 64*1024)
	write(t, filepath.Join(root, "a"), string(big))
	before, err := MeasureUsage(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	// Like pnpm linking from its store: deleting the worktree frees nothing.
	store := tempRoot(t)
	if err := os.Link(filepath.Join(root, "a"), filepath.Join(store, "a")); err != nil {
		t.Fatal(err)
	}

	after, err := MeasureUsage(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	if before.Bytes < int64(len(big)) || after.Bytes >= int64(len(big)) {
		t.Errorf("before %d, after %d", before.Bytes, after.Bytes)
	}

	if after.LatestChange.IsZero() {
		t.Error("no mtime recorded")
	}
}

func TestMeasureUsageCountsNestedTree(t *testing.T) {
	root := tempRoot(t)
	const files = 60
	for i := range files {
		dir := filepath.Join(root, strings.Repeat("d/", i%5))
		write(t, filepath.Join(dir, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), string(make([]byte, 8192)))
	}

	usage, err := MeasureUsage(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	if usage.Bytes < files*8192 {
		t.Errorf("bytes = %d, want at least %d", usage.Bytes, files*8192)
	}
}

func TestNestedWorktreeIsNotCountedInItsParentsSize(t *testing.T) {
	root := tempRoot(t)
	repo := newRepo(t, root, "app")
	outer := filepath.Join(root, "outer")
	run(t, repo, "worktree", "add", "-q", "-b", "outer", outer)
	inner := filepath.Join(outer, ".claude", "worktrees", "inner")
	run(t, repo, "worktree", "add", "-q", "-b", "inner", inner)

	big := string(make([]byte, 256*1024))
	write(t, filepath.Join(inner, "big.bin"), big)
	before, err := MeasureUsage(context.Background(), outer)
	if err != nil {
		t.Fatal(err)
	}

	if before.Bytes >= int64(len(big)) {
		t.Errorf("outer counts the nested worktree: %d bytes", before.Bytes)
	}

	// A nested plain repo isn't listed on its own, so it still counts.
	plain := filepath.Join(outer, "vendor-clone")
	if err := os.MkdirAll(filepath.Join(plain, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(plain, "big.bin"), big)
	after, err := MeasureUsage(context.Background(), outer)
	if err != nil {
		t.Fatal(err)
	}

	if after.Bytes-before.Bytes < int64(len(big)) {
		t.Errorf("nested plain repo not counted: %d -> %d", before.Bytes, after.Bytes)
	}
}
