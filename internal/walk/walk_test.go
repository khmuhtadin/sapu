package walk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// blocks returns the allocated bytes of one entry, without following symlinks.
func blocks(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Blocks * 512
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lastEvent returns a Progress that remembers the last event it saw.
func lastEvent() (Progress, func() Event) {
	var last atomic.Pointer[Event]
	return func(e Event) { last.Store(&e) }, func() Event {
		if e := last.Load(); e != nil {
			return *e
		}
		return Event{}
	}
}

func TestSizeCountsAllocatedBytesOfSparseFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sparse.raw")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const apparent = 1 << 30
	if _, err := f.Seek(apparent, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, files, err := Size(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := blocks(t, path) + blocks(t, root); got != want {
		t.Errorf("Size = %d, want allocated bytes %d", got, want)
	}
	if got >= apparent/10 {
		t.Errorf("Size = %d looks like the apparent size %d, not allocated bytes", got, apparent)
	}
	if files != 1 {
		t.Errorf("files = %d, want 1", files)
	}
}

func TestSizeCountsHardLinksOnce(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	writeFile(t, a, 1<<20)
	for _, name := range []string{"b", "c"} {
		if err := os.Link(a, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(root, "sub")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, filepath.Join(other, "d")); err != nil {
		t.Fatal(err)
	}

	got, files, err := Size(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := blocks(t, a) + blocks(t, root) + blocks(t, other)
	if got != want {
		t.Errorf("Size = %d, want %d (one copy of the file)", got, want)
	}
	if files != 1 {
		t.Errorf("files = %d, want 1", files)
	}
}

func TestSizeDoesNotFollowSymlinks(t *testing.T) {
	big := t.TempDir()
	writeFile(t, filepath.Join(big, "big.bin"), 1<<20)

	root := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(big, link); err != nil {
		t.Fatal(err)
	}

	got, files, err := Size(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := blocks(t, root) + blocks(t, link); got != want {
		t.Errorf("Size = %d, want %d (the link itself)", got, want)
	}
	if files != 1 {
		t.Errorf("files = %d, want 1 (the link)", files)
	}

	// A symlink given as the start path is measured as a link too.
	got, files, err = Size(context.Background(), link, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := blocks(t, link); got != want || files != 1 {
		t.Errorf("Size(link) = %d, %d files; want %d, 1", got, files, want)
	}
}

func TestSizeCountsUnreadableFolderAsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ok", "file"), 1<<16)
	writeFile(t, filepath.Join(root, "locked", "inner"), 1<<20)
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	progress, last := lastEvent()
	got, files, err := Size(context.Background(), root, progress)
	if err != nil {
		t.Fatalf("an unreadable folder must not fail the walk: %v", err)
	}
	if files != 1 {
		t.Errorf("files = %d, want 1 (inner is unreadable)", files)
	}
	if limit := int64(1 << 20); got >= limit {
		t.Errorf("Size = %d includes the unreadable folder", got)
	}
	if e := last(); e.Errors != 1 || e.Files != files || e.Bytes != got {
		t.Errorf("final event = %+v, want 1 error and totals %d bytes, %d files", e, got, files)
	}
}

func TestSizeMissingPathIsAnError(t *testing.T) {
	_, _, err := Size(context.Background(), filepath.Join(t.TempDir(), "nope"), nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want not-exist", err)
	}
	if _, err := Tree(context.Background(), filepath.Join(t.TempDir(), "nope"), nil); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Tree err = %v, want not-exist", err)
	}
}

func TestSizeOfSingleFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	writeFile(t, path, 10000)
	got, files, err := Size(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := blocks(t, path); got != want || files != 1 {
		t.Errorf("Size = %d, %d files; want %d, 1", got, files, want)
	}
}

func TestSizeStaysOnTheStartVolume(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "sub", "file"), 1<<16)
	writeFile(t, filepath.Join(root, "top"), 1<<16)

	fi, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)

	// Pretend the start volume is a different device: every entry is then on
	// "another volume" and must be skipped.
	w := newWalker(context.Background(), nil)
	w.walkOn(root, fi, st, uint64(st.Dev)+1, false)
	if got := w.files.Load(); got != 0 {
		t.Errorf("counted %d files on another volume", got)
	}
	if got, want := w.bytes.Load(), st.Blocks*512; got != want {
		t.Errorf("bytes = %d, want only the start folder (%d)", got, want)
	}
}

// makeFiles creates dirs folders with filesPerDir empty files each.
func makeFiles(t *testing.T, root string, dirs, filesPerDir int) {
	t.Helper()
	for d := range dirs {
		dir := filepath.Join(root, fmt.Sprintf("d%03d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := range filesPerDir {
			f, err := os.Create(filepath.Join(dir, fmt.Sprintf("f%04d", i)))
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCancelReturnsQuicklyWithPartialResult(t *testing.T) {
	const dirs, perDir = 100, 1000
	root := t.TempDir()
	makeFiles(t, root, dirs, perDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cancelledAt atomic.Int64
	progress := func(Event) {
		cancelledAt.CompareAndSwap(0, time.Now().UnixNano())
		cancel()
	}

	_, files, err := Size(ctx, root, progress)
	elapsed := time.Since(time.Unix(0, cancelledAt.Load()))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if cancelledAt.Load() == 0 {
		t.Fatal("no progress event before the walk ended")
	}
	if elapsed > time.Second {
		t.Errorf("walk took %v to stop after cancel", elapsed)
	}
	if files >= dirs*perDir {
		t.Errorf("counted all %d files; cancel did not stop the walk", files)
	}

	node, err := Tree(ctx, root, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Tree err = %v, want context.Canceled", err)
	}
	if node == nil {
		t.Error("Tree should return the partial tree")
	}
}

func TestProgressIsThrottledAndSerial(t *testing.T) {
	root := t.TempDir()
	makeFiles(t, root, 100, 200)

	var events, inside atomic.Int64
	overlap := false
	progress := func(Event) {
		if inside.Add(1) != 1 {
			overlap = true
		}
		events.Add(1)
		time.Sleep(time.Millisecond)
		inside.Add(-1)
	}
	start := time.Now()
	_, files, err := Size(context.Background(), root, progress)
	if err != nil {
		t.Fatal(err)
	}
	if files != 100*200 {
		t.Errorf("files = %d, want %d", files, 100*200)
	}
	if overlap {
		t.Error("Progress was called from two goroutines at once")
	}
	limit := int64(time.Since(start).Seconds()*10) + 3 // about 10 a second, plus first and final
	if got := events.Load(); got < 2 || got > limit {
		t.Errorf("%d events in %v, want between 2 and %d", got, time.Since(start), limit)
	}
}

func TestTreeTotalsEqualSize(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "small"), 100)
	writeFile(t, filepath.Join(root, "a", "medium"), 1<<16)
	writeFile(t, filepath.Join(root, "a", "b", "big"), 1<<20)
	writeFile(t, filepath.Join(root, "a", "b", "c", "other"), 5000)
	writeFile(t, filepath.Join(root, "empty-dir-sibling", "x"), 0)
	if err := os.Link(filepath.Join(root, "a", "b", "big"), filepath.Join(root, "a", "big-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	bytes, files, err := Size(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := Tree(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Bytes != bytes || tree.Files != files {
		t.Errorf("Tree = %d bytes, %d files; Size = %d bytes, %d files", tree.Bytes, tree.Files, bytes, files)
	}
	if !tree.IsDir || tree.Path != root || tree.Name != filepath.Base(root) {
		t.Errorf("unexpected root node: %+v", tree)
	}
	checkNode(t, tree)

	// The big file shows up under one of its two links, never both.
	counted := 0
	var find func(n *Node)
	find = func(n *Node) {
		if (n.Name == "big" || n.Name == "big-link") && n.Bytes > 0 {
			counted++
		}
		for _, c := range n.Children {
			find(c)
		}
	}
	find(tree)
	if counted != 1 {
		t.Errorf("big file counted through %d links, want 1", counted)
	}
}

// checkNode verifies a folder's totals and order, recursively.
func checkNode(t *testing.T, n *Node) {
	t.Helper()
	if !n.IsDir {
		if len(n.Children) != 0 {
			t.Errorf("%s: a file has children", n.Path)
		}
		return
	}
	own := blocks(t, n.Path)
	var sumBytes, sumFiles int64
	for i, c := range n.Children {
		sumBytes += c.Bytes
		sumFiles += c.Files
		if i > 0 && n.Children[i-1].Bytes < c.Bytes {
			t.Errorf("%s: children not sorted by size", n.Path)
		}
		checkNode(t, c)
	}
	if n.Bytes != own+sumBytes || n.Files != sumFiles {
		t.Errorf("%s: %d bytes, %d files; children add up to %d, %d (+%d own)", n.Path, n.Bytes, n.Files, sumBytes, sumFiles, own)
	}
}

func TestTreeCountsUnreadableEntriesPerFolder(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a/locked/f", "a/open/f", "b/f"} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(root, "a", "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	tree, err := Tree(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	errs := map[string]int64{}
	var visit func(n *Node)
	visit = func(n *Node) {
		rel, _ := filepath.Rel(root, n.Path)
		errs[rel] = n.Errors
		for _, c := range n.Children {
			visit(c)
		}
	}
	visit(tree)
	if errs["a/locked"] != 1 || errs["a"] != 1 || errs["."] != 1 || errs["b"] != 0 || errs["a/open"] != 0 {
		t.Errorf("errors per folder %v", errs)
	}
}
