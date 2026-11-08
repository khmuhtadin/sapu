// Package purge finds build artifacts in project folders (blueprint F4).
// Artifact types live in rules/purge/*.yml (list adapted from kondo, MIT);
// the file format is documented on Type.
// Find only reads the disk; the items go through engine.Plan like any other.
//
// How Find decides:
//   - A folder with an entry matching a marker of a type is a project root of
//     that type. Its direct children named in the type's artifacts, when they
//     are real folders on the same volume, become items. A folder can be the
//     root of several types; the first type by id owns a shared name.
//   - Find never descends into an artifact folder, into node_modules (even
//     where no project owns it, so global npm installs and stray copies are
//     not scanned), into .git, or into Sapu's own state folder. It does
//     descend into every other real folder, so monorepos and nested projects
//     are found. Symlinks are never followed and volumes never crossed.
//   - An artifact folder with a file tracked by git (a committed vendor/ or
//     build/) is skipped, and so is one that holds a git repository one or two
//     levels down (see holdsRepo). The tracked check runs `git ls-files`;
//     when git is not installed, tracked folders are NOT protected.
//   - Options.OlderThan skips a project whose newest activity is newer than
//     Now - OlderThan. Activity is the newest mtime of any file or folder
//     below the project root, not counting artifact folders (the root's own
//     and those of nested projects) and .git. A folder that cannot be read
//     counts as active.
//   - A folder whose .git is a FILE starting with "gitdir:" is a linked git
//     worktree. It also yields one suggestion item, Source "purge:git-worktree",
//     action official_command: `git -C <main repo> worktree remove <worktree>`
//     (WorktreeCommand). git refuses to remove a worktree with uncommitted
//     work. OlderThan applies to it too. Only a worktree whose main checkout
//     is a real folder with a .git folder gets one (IsMainRepo), not one of
//     a bare repository: Apply runs this command only in such a folder.
//
// Unreadable folders and items that vanish or cannot be measured are left out.
// On cancel, Find returns ctx.Err() and no items.
//
// Implemented in Wave 2 (W2-B).
package purge

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

type Options struct {
	OlderThan time.Duration // skip projects touched more recently; 0 = no filter
	Now       time.Time
	Types     fs.FS // the type files to use (rules/purge/*.yml); nil = the ones built into the binary
}

// Find returns one item per artifact folder under roots. Source is
// "purge:<type id>".
func Find(ctx context.Context, e env.Env, roots []string, opts Options) ([]plan.Item, error) {
	var types []Type
	var err error
	if opts.Types != nil {
		types, err = loadTypes(opts.Types)
	} else {
		types, err = embeddedTypes()
	}
	if err != nil {
		return nil, fmt.Errorf("load purge types: %w", err)
	}
	f := &finder{ctx: ctx, env: e, types: types, opts: opts, seen: map[string]bool{}}

	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		fi, err := os.Lstat(abs)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() { // a symlink to a folder is not a folder here
			return nil, fmt.Errorf("%s is not a folder", abs)
		}
		dev, err := devOf(fi)
		if err != nil {
			return nil, err
		}
		f.dev = dev
		if err := f.visit(abs); err != nil {
			return nil, err
		}
	}

	sort.SliceStable(f.items, func(i, j int) bool { return f.items[i].Path < f.items[j].Path })
	return f.items, nil
}

type finder struct {
	ctx   context.Context
	env   env.Env
	types []Type
	opts  Options

	dev   uint64          // the volume of the root being walked
	seen  map[string]bool // paths already turned into items
	items []plan.Item
}

// candidate is an artifact folder that exists in a project root.
type candidate struct {
	typ  Type
	name string
}

// visit looks at one folder and then at its sub-folders.
func (f *finder) visit(dir string) error {
	if err := f.ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // unreadable: leave it out
	}

	cands := f.candidates(entries)
	main := worktreeOf(dir)
	if len(cands) > 0 || main != "" {
		if err := f.emit(dir, cands, main); err != nil {
			return err
		}
	}

	owned := map[string]bool{}
	for _, c := range cands {
		owned[c.name] = true
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || owned[name] || name == ".git" || name == "node_modules" {
			continue
		}
		path := filepath.Join(dir, name)
		if path == f.env.StateDir || !f.onVolume(entry) {
			continue
		}
		if err := f.visit(path); err != nil {
			return err
		}
	}
	return nil
}

// candidates returns the artifact folders of the types whose marker is in
// entries. A name shared by several types goes to the first type by id.
func (f *finder) candidates(entries []os.DirEntry) []candidate {
	names := make([]string, len(entries))
	folders := map[string]bool{}
	for i, entry := range entries {
		names[i] = entry.Name()
		if entry.IsDir() { // false for a symlink to a folder
			folders[entry.Name()] = true
		}
	}

	var out []candidate
	taken := map[string]bool{}
	for _, t := range f.types {
		if !t.hasMarker(names) {
			continue
		}
		for _, a := range t.Artifacts {
			if folders[a] && !taken[a] {
				taken[a] = true
				out = append(out, candidate{typ: t, name: a})
			}
		}
	}
	return out
}

// emit turns the artifact folders of project root dir into items, and the
// folder itself when it is a linked worktree of the repository main.
func (f *finder) emit(dir string, cands []candidate, main string) error {
	if f.opts.OlderThan > 0 {
		newer, err := f.newerThan(dir, f.opts.Now.Add(-f.opts.OlderThan))
		if err != nil || newer {
			return err
		}
	}

	for _, c := range cands {
		path := filepath.Join(dir, c.name)
		if f.seen[path] {
			continue
		}
		if holdsRepo(path) {
			continue
		}
		isTracked, err := tracked(f.ctx, dir, c.name)
		if err != nil {
			return err
		}
		if isTracked {
			continue
		}
		it, ok, err := f.item(path)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		it.Source = "purge:" + c.typ.ID
		it.Risk = c.typ.Risk
		it.Regenerates = true
		it.Explain = fmt.Sprintf("%s Project: %s", c.typ.text(f.env.Lang), dir)
		it.Action = rules.ActionQuarantine
		f.seen[path] = true
		f.items = append(f.items, it)
	}

	if main == "" || f.seen[dir] {
		return nil
	}
	it, ok, err := f.item(dir)
	if err != nil || !ok {
		return err
	}
	it.Source = WorktreeSource
	it.Risk = rules.RiskModerate
	it.Explain = fmt.Sprintf("A git worktree, an extra checkout of %s, often left behind by coding agents. "+
		"git removes it only when it has no uncommitted work. Folder: %s", main, dir)
	it.Action = rules.ActionOfficialCommand
	it.Command = WorktreeCommand(main, dir)
	f.seen[dir] = true
	f.items = append(f.items, it)
	return nil
}

// item measures path. ok is false when path is gone, unreadable or on another
// volume. The caller sets everything else.
func (f *finder) item(path string) (it plan.Item, ok bool, err error) {
	id, err := plan.IdentityOf(path)
	if err != nil || id.Dev != f.dev {
		return plan.Item{}, false, nil
	}
	bytes, _, err := walk.Size(f.ctx, path, nil)
	if ctxErr := f.ctx.Err(); ctxErr != nil {
		return plan.Item{}, false, ctxErr
	}
	if err != nil {
		return plan.Item{}, false, nil
	}
	return plan.Item{Path: path, Bytes: bytes, Identity: id}, true, nil
}

// newerThan reports whether anything below dir, dir itself included, changed
// after cutoff. It stops at the first such entry. Artifact folders and .git
// are not looked at. Anything that cannot be read counts as newer.
func (f *finder) newerThan(dir string, cutoff time.Time) (bool, error) {
	if err := f.ctx.Err(); err != nil {
		return false, err
	}
	fi, err := os.Lstat(dir)
	if err != nil || fi.ModTime().After(cutoff) {
		return true, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true, nil
	}

	skip := map[string]bool{".git": true}
	for _, c := range f.candidates(entries) {
		skip[c.name] = true
	}
	for _, entry := range entries {
		if skip[entry.Name()] {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			return true, nil
		}
		if !entry.IsDir() || !f.onVolume(entry) {
			continue
		}
		newer, err := f.newerThan(filepath.Join(dir, entry.Name()), cutoff)
		if err != nil || newer {
			return newer, err
		}
	}
	return false, nil
}

// onVolume reports whether entry is on the volume being walked.
func (f *finder) onVolume(entry os.DirEntry) bool {
	info, err := entry.Info()
	if err != nil {
		return false
	}
	dev, err := devOf(info)
	return err == nil && dev == f.dev
}

func devOf(fi os.FileInfo) (uint64, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("no stat data for %s", fi.Name())
	}
	return uint64(st.Dev), nil
}
