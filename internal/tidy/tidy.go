// Package tidy finds empty folders, broken symlinks and zero-byte files
// (blueprint H3). Find only reads the disk; the items go through engine.Plan
// like any other.
//
// How Find decides:
//   - It walks the roots. It never follows symlinks, and never enters
//     ~/Library, .app bundles, *.photoslibrary, .git, node_modules, a git
//     repository (a folder with a .git entry) or Sapu's own state folder.
//     A root that is inside one of them is an error. A folder that holds one
//     of them is not empty.
//   - An empty folder is one whose whole subtree holds nothing but .DS_Store
//     files and empty folders. Only the topmost one is reported, never a root
//     itself. A folder Find could not read completely is not empty.
//   - A broken symlink is one whose target does not exist. A link whose
//     target could not be checked (no permission, a loop) is left alone.
//   - A zero-byte file is reported unless it is a dotfile, "Icon\r", *.lock
//     or *.pid: apps create those on purpose.
//   - Bytes of every item is 0: none of them uses space worth counting.
//
// Unreadable folders are left out. On cancel, Find returns ctx.Err().
package tidy

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Source is the Source of every item, and the name of the command.
const Source = "tidy"

// Kind says what an entry is.
type Kind string

const (
	EmptyFolder Kind = "empty-folder"
	BrokenLink  Kind = "broken-link"
	EmptyFile   Kind = "empty-file"
)

// Entry is one thing that is worth tidying.
type Entry struct {
	Path     string
	Kind     Kind
	Target   string // broken links: where the link points
	Identity plan.Identity
}

// DefaultRoots are the folders searched when the user names none, those that
// exist.
func DefaultRoots(e env.Env) []string {
	var roots []string
	for _, name := range []string{"Downloads", "Desktop", "Documents"} {
		path := filepath.Join(e.Home, name)
		if fi, err := os.Lstat(path); err == nil && fi.IsDir() {
			roots = append(roots, path)
		}
	}
	return roots
}

// Find returns what is worth tidying under roots, folders first, each kind in
// path order.
func Find(ctx context.Context, e env.Env, roots []string) ([]Entry, error) {
	w := &walker{ctx: ctx, home: filepath.Clean(e.Home), state: strings.ToLower(e.StateDir),
		lib: strings.ToLower(filepath.Join(e.Home, "Library")), seen: map[string]bool{}}
	for _, root := range roots {
		if err := w.walkRoot(root); err != nil {
			return nil, err
		}
	}
	order := []Kind{EmptyFolder, BrokenLink, EmptyFile}
	slices.SortFunc(w.entries, func(a, b Entry) int {
		return cmp.Or(cmp.Compare(slices.Index(order, a.Kind), slices.Index(order, b.Kind)), cmp.Compare(a.Path, b.Path))
	})
	return w.entries, nil
}

// Items turns the entries into plan items: risky, quarantine.
func Items(entries []Entry) []plan.Item {
	items := make([]plan.Item, len(entries))
	for i, en := range entries {
		items[i] = plan.Item{
			Source:   Source,
			Path:     en.Path,
			Risk:     rules.RiskRisky,
			Explain:  explain(en),
			Action:   rules.ActionQuarantine,
			Identity: en.Identity,
		}
	}
	return items
}

func explain(en Entry) string {
	switch en.Kind {
	case EmptyFolder:
		return "An empty folder (only Finder's .DS_Store files). Some apps expect their folders; undo puts it back."
	case BrokenLink:
		return fmt.Sprintf("A link to %s, which does not exist any more. Only the link moves; undo puts it back.", en.Target)
	case EmptyFile:
		return "A file with nothing in it. An app may use it as a marker; undo puts it back."
	}
	return ""
}

type walker struct {
	ctx   context.Context
	home  string
	state string // lower case
	lib   string // lower case

	seen    map[string]bool // roots already walked, so a folder given twice is listed once
	entries []Entry
}

// excluded reports whether Find never looks at path: a folder it never enters,
// or something inside one.
func (w *walker) excluded(path string) bool {
	lower := strings.ToLower(path)
	for _, dir := range []string{w.lib, w.state} {
		if lower == dir || strings.HasPrefix(lower, dir+"/") {
			return true
		}
	}
	name := strings.ToLower(filepath.Base(path))
	return name == ".git" || name == "node_modules" || strings.HasSuffix(name, ".app") || strings.HasSuffix(name, ".photoslibrary")
}

// isRepo reports whether dir holds a .git entry (a folder, or the file of a
// worktree or submodule).
func isRepo(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

func (w *walker) walkRoot(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !fi.IsDir() { // a symlink to a folder is not a folder here
		return fmt.Errorf("%s is not a folder", root)
	}
	for p := root; ; p = filepath.Dir(p) {
		if w.excluded(p) {
			return fmt.Errorf("%s is never searched: it is, or is inside, a folder Sapu keeps out of tidy searches", root)
		}
		if strings.HasPrefix(p, w.home+"/") && isRepo(p) {
			return fmt.Errorf("%s is never searched: it is, or is inside, the git repository %s", root, p)
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	if w.seen[root] {
		return nil
	}
	w.seen[root] = true
	w.scan(root)
	return w.ctx.Err()
}

// scan walks dir, records what it finds, and reports whether dir is empty.
func (w *walker) scan(dir string) bool {
	if w.ctx.Err() != nil {
		return false
	}
	children, err := os.ReadDir(dir)
	empty := err == nil
	var emptyKids []string
	for _, child := range children {
		path := filepath.Join(dir, child.Name())
		switch {
		case child.Type().IsRegular() && child.Name() == ".DS_Store":
		case child.IsDir():
			if w.excluded(path) || isRepo(path) {
				empty = false
			} else if w.scan(path) {
				emptyKids = append(emptyKids, path)
			} else {
				empty = false
			}
		case child.Type()&fs.ModeSymlink != 0:
			empty = false
			w.checkLink(path)
		case child.Type().IsRegular():
			empty = false
			w.checkFile(path, child.Name())
		default: // sockets, pipes, devices
			empty = false
		}
	}
	if !empty {
		for _, path := range emptyKids {
			w.add(Entry{Path: path, Kind: EmptyFolder})
		}
	}
	return empty
}

func (w *walker) checkLink(path string) {
	_, err := os.Stat(path)
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return // the target is there, or could not be checked
	}
	target, err := os.Readlink(path)
	if err != nil {
		return
	}
	w.add(Entry{Path: path, Kind: BrokenLink, Target: target})
}

func (w *walker) checkFile(path, name string) {
	lower := strings.ToLower(name)
	if strings.HasPrefix(name, ".") || name == "Icon\r" || strings.HasSuffix(lower, ".lock") || strings.HasSuffix(lower, ".pid") {
		return
	}
	info, err := os.Lstat(path)
	if err != nil || info.Size() != 0 {
		return
	}
	w.add(Entry{Path: path, Kind: EmptyFile})
}

// add records an entry with its identity. An entry that cannot be read
// (it vanished) is left out.
func (w *walker) add(en Entry) {
	id, err := plan.IdentityOf(en.Path)
	if err != nil {
		return
	}
	en.Identity = id
	w.entries = append(w.entries, en)
}
