package fsop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

// entry is everything undo must bring back for one path.
type entry struct {
	Mode    fs.FileMode
	Size    int64
	MtimeNs int64
	Ino     uint64 // rename keeps inodes, so equal inodes also prove equal hard-link groups
	Nlink   uint16
	Sum     string // sha256 of a regular file
	Link    string // symlink target
	Xattrs  map[string]string
}

// snapshot records every path below root (root itself is left out: moving
// its children changes its mtime).
func snapshot(t *testing.T, root string) map[string]entry {
	t.Helper()
	out := map[string]entry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st := fi.Sys().(*syscall.Stat_t)
		en := entry{
			Mode:    fi.Mode(),
			Size:    fi.Size(),
			MtimeNs: st.Mtimespec.Nano(),
			Ino:     st.Ino,
			Nlink:   st.Nlink,
			Xattrs:  xattrs(t, p),
		}
		switch {
		case fi.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			en.Sum = hex.EncodeToString(sum[:])
		case fi.Mode()&fs.ModeSymlink != 0:
			if en.Link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		out[rel] = en
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func xattrs(t *testing.T, p string) map[string]string {
	t.Helper()
	size, err := unix.Llistxattr(p, nil)
	if err != nil {
		t.Fatalf("listxattr %s: %v", p, err)
	}
	if size == 0 {
		return nil
	}
	buf := make([]byte, size)
	if size, err = unix.Llistxattr(p, buf); err != nil {
		t.Fatalf("listxattr %s: %v", p, err)
	}
	out := map[string]string{}
	for _, name := range strings.Split(strings.TrimRight(string(buf[:size]), "\x00"), "\x00") {
		n, err := unix.Lgetxattr(p, name, nil)
		if err != nil {
			t.Fatalf("getxattr %s %s: %v", p, name, err)
		}
		val := make([]byte, n)
		if n, err = unix.Lgetxattr(p, name, val); err != nil {
			t.Fatalf("getxattr %s %s: %v", p, name, err)
		}
		out[name] = string(val[:n])
	}
	return out
}

func requireSame(t *testing.T, want, got map[string]entry) {
	t.Helper()
	var diffs []string
	for p, w := range want {
		g, ok := got[p]
		if !ok {
			diffs = append(diffs, "missing "+p)
			continue
		}
		if !reflect.DeepEqual(w, g) {
			diffs = append(diffs, fmt.Sprintf("%s:\n  want %+v\n  got  %+v", p, w, g))
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			diffs = append(diffs, "extra "+p)
		}
	}
	if len(diffs) > 0 {
		sort.Strings(diffs)
		t.Fatalf("%d differences, first ones:\n%s", len(diffs), strings.Join(diffs[:min(10, len(diffs))], "\n"))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// newHome returns a test Env whose home is a temp folder. Read-only folders
// are made writable again before the temp folder is removed.
func newHome(t *testing.T) env.Env {
	t.Helper()
	home := t.TempDir()
	t.Cleanup(func() { makeWritable(home) })
	return env.New(home)
}

func makeWritable(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
}

// buildTree fills root with items: folder trees, single files and symlinks.
// It makes at least `files` regular files, with varied modes and mtimes,
// xattrs, read-only folders, symlinks (some dangling) and hard links, also
// across items. It returns the number of regular files made.
func buildTree(t *testing.T, root string, items, files int, seed uint64) int {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 7))
	must(t, os.MkdirAll(root, 0o755))
	modes := []fs.FileMode{0o600, 0o644, 0o755, 0o400, 0o640}
	var folderItems, singles int
	for i := 0; i < items; i++ {
		switch i % 5 {
		case 2:
			singles++
		case 3:
		default:
			folderItems++
		}
	}
	perItem := max(1, (files-singles+folderItems-1)/max(1, folderItems))

	made := 0
	var regular []string
	var readOnly []string
	write := func(p string) {
		data := make([]byte, rng.IntN(2048))
		for i := range data {
			data[i] = byte(rng.Uint32())
		}
		must(t, os.WriteFile(p, data, 0o644))
		if made%10 == 0 {
			must(t, unix.Setxattr(p, "com.sapu.test", []byte(fmt.Sprintf("v%d", made)), 0))
		}
		must(t, os.Chmod(p, modes[rng.IntN(len(modes))]))
		regular = append(regular, p)
		made++
	}

	for i := 0; i < items; i++ {
		p := filepath.Join(root, fmt.Sprintf("item%03d", i))
		switch i % 5 {
		case 2: // a single file
			write(p)
		case 3: // a symlink, dangling every other time
			target := fmt.Sprintf("item%03d", (i+1)%items)
			if i%2 == 0 {
				target = "no-such-target"
			}
			must(t, os.Symlink(target, p))
			must(t, unix.Lsetxattr(p, "com.sapu.link", []byte("l"), 0))
		default: // a folder tree
			for f := 0; f < perItem; f++ {
				dir := filepath.Join(p, fmt.Sprintf("d%d", f%7), fmt.Sprintf("e%d", f%3))
				must(t, os.MkdirAll(dir, 0o755))
				write(filepath.Join(dir, fmt.Sprintf("f%04d.bin", f)))
			}
			must(t, unix.Setxattr(p, "com.sapu.dir", []byte(p[len(root):]), 0))
			must(t, os.Symlink("d0/e0", filepath.Join(p, "rel-link")))
			must(t, os.Symlink(filepath.Join(root, "outside-target"), filepath.Join(p, "abs-link")))
			// hard links inside the item and to a file of an earlier item
			must(t, os.Link(regular[len(regular)-1], filepath.Join(p, "hard-inside")))
			if len(regular) > perItem+1 {
				must(t, os.Link(regular[rng.IntN(len(regular)-perItem)], filepath.Join(p, "hard-across")))
			}
			if i%5 == 1 {
				readOnly = append(readOnly, filepath.Join(p, "d1"), filepath.Join(p, "d1", "e1"))
			}
		}
	}
	for _, d := range readOnly {
		if _, err := os.Lstat(d); err == nil {
			must(t, os.Chmod(d, 0o555))
		}
	}

	// Random mtimes, children before their folders.
	var paths []string
	must(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != root && d.Type()&fs.ModeSymlink == 0 {
			paths = append(paths, p)
		}
		return nil
	}))
	sort.Slice(paths, func(i, j int) bool { return strings.Count(paths[i], "/") > strings.Count(paths[j], "/") })
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, p := range paths {
		mt := base.Add(time.Duration(rng.Int64N(int64(300 * 24 * time.Hour))))
		must(t, os.Chtimes(p, mt, mt))
	}
	return made
}

// itemsFor makes one plan item for every entry directly inside root.
func itemsFor(t *testing.T, root string) []plan.Item {
	t.Helper()
	des, err := os.ReadDir(root)
	must(t, err)
	var items []plan.Item
	for i, de := range des {
		items = append(items, itemFor(t, fmt.Sprintf("%04d", i+1), filepath.Join(root, de.Name()), 100))
	}
	return items
}

func itemFor(t *testing.T, id, path string, bytes int64) plan.Item {
	t.Helper()
	ident, err := plan.IdentityOf(path)
	must(t, err)
	return plan.Item{ID: id, Source: "test", Path: path, Bytes: bytes, Action: "quarantine", Identity: ident, Selected: true}
}

func startRun(t *testing.T, e env.Env, items []plan.Item, now time.Time) *runlog.Run {
	t.Helper()
	p := &plan.Plan{SchemaVersion: plan.SchemaVersion, Kind: plan.KindClean, CreatedAt: now, Home: e.Home, Items: items}
	run, err := runlog.Start(e, p, now)
	must(t, err)
	return run
}

// quarantineAll quarantines every item and closes the run with an end event.
func quarantineAll(t *testing.T, e env.Env, items []plan.Item, now time.Time) string {
	t.Helper()
	run := startRun(t, e, items, now)
	for _, it := range items {
		must(t, Quarantine(e, run, it))
	}
	must(t, run.Close(now, true))
	return run.ID
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte(content), 0o644))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	return string(data)
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return false
}

func summary(t *testing.T, e env.Env, id string) runlog.Summary {
	t.Helper()
	runs, err := runlog.List(e)
	must(t, err)
	for _, r := range runs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("run %s not listed", id)
	return runlog.Summary{}
}

func journalTypes(t *testing.T, e env.Env, id string) []string {
	t.Helper()
	evs, err := runlog.Events(e, id)
	must(t, err)
	var types []string
	for _, ev := range evs {
		types = append(types, ev.Type)
	}
	return types
}

// setHook makes afterStep fail with errCrash on its k-th call.
func setHook(t *testing.T, k int) {
	t.Helper()
	n := 0
	afterStep = func() error {
		n++
		if n == k {
			return errCrash
		}
		return nil
	}
	t.Cleanup(func() { afterStep = func() error { return nil } })
}

var errCrash = fmt.Errorf("simulated crash")

// appendTorn adds half a journal line, as a crash in the middle of a write would.
func appendTorn(t *testing.T, e env.Env, id string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(e.RunsDir(), id, "journal.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
	must(t, err)
	_, err = f.Write([]byte(`{"time":"2026-11-08T11:15:00Z","type":"move_be`))
	must(t, err)
	must(t, f.Close())
}
