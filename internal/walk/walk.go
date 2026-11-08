// Package walk measures folders. Rules for every function here:
//   - sizes are allocated bytes (st_blocks * 512), so sparse files such as
//     Docker.raw count what they really use
//   - a file with several hard links is counted once per walk
//   - symlinks are not followed; a symlink counts its own size only
//   - the walk does not cross into another volume
//   - unreadable entries are skipped and counted in Event.Errors, never fatal
//   - cancelling ctx stops the walk quickly and returns ctx.Err() together
//     with the partial result
//
// Implemented in Wave 1 (W1-B).
package walk

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	workers       = 8
	progressEvery = 100 * time.Millisecond
)

// Event reports progress. Values are running totals for the current walk.
type Event struct {
	Path   string // the entry being read
	Files  int64
	Bytes  int64
	Errors int64
}

// Progress is called from the walk; it may be nil. Calls are throttled
// (at most about 10 per second) and come from one goroutine at a time.
// The last call of a walk always carries the final totals.
type Progress func(Event)

// Node is one entry of a measured tree.
type Node struct {
	Name     string
	Path     string
	Bytes    int64 // allocated, recursive
	Files    int64 // number of files, recursive
	Errors   int64 // entries below that could not be read, recursive
	IsDir    bool
	Children []*Node // folders only; sorted by Bytes, largest first
}

// Size returns the allocated bytes and file count under path.
// A file with several hard links counts once, whichever link the walk meets first.
func Size(ctx context.Context, path string, progress Progress) (bytes, files int64, err error) {
	w := newWalker(ctx, progress)
	if err := w.run(path, false); err != nil {
		return 0, 0, err
	}
	return w.bytes.Load(), w.files.Load(), ctx.Err()
}

// Tree measures path and returns the full tree. Hard links count once, so the
// tree totals equal what Size reports.
func Tree(ctx context.Context, path string, progress Progress) (*Node, error) {
	w := newWalker(ctx, progress)
	if err := w.run(path, true); err != nil {
		return nil, err
	}
	total(w.root)
	return w.root, ctx.Err()
}

type linkKey struct{ dev, ino uint64 }

type job struct {
	path string
	node *Node // nil when no tree is wanted
}

// addErr counts a read error on the folder of the job. Only one worker
// touches j.node, so it needs no lock.
func (j job) addErr() {
	if j.node != nil {
		j.node.Errors++
	}
}

type walker struct {
	ctx      context.Context
	progress Progress
	dev      uint64 // the walk stays on this volume
	build    bool
	root     *Node

	bytes, files, errs atomic.Int64

	linkMu sync.Mutex
	links  map[linkKey]bool // files with several hard links, already counted

	progressMu sync.Mutex
	nextEvent  atomic.Int64 // unix nanoseconds; no event before this time

	queueMu sync.Mutex
	queueCV *sync.Cond
	stack   []job
	pending int // jobs queued or running
}

func newWalker(ctx context.Context, progress Progress) *walker {
	w := &walker{ctx: ctx, progress: progress, links: map[linkKey]bool{}}
	w.queueCV = sync.NewCond(&w.queueMu)
	return w
}

// run measures root. Only a root that cannot be examined is an error;
// problems further down are counted in w.errs.
func (w *walker) run(root string, build bool) error {
	fi, err := os.Lstat(root)
	if err != nil {
		return err
	}
	st, err := statOf(fi)
	if err != nil {
		return err
	}
	w.walkOn(root, fi, st, uint64(st.Dev), build)
	return nil
}

// walkOn measures root, staying on volume dev.
func (w *walker) walkOn(root string, fi os.FileInfo, st *syscall.Stat_t, dev uint64, build bool) {
	w.dev = dev
	w.build = build
	w.bytes.Add(st.Blocks * 512)
	w.root = &Node{Name: filepath.Base(root), Path: root, Bytes: st.Blocks * 512, IsDir: fi.IsDir()}

	if fi.IsDir() {
		w.push(job{path: root, node: w.root})
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w.work()
			}()
		}
		wg.Wait()
	} else {
		w.files.Add(1)
		w.root.Files = 1
	}
	w.emit(root, true)
}

func (w *walker) push(j job) {
	w.queueMu.Lock()
	w.stack = append(w.stack, j)
	w.pending++
	w.queueMu.Unlock()
	w.queueCV.Signal()
}

// pop waits for a job. It returns false when no job is left and none is running.
func (w *walker) pop() (job, bool) {
	w.queueMu.Lock()
	defer w.queueMu.Unlock()
	for len(w.stack) == 0 && w.pending > 0 {
		w.queueCV.Wait()
	}
	if len(w.stack) == 0 {
		return job{}, false
	}
	j := w.stack[len(w.stack)-1]
	w.stack = w.stack[:len(w.stack)-1]
	return j, true
}

func (w *walker) finish() {
	w.queueMu.Lock()
	w.pending--
	if w.pending == 0 {
		w.queueCV.Broadcast()
	}
	w.queueMu.Unlock()
}

func (w *walker) work() {
	for {
		j, ok := w.pop()
		if !ok {
			return
		}
		if w.ctx.Err() == nil {
			w.readDir(j)
		}
		w.finish()
	}
}

// readDir measures the entries of one folder. Sub-folders become new jobs.
// Only one worker touches j.node, so it needs no lock.
func (w *walker) readDir(j job) {
	f, err := os.Open(j.path)
	if err != nil {
		w.errs.Add(1)
		j.addErr()
		return
	}
	entries, err := f.ReadDir(-1)
	if err != nil {
		w.errs.Add(1) // keep the entries that were read
		j.addErr()
	}
	if err := f.Close(); err != nil {
		w.errs.Add(1)
		j.addErr()
	}

	for _, entry := range entries {
		if w.ctx.Err() != nil {
			return
		}
		path := filepath.Join(j.path, entry.Name())
		fi, err := entry.Info()
		if err != nil {
			w.errs.Add(1)
			j.addErr()
			continue
		}
		st, err := statOf(fi)
		if err != nil {
			w.errs.Add(1)
			j.addErr()
			continue
		}
		if uint64(st.Dev) != w.dev {
			continue // another volume
		}

		size := st.Blocks * 512
		child := &Node{Name: entry.Name(), Path: path, Bytes: size, IsDir: fi.IsDir()}
		if fi.IsDir() {
			w.bytes.Add(size)
			if w.build {
				j.node.Children = append(j.node.Children, child)
				w.push(job{path: path, node: child})
			} else {
				w.push(job{path: path})
			}
			continue
		}

		if st.Nlink > 1 && w.seenLink(st) {
			child.Bytes = 0 // already counted through another link
		} else {
			child.Files = 1
			w.files.Add(1)
			w.bytes.Add(size)
		}
		if w.build {
			j.node.Children = append(j.node.Children, child)
		}
	}
	w.emit(j.path, false)
}

// seenLink reports whether this file was met before, and remembers it.
func (w *walker) seenLink(st *syscall.Stat_t) bool {
	key := linkKey{uint64(st.Dev), st.Ino}
	w.linkMu.Lock()
	defer w.linkMu.Unlock()
	if w.links[key] {
		return true
	}
	w.links[key] = true
	return false
}

// emit sends a progress event, at most once per progressEvery.
// The final event is always sent.
func (w *walker) emit(path string, final bool) {
	if w.progress == nil {
		return
	}
	now := time.Now()
	if !final && now.UnixNano() < w.nextEvent.Load() {
		return
	}
	w.progressMu.Lock()
	defer w.progressMu.Unlock()
	if !final && now.UnixNano() < w.nextEvent.Load() {
		return
	}
	w.nextEvent.Store(now.Add(progressEvery).UnixNano())
	w.progress(Event{Path: path, Files: w.files.Load(), Bytes: w.bytes.Load(), Errors: w.errs.Load()})
}

func statOf(fi os.FileInfo) (*syscall.Stat_t, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, &os.PathError{Op: "stat", Path: fi.Name(), Err: syscall.EINVAL}
	}
	return st, nil
}

// total adds up the tree from the bottom and sorts every folder's children.
func total(n *Node) {
	for _, c := range n.Children {
		total(c)
		n.Bytes += c.Bytes
		n.Files += c.Files
		n.Errors += c.Errors
	}
	sort.SliceStable(n.Children, func(i, j int) bool {
		a, b := n.Children[i], n.Children[j]
		if a.Bytes != b.Bytes {
			return a.Bytes > b.Bytes
		}
		return a.Name < b.Name
	})
}
