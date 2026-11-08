// Package trends keeps one small size snapshot of the home folder per day
// (blueprint D3) and compares two of them to show which folders grew.
//
// A snapshot is ~/.sapu/trends/<YYYY-MM-DD>.json. It holds the home total and
// every folder up to three levels below home that uses at least MinBytes.
// Folders under the state folder are left out: they are Sapu's own.
//
// Rules:
//  1. Taking a snapshot only reads the home folder. The one thing written is
//     the snapshot file, in place with os.WriteFile (never a rename).
//  2. A second snapshot on the same day replaces the first.
//  3. Nothing here deletes a file. Only internal/fsop may do that, so old
//     snapshots are kept; each one is a few KB (see docs/reporting.md).
//  4. A folder missing from a snapshot used less than the snapshot's
//     MinBytes, and a comparison says so instead of inventing a number.
package trends

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/safefile"
	"github.com/khmuhtadin/sapu/internal/walk"
)

const (
	// MinBytes is the smallest folder a snapshot lists, base 1000 as Sapu prints sizes.
	MinBytes = 100_000_000
	// MaxDepth is how deep below home a snapshot looks: ~/Library/Caches/CapCut is 3.
	MaxDepth = 3

	// minChange is the smallest change a comparison reports. Caches move by
	// a few MB all the time and that is not news.
	minChange = 10_000_000
	// explainedShare is how much of a folder's change one of its sub-folders
	// must carry for the sub-folder to be reported instead of the folder.
	explainedShare = 0.75

	dateLayout = "2006-01-02"
)

// Folder is one folder of a snapshot.
type Folder struct {
	Path  string `json:"path"`  // relative to home, with slashes: "Library/Caches/CapCut"
	Bytes int64  `json:"bytes"` // allocated bytes, recursive
	// Errors counts entries below that Sapu could not read. Diff compares a
	// folder only when this count is the same in both snapshots: a folder
	// that became unreadable must not look like it shrank.
	Errors int64 `json:"errors,omitempty"`
}

// Snapshot is the sizes on one day.
type Snapshot struct {
	Date      string    `json:"date"`       // YYYY-MM-DD, local time
	Taken     time.Time `json:"taken"`      // when the walk finished
	HomeBytes int64     `json:"home_bytes"` // the whole home folder
	MinBytes  int64     `json:"min_bytes"`  // folders smaller than this are not listed
	Folders   []Folder  `json:"folders"`    // largest first
	// Unmeasured lists the parts (relative to home) that did not answer in
	// time, or whose top folder could not be read at all, and are left out
	// of every total, e.g. "Library/Mobile Documents".
	Unmeasured []string `json:"unmeasured,omitempty"`
}

// Dir is the folder of the snapshots.
func Dir(e env.Env) string { return filepath.Join(e.StateDir, "trends") }

func path(e env.Env, date string) string { return filepath.Join(Dir(e), date+".json") }

// Date is the snapshot name of a time.
func Date(t time.Time) string { return t.Format(dateLayout) }

// Has reports whether a snapshot of today exists.
func Has(e env.Env, now time.Time) bool {
	_, err := os.Stat(path(e, Date(now)))
	return err == nil
}

// PartTimeout limits the walk of one part of the home folder. A folder that
// never answers (a stuck iCloud Drive placeholder, for example) blocks a
// system call that cannot be cancelled; that part is then left out of the
// snapshot and listed in Snapshot.Unmeasured instead of failing the whole
// snapshot.
var PartTimeout = time.Minute

// walkTree is walk.Tree; tests replace it to stand in for a folder that
// never answers.
var walkTree = walk.Tree

// Record measures the home folder and saves the snapshot of now's date.
// It walks the entries of home, and of home/Library, one by one, each with
// PartTimeout. It saves nothing when ctx is cancelled.
func Record(ctx context.Context, e env.Env, now time.Time, progress walk.Progress) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	root := &walk.Node{Name: filepath.Base(e.Home), Path: e.Home, IsDir: true}
	var unmeasured []string
	entries, err := os.ReadDir(e.Home)
	if err != nil {
		return Snapshot{}, err
	}
	for _, de := range entries {
		p := filepath.Join(e.Home, de.Name())
		if de.Name() == "Library" && de.IsDir() {
			lib := &walk.Node{Name: "Library", Path: p, IsDir: true}
			kids, err := os.ReadDir(p)
			if err != nil {
				return Snapshot{}, err
			}
			for _, k := range kids {
				n, ok, err := measurePart(ctx, filepath.Join(p, k.Name()), progress)
				if err != nil {
					return Snapshot{}, err
				}
				if !ok {
					unmeasured = append(unmeasured, "Library/"+k.Name())
					continue
				}
				if n != nil {
					lib.Children = append(lib.Children, n)
				}
			}
			addUp(lib)
			root.Children = append(root.Children, lib)
			continue
		}
		n, ok, err := measurePart(ctx, p, progress)
		if err != nil {
			return Snapshot{}, err
		}
		if !ok {
			unmeasured = append(unmeasured, de.Name())
			continue
		}
		if n != nil {
			root.Children = append(root.Children, n)
		}
	}
	addUp(root)
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s := FromTree(root, e, now)
	s.Unmeasured = unmeasured
	return s, Save(e, s)
}

// measurePart walks one part with PartTimeout. ok is false when the part did
// not answer in time; n is nil when it vanished or cannot be read.
func measurePart(ctx context.Context, path string, progress walk.Progress) (n *walk.Node, ok bool, err error) {
	tctx, cancel := context.WithTimeout(ctx, PartTimeout)
	defer cancel()
	type result struct {
		tree *walk.Node
		err  error
	}
	done := make(chan result, 1) // buffered: a late answer must not block the goroutine
	walkFn := walkTree
	go func() {
		tree, err := walkFn(tctx, path, progress)
		done <- result{tree, err}
	}()
	select {
	case res := <-done:
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if tctx.Err() != nil {
			return nil, false, nil
		}
		if errors.Is(res.err, fs.ErrNotExist) {
			return nil, true, nil // gone: nothing to count
		}
		if res.err != nil {
			return nil, false, nil // its top folder cannot be read: unmeasured
		}
		return res.tree, true, nil
	case <-tctx.Done():
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
}

// addUp sets a folder's totals from its children.
func addUp(n *walk.Node) {
	n.Bytes, n.Files = 0, 0
	for _, c := range n.Children {
		n.Bytes += c.Bytes
		n.Files += c.Files
	}
}

// FromTree builds a snapshot from a measured home folder.
func FromTree(tree *walk.Node, e env.Env, now time.Time) Snapshot {
	s := Snapshot{Date: Date(now), Taken: now, HomeBytes: tree.Bytes, MinBytes: MinBytes, Folders: []Folder{}}
	var add func(n *walk.Node, depth int)
	add = func(n *walk.Node, depth int) {
		for _, c := range n.Children {
			if !c.IsDir || c.Bytes < MinBytes || c.Path == e.StateDir {
				continue
			}
			rel, err := filepath.Rel(tree.Path, c.Path)
			if err != nil {
				continue
			}
			s.Folders = append(s.Folders, Folder{Path: filepath.ToSlash(rel), Bytes: c.Bytes, Errors: c.Errors})
			if depth < MaxDepth {
				add(c, depth+1)
			}
		}
	}
	add(tree, 1)
	sort.SliceStable(s.Folders, func(i, j int) bool { return s.Folders[i].Bytes > s.Folders[j].Bytes })
	return s
}

// Save writes the snapshot, replacing one of the same date.
func Save(e env.Env, s Snapshot) error {
	if _, err := time.Parse(dateLayout, s.Date); err != nil {
		return fmt.Errorf("snapshot date %q: %w", s.Date, err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(e), 0o700); err != nil {
		return err
	}
	// Never through a symlink or a hard link: the M2 review showed a
	// snapshot path linked to a document would otherwise overwrite it.
	return safefile.Write(e.StateDir, path(e, s.Date), append(b, '\n'), 0o600)
}

// Load reads the snapshot of one date.
func Load(e env.Env, date string) (Snapshot, error) {
	var s Snapshot
	b, err := os.ReadFile(path(e, date))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, fmt.Errorf("%s: %w", path(e, date), err)
	}
	s.Date = date
	return s, nil
}

// Dates lists the dates that have a snapshot, newest first. A folder that
// does not exist yet has none.
func Dates(e env.Env) ([]string, error) {
	entries, err := os.ReadDir(Dir(e))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var dates []string
	for _, de := range entries {
		date, ok := strings.CutSuffix(de.Name(), ".json")
		if _, err := time.Parse(dateLayout, date); ok && err == nil && !de.IsDir() {
			dates = append(dates, date)
		}
	}
	slices.Sort(dates)
	slices.Reverse(dates)
	return dates, nil
}

// Newest returns the newest snapshot; ok is false when there is none.
func Newest(e env.Env) (s Snapshot, ok bool, err error) {
	dates, err := Dates(e)
	if err != nil || len(dates) == 0 {
		return Snapshot{}, false, err
	}
	s, err = Load(e, dates[0])
	return s, err == nil, err
}

// Pick chooses the two snapshots to compare: the newest, and the newest
// that is at least days days older. dates is newest first, as Dates gives it.
// ok is false when there is no such pair.
func Pick(dates []string, days int) (newer, older string, ok bool) {
	if len(dates) == 0 {
		return "", "", false
	}
	newest, err := time.Parse(dateLayout, dates[0])
	if err != nil {
		return "", "", false
	}
	cutoff := newest.AddDate(0, 0, -days)
	for _, d := range dates[1:] {
		t, err := time.Parse(dateLayout, d)
		if err == nil && !t.After(cutoff) {
			return dates[0], d, true
		}
	}
	return "", "", false
}

// Change is how one folder changed between two snapshots.
type Change struct {
	Path   string `json:"path"`   // relative to home
	Before int64  `json:"before"` // bytes in the older snapshot; 0 when it was under the minimum
	After  int64  `json:"after"`  // bytes in the newer snapshot; 0 when it is under the minimum
	Delta  int64  `json:"delta"`  // After - Before; an estimate when Approx
	// Approx is true when the folder is missing from one snapshot: it used
	// less than MinBytes then, so the real change is somewhat smaller.
	Approx bool `json:"approx"`
}

// Diff is the comparison of two snapshots.
type Diff struct {
	// Unmeasured are parts left out of one of the two snapshots; changes in
	// them, and in the folders that hold them, are not reported.
	Unmeasured []string `json:"unmeasured,omitempty"`
	// Unreadable are the top folders whose count of unreadable entries
	// changed between the snapshots, so their sizes are not compared.
	Unreadable []string `json:"unreadable,omitempty"`
	Older      string   `json:"older"` // dates
	Newer      string   `json:"newer"`
	Growth     []Change `json:"growth"` // biggest growth first
	Shrink     []Change `json:"shrink"` // biggest shrink first
	// HomeBefore and HomeAfter are the home totals.
	HomeBefore int64 `json:"home_before"`
	HomeAfter  int64 `json:"home_after"`
}

// Compare lists the biggest growth and shrink, at most limit of each. It
// reports the deepest folder that explains a change: when one sub-folder
// carries at least three quarters of its parent's change, only the
// sub-folder is listed.
func Compare(newer, older Snapshot, limit int) Diff {
	d := Diff{Older: older.Date, Newer: newer.Date, HomeBefore: older.HomeBytes, HomeAfter: newer.HomeBytes,
		Growth: []Change{}, Shrink: []Change{}}
	before := map[string]int64{}
	after := map[string]int64{}
	errsBefore := map[string]int64{}
	errsAfter := map[string]int64{}
	for _, f := range older.Folders {
		before[f.Path] = f.Bytes
		errsBefore[f.Path] = f.Errors
	}
	for _, f := range newer.Folders {
		after[f.Path] = f.Bytes
		errsAfter[f.Path] = f.Errors
	}
	changes := map[string]Change{}
	for p := range before {
		changes[p] = change(p, before, after)
	}
	for p := range after {
		changes[p] = change(p, before, after)
	}

	// Sorted paths keep equal changes in a stable order.
	paths := make([]string, 0, len(changes))
	for p := range changes {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	skipped := slices.Concat(newer.Unmeasured, older.Unmeasured)
	slices.Sort(skipped)
	d.Unmeasured = slices.Compact(skipped)
	for _, p := range paths {
		if errsBefore[p] != errsAfter[p] {
			d.Unreadable = append(d.Unreadable, p)
		}
	}
	d.Unreadable = topmost(d.Unreadable)
	for _, p := range paths {
		c := changes[p]
		if abs(c.Delta) < minChange || explainedBelow(c, changes) || touches(p, d.Unmeasured) || errsBefore[p] != errsAfter[p] {
			continue
		}
		if c.Delta > 0 {
			d.Growth = append(d.Growth, c)
		} else {
			d.Shrink = append(d.Shrink, c)
		}
	}
	sort.SliceStable(d.Growth, func(i, j int) bool { return d.Growth[i].Delta > d.Growth[j].Delta })
	sort.SliceStable(d.Shrink, func(i, j int) bool { return d.Shrink[i].Delta < d.Shrink[j].Delta })
	d.Growth = d.Growth[:min(limit, len(d.Growth))]
	d.Shrink = d.Shrink[:min(limit, len(d.Shrink))]
	return d
}

func change(p string, before, after map[string]int64) Change {
	b, hadB := before[p]
	a, hadA := after[p]
	return Change{Path: p, Before: b, After: a, Delta: a - b, Approx: !hadB || !hadA}
}

// explainedBelow reports whether a sub-folder of c carries most of its change.
func explainedBelow(c Change, changes map[string]Change) bool {
	for p, sub := range changes {
		if filepath.Dir(p) != c.Path || (sub.Delta > 0) != (c.Delta > 0) {
			continue
		}
		if abs(sub.Delta) >= minChange && float64(abs(sub.Delta)) >= explainedShare*float64(abs(c.Delta)) {
			return true
		}
	}
	return false
}

// topmost keeps the paths that have no ancestor in the list, at most 10.
// paths must be sorted.
func topmost(paths []string) []string {
	var out []string
	for _, p := range paths {
		if len(out) > 0 && (p == out[len(out)-1] || strings.HasPrefix(p, out[len(out)-1]+"/")) {
			continue
		}
		out = append(out, p)
	}
	return out[:min(10, len(out))]
}

// touches reports whether p is, holds or sits inside one of parts.
func touches(p string, parts []string) bool {
	for _, u := range parts {
		if p == u || strings.HasPrefix(p, u+"/") || strings.HasPrefix(u, p+"/") {
			return true
		}
	}
	return false
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// Errors of Changes.
var (
	ErrNoSnapshots = errors.New("no size snapshots yet")
	ErrTooRecent   = errors.New("no snapshot is old enough to compare with")
)

// Changes compares the newest snapshot with the newest one at least days
// days older. It returns ErrNoSnapshots or ErrTooRecent when it cannot.
func Changes(e env.Env, days, limit int) (Diff, error) {
	dates, err := Dates(e)
	if err != nil {
		return Diff{}, err
	}
	if len(dates) == 0 {
		return Diff{}, ErrNoSnapshots
	}
	newer, older, ok := Pick(dates, days)
	if !ok {
		return Diff{}, ErrTooRecent
	}
	n, err := Load(e, newer)
	if err != nil {
		return Diff{}, err
	}
	o, err := Load(e, older)
	if err != nil {
		return Diff{}, err
	}
	return Compare(n, o, limit), nil
}
