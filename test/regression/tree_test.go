// Package regression is Sapu's data-loss regression suite. It was written
// before the engine and is its specification: every test drives the real
// pipeline (scan.Scan -> engine.Plan -> engine.Apply -> fsop.Restore) against
// a fake home in t.TempDir() and checks that user data survives. Each test
// cites the real data-loss case it prevents.
//
// This file holds the fake-home builder and the folder snapshots.
package regression

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/env"
)

// fixtureTime is the mtime of every entry in a fake home. The odd nanoseconds
// catch code that copies files or rounds times instead of renaming.
var fixtureTime = time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)

// newHome creates a fake home in t.TempDir() holding files (path relative to
// the home -> contents; a path ending in "/" is an empty folder), with every
// mtime set to fixtureTime.
func newHome(t *testing.T, files map[string]string) env.Env {
	t.Helper()
	e := env.New(t.TempDir())
	build(t, e.Home, files)
	fixTimes(t, e.Home)
	return e
}

// homePath turns a slash-separated path relative to the home into an
// absolute path.
func homePath(e env.Env, rel string) string {
	return filepath.Join(e.Home, filepath.FromSlash(rel))
}

// relPath is the reverse of homePath.
func relPath(t *testing.T, e env.Env, p string) string {
	t.Helper()
	rel, err := filepath.Rel(e.Home, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		t.Fatalf("%s is not inside the fake home %s", p, e.Home)
	}
	return filepath.ToSlash(rel)
}

// display shows a path relative to the home when it is inside it.
func display(e env.Env, p string) string {
	rel, err := filepath.Rel(e.Home, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return p
	}
	return "~/" + filepath.ToSlash(rel)
}

func merge(sets ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, s := range sets {
		maps.Copy(out, s)
	}
	return out
}

func build(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, data := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			mkdirAll(t, p)
			continue
		}
		writeFile(t, p, data)
	}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, p, data string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, p, data string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		_ = f.Close() // the write error is the one to report
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, p, target string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(p))
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

func hardlink(t *testing.T, p, existing string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(p))
	if err := os.Link(existing, p); err != nil {
		t.Fatal(err)
	}
}

func chmod(t *testing.T, p string, mode fs.FileMode) {
	t.Helper()
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

// setXattr sets an extended attribute without following symlinks.
func setXattr(t *testing.T, p, name, value string) {
	t.Helper()
	if err := unix.Lsetxattr(p, name, []byte(value), 0); err != nil {
		t.Fatalf("set xattr %s on %s: %v", name, p, err)
	}
}

// removeAll deletes a path inside the test's own temp folder. AGENTS.md
// rule 1 allows this in tests only.
func removeAll(t *testing.T, p string) {
	t.Helper()
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
}

// fixTimes sets the access and modification time of every entry under root,
// symlinks included, to fixtureTime.
func fixTimes(t *testing.T, root string) {
	t.Helper()
	ts := unix.NsecToTimespec(fixtureTime.UnixNano())
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return unix.UtimesNanoAt(unix.AT_FDCWD, p, []unix.Timespec{ts, ts}, unix.AT_SYMLINK_NOFOLLOW)
	})
	if err != nil {
		t.Fatalf("set times under %s: %v", root, err)
	}
}

// entry is everything a snapshot records about one folder entry.
type entry struct {
	Type   string      // "dir", "file", "symlink" or "other"
	Mode   fs.FileMode // type and permission bits
	Size   int64       // files only
	Mtime  int64       // nanoseconds since 1970; 0 means "not compared"
	SHA256 string      // files only
	Xattrs string      // "name=hexvalue" pairs, sorted, space-separated
	Target string      // symlinks only
	Nlink  uint64      // files only
	inode  [2]uint64   // dev and inode: used for hard-link groups, never compared
}

// snapshot maps a slash-separated path relative to the snapshot root to its
// entry. The root itself is ".".
type snapshot map[string]entry

// takeSnapshot records the tree at root without following symlinks. A missing
// root gives an empty snapshot, so a comparison reports everything missing.
func takeSnapshot(t *testing.T, root string) snapshot {
	t.Helper()
	s := snapshot{}
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return s
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		en, err := readEntry(p)
		if err != nil {
			return err
		}
		s[filepath.ToSlash(rel)] = en
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot of %s: %v", root, err)
	}
	return s
}

// snapHome snapshots the fake home without the home folder entry itself and
// without Sapu's own state folder.
func snapHome(t *testing.T, e env.Env) snapshot {
	t.Helper()
	s := takeSnapshot(t, e.Home)
	delete(s, ".")
	s.removeTree(relPath(t, e, e.StateDir))
	return s
}

func readEntry(p string) (entry, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return entry{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return entry{}, fmt.Errorf("no stat data for %s", p)
	}
	xattrs, err := readXattrs(p)
	if err != nil {
		return entry{}, err
	}
	en := entry{
		Mode:   fi.Mode(),
		Mtime:  st.Mtimespec.Nano(),
		Xattrs: xattrs,
		inode:  [2]uint64{uint64(st.Dev), st.Ino},
	}
	switch {
	case fi.Mode().IsDir():
		en.Type = "dir"
	case fi.Mode()&fs.ModeSymlink != 0:
		en.Type = "symlink"
		if en.Target, err = os.Readlink(p); err != nil {
			return entry{}, err
		}
	case fi.Mode().IsRegular():
		en.Type = "file"
		en.Size = fi.Size()
		en.Nlink = uint64(st.Nlink)
		data, err := os.ReadFile(p)
		if err != nil {
			return entry{}, err
		}
		sum := sha256.Sum256(data)
		en.SHA256 = hex.EncodeToString(sum[:])
	default:
		en.Type = "other"
	}
	return en, nil
}

// readXattrs lists the extended attributes of p without following symlinks.
func readXattrs(p string) (string, error) {
	size, err := unix.Llistxattr(p, nil)
	if err != nil {
		return "", fmt.Errorf("list xattrs of %s: %w", p, err)
	}
	if size == 0 {
		return "", nil
	}
	buf := make([]byte, size)
	size, err = unix.Llistxattr(p, buf)
	if err != nil {
		return "", fmt.Errorf("list xattrs of %s: %w", p, err)
	}
	var pairs []string
	for _, name := range strings.Split(strings.TrimRight(string(buf[:size]), "\x00"), "\x00") {
		vsize, err := unix.Lgetxattr(p, name, nil)
		if err != nil {
			return "", fmt.Errorf("read xattr %s of %s: %w", name, p, err)
		}
		val := make([]byte, vsize)
		vsize, err = unix.Lgetxattr(p, name, val)
		if err != nil {
			return "", fmt.Errorf("read xattr %s of %s: %w", name, p, err)
		}
		pairs = append(pairs, name+"="+hex.EncodeToString(val[:vsize]))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, " "), nil
}

// sub returns the part of the snapshot under rel, with rel itself as ".".
func (s snapshot) sub(rel string) snapshot {
	out := snapshot{}
	for k, v := range s {
		switch {
		case k == rel:
			out["."] = v
		case strings.HasPrefix(k, rel+"/"):
			out[k[len(rel)+1:]] = v
		}
	}
	return out
}

// removeTree drops rel and everything under it.
func (s snapshot) removeTree(rel string) {
	for k := range s {
		if k == rel || strings.HasPrefix(k, rel+"/") {
			delete(s, k)
		}
	}
}

// clearMtime stops comparing the mtime of the given folders. Moving an entry
// out of a folder (or back in) always changes that folder's mtime.
func (s snapshot) clearMtime(rels ...string) {
	for _, rel := range rels {
		if en, ok := s[rel]; ok {
			en.Mtime = 0
			s[rel] = en
		}
	}
}

// setNlink records the expected link count of a file after one of its hard
// links was deleted for good.
func (s snapshot) setNlink(rel string, n uint64) {
	en := s[rel]
	en.Nlink = n
	s[rel] = en
}

// linkGroups maps every file that shares its inode with other entries of the
// snapshot to the sorted list of those entries.
func linkGroups(s snapshot) map[string]string {
	byInode := map[[2]uint64][]string{}
	for rel, en := range s {
		if en.Type == "file" && en.Nlink > 1 {
			byInode[en.inode] = append(byInode[en.inode], rel)
		}
	}
	groups := map[string]string{}
	for _, rels := range byInode {
		sort.Strings(rels)
		joined := "[" + strings.Join(rels, ", ") + "]"
		for _, rel := range rels {
			groups[rel] = joined
		}
	}
	return groups
}

// diffSnapshots lists every difference, one readable line each:
// "- path: missing", "+ path: unexpected" or "~ path: what changed".
func diffSnapshots(want, got snapshot) []string {
	wantLinks, gotLinks := linkGroups(want), linkGroups(got)
	keys := maps.Clone(want)
	maps.Copy(keys, got)
	var diffs []string
	for _, rel := range sortedKeys(keys) {
		w, inWant := want[rel]
		g, inGot := got[rel]
		if !inGot {
			diffs = append(diffs, fmt.Sprintf("- %s (%s): missing", rel, w.Type))
			continue
		}
		if !inWant {
			diffs = append(diffs, fmt.Sprintf("+ %s (%s): unexpected", rel, g.Type))
			continue
		}
		var changes []string
		field := func(name string, a, b any) {
			if a != b {
				changes = append(changes, fmt.Sprintf("%s %v -> %v", name, a, b))
			}
		}
		field("type", w.Type, g.Type)
		field("mode", w.Mode, g.Mode)
		field("size", w.Size, g.Size)
		field("mtime", formatTime(w.Mtime), formatTime(g.Mtime))
		if w.SHA256 != g.SHA256 {
			changes = append(changes, fmt.Sprintf("sha256 %.12s -> %.12s", w.SHA256, g.SHA256))
		}
		field("xattrs", "{"+w.Xattrs+"}", "{"+g.Xattrs+"}")
		field("symlink target", w.Target, g.Target)
		field("nlink", w.Nlink, g.Nlink)
		field("hard links", wantLinks[rel], gotLinks[rel])
		if len(changes) > 0 {
			diffs = append(diffs, fmt.Sprintf("~ %s: %s", rel, strings.Join(changes, "; ")))
		}
	}
	return diffs
}

func formatTime(ns int64) string {
	if ns == 0 {
		return "(not compared)"
	}
	return time.Unix(0, ns).UTC().Format(time.RFC3339Nano)
}

// requireSameTree fails the test with a readable diff when got differs from want.
func requireSameTree(t *testing.T, what string, want, got snapshot) {
	t.Helper()
	diffs := diffSnapshots(want, got)
	if len(diffs) == 0 {
		return
	}
	const limit = 40
	shown := diffs
	if len(shown) > limit {
		shown = append(shown[:limit:limit], fmt.Sprintf("... and %d more", len(diffs)-limit))
	}
	t.Errorf("%s changed (%d differences):\n    %s", what, len(diffs), strings.Join(shown, "\n    "))
}

// snapshotEach snapshots several folders of the home, keyed by their path
// relative to the home.
func snapshotEach(t *testing.T, e env.Env, rels []string) map[string]snapshot {
	t.Helper()
	out := map[string]snapshot{}
	for _, rel := range rels {
		s := takeSnapshot(t, homePath(e, rel))
		if len(s) == 0 {
			t.Fatalf("test setup: ~/%s does not exist", rel)
		}
		out[rel] = s
	}
	return out
}

// requireSurvived checks that every folder in before is still byte-identical:
// contents, mode, mtime, xattrs, symlink targets and hard links.
func requireSurvived(t *testing.T, e env.Env, before map[string]snapshot) {
	t.Helper()
	for _, rel := range sortedKeys(before) {
		requireSameTree(t, "~/"+rel, before[rel], takeSnapshot(t, homePath(e, rel)))
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
