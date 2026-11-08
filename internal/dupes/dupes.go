// Package dupes finds files with identical content (blueprint H1).
// Find only reads the disk; the items go through engine.Plan like any other.
//
// How Find decides:
//   - It walks the roots and keeps regular files of at least Options.MinSize
//     bytes. Symlinks are never followed, other kinds of file are skipped, and
//     a walk never crosses into another volume.
//   - It never descends into ~/Library, ~/Music/Music, ~/Music/iTunes, .app
//     bundles, *.photoslibrary, *.musiclibrary, .git, node_modules or Sapu's
//     own state folder. A root that is inside one of them is an error.
//   - It never descends into a git repository (a folder with a .git entry):
//     copies in code projects are usually tracked files, and moving them
//     breaks the project. A root inside a repository below the home folder
//     is an error. (A home folder that is itself a repository is allowed.)
//   - Files are compared in three stages: same size, then the same SHA-256 of
//     the first and last 64 KB, then the same SHA-256 of the whole content.
//     Only the last stage makes two files duplicates.
//   - Names of one file (hard links, or a folder given twice) count once:
//     they are the same data, not copies.
//   - A file whose size or modification time changes while it is hashed is
//     left out, so the identity in an item always belongs to the content that
//     was hashed.
//   - Of each group of copies, Options.Keep picks the one that stays. Every
//     other copy is an Extra. Items turns the extras into plan items whose
//     KeepCopy names the copy that stays.
//
// Unreadable folders and files are left out. On cancel, Find returns
// ctx.Err() and no groups.
package dupes

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// Source is the Source of every item, and the name of the command.
const Source = "dupes"

// Keep says which copy of a group stays.
type Keep string

const (
	KeepOldest       Keep = "oldest"        // earliest birth time, then shortest path, then path order
	KeepNewest       Keep = "newest"        // latest birth time, then shortest path, then path order
	KeepShortestPath Keep = "shortest-path" // shortest path, then path order
)

// edge is how much of the start and the end of a file the partial hash reads.
const edge = 64 << 10

type Options struct {
	MinSize int64 // files smaller than this are ignored
	Keep    Keep

	Scan   walk.Progress     // files and bytes found while walking; may be nil
	Status func(text string) // a sentence for the comparing stages; may be nil
}

// File is one copy.
type File struct {
	Path     string
	Bytes    int64 // allocated bytes
	Identity plan.Identity

	born int64 // birth time, unix nanoseconds
}

// Group is files with the same content: the copy that stays and the others.
type Group struct {
	Keep   File
	Extras []File
}

// DefaultRoots are the folders searched when the user names none, those that
// exist.
func DefaultRoots(e env.Env) []string {
	var roots []string
	for _, name := range []string{"Downloads", "Desktop", "Documents", "Pictures", "Movies", "Music"} {
		path := filepath.Join(e.Home, name)
		if fi, err := os.Lstat(path); err == nil && fi.IsDir() {
			roots = append(roots, path)
		}
	}
	return roots
}

// Find returns the groups of duplicates under roots, the largest first.
func Find(ctx context.Context, e env.Env, roots []string, opts Options) ([]Group, error) {
	s := &scanner{ctx: ctx, opts: opts, home: filepath.Clean(e.Home), seen: map[inode]bool{}}
	for _, dir := range []string{
		filepath.Join(e.Home, "Library"),
		filepath.Join(e.Home, "Music", "Music"),
		filepath.Join(e.Home, "Music", "iTunes"),
		e.StateDir,
	} {
		s.skipPaths = append(s.skipPaths, strings.ToLower(dir))
	}

	for _, root := range roots {
		if err := s.walkRoot(root); err != nil {
			return nil, err
		}
	}
	s.sendScan(s.lastPath, true)

	groups, err := s.compare()
	if err != nil {
		return nil, err
	}
	for i := range groups {
		sortCopies(groups[i].Extras, KeepShortestPath) // stable listing; the keeper was chosen already
	}
	slices.SortFunc(groups, func(a, b Group) int {
		return cmp.Or(cmp.Compare(extraBytes(b), extraBytes(a)), cmp.Compare(a.Keep.Path, b.Keep.Path))
	})
	return groups, nil
}

func extraBytes(g Group) int64 {
	var n int64
	for _, f := range g.Extras {
		n += f.Bytes
	}
	return n
}

// Items turns the extra copies into plan items: risky, quarantine, each naming
// the copy that stays.
func Items(groups []Group) []plan.Item {
	var items []plan.Item
	for _, g := range groups {
		for _, f := range g.Extras {
			items = append(items, plan.Item{
				Source:   Source,
				Path:     f.Path,
				Bytes:    f.Bytes,
				Risk:     rules.RiskRisky,
				Explain:  fmt.Sprintf("Same content as %s. The copy listed first stays.", g.Keep.Path),
				Action:   rules.ActionQuarantine,
				Identity: f.Identity,
				KeepCopy: &plan.KeepCopy{Path: g.Keep.Path, Identity: g.Keep.Identity},
			})
		}
	}
	return items
}

type inode struct{ dev, ino uint64 }

// candidate is a file that was big enough, found by the walk.
type candidate struct {
	path   string
	size   int64 // logical size, as read from the folder
	blocks int64 // allocated bytes
	mtime  int64
	born   int64
}

type scanner struct {
	ctx       context.Context
	opts      Options
	home      string
	skipPaths []string // lower case

	seen       map[inode]bool // files already taken, whatever name they were found by
	candidates []candidate

	files, bytes int64 // found so far
	lastPath     string
	nextEvent    time.Time
}

// excluded reports whether Find never looks at path: a folder it never enters,
// or something inside one.
func (s *scanner) excluded(path string) bool {
	lower := strings.ToLower(path)
	for _, dir := range s.skipPaths {
		if lower == dir || strings.HasPrefix(lower, dir+"/") {
			return true
		}
	}
	name := strings.ToLower(filepath.Base(path))
	if name == ".git" || name == "node_modules" {
		return true
	}
	for _, suffix := range []string{".app", ".photoslibrary", ".musiclibrary"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// isRepo reports whether dir holds a .git entry (a folder, or the file of a
// worktree or submodule).
func isRepo(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

func (s *scanner) walkRoot(root string) error {
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
		if s.excluded(p) {
			return fmt.Errorf("%s is never searched: it is, or is inside, a folder Sapu keeps out of duplicate searches", root)
		}
		if strings.HasPrefix(p, s.home+"/") && isRepo(p) {
			return fmt.Errorf("%s is never searched: it is, or is inside, the git repository %s; copies in code projects are usually tracked files", root, p)
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	rootStat, err := statOf(fi)
	if err != nil {
		return err
	}

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return nil // unreadable: leave it out
		}
		if d.IsDir() {
			if s.excluded(path) || isRepo(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // symlinks, sockets, devices, ...
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, err := statOf(info)
		if err != nil || st.Dev != rootStat.Dev { // another volume
			return nil
		}
		if st.Size < max(s.opts.MinSize, 1) {
			return nil
		}
		key := inode{uint64(st.Dev), st.Ino}
		if s.seen[key] {
			return nil
		}
		s.seen[key] = true
		s.candidates = append(s.candidates, candidate{
			path:   path,
			size:   st.Size,
			blocks: st.Blocks * 512,
			mtime:  st.Mtimespec.Nano(),
			born:   st.Birthtimespec.Nano(),
		})
		s.files++
		s.bytes += st.Size
		s.sendScan(path, false)
		return nil
	})
}

func statOf(fi os.FileInfo) (*syscall.Stat_t, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("no stat data for %s", fi.Name())
	}
	return st, nil
}

// due reports whether a progress message may go out now; at most ten a second.
func (s *scanner) due() bool {
	now := time.Now()
	if now.Before(s.nextEvent) {
		return false
	}
	s.nextEvent = now.Add(100 * time.Millisecond)
	return true
}

func (s *scanner) sendScan(path string, final bool) {
	s.lastPath = path
	if s.opts.Scan != nil && (final || s.due()) {
		s.opts.Scan(walk.Event{Path: path, Files: s.files, Bytes: s.bytes})
	}
}

func (s *scanner) sendStatus(format string, args ...any) {
	if s.opts.Status != nil && s.due() {
		s.opts.Status(fmt.Sprintf(format, args...))
	}
}

// compare runs the three stages and returns the groups of identical files.
func (s *scanner) compare() ([]Group, error) {
	bySize := map[int64][]candidate{}
	for _, c := range s.candidates {
		bySize[c.size] = append(bySize[c.size], c)
	}
	total := 0
	for _, same := range bySize {
		if len(same) > 1 {
			total += len(same)
		}
	}

	var groups []Group
	done := 0
	for _, size := range slices.Sorted(maps.Keys(bySize)) {
		same := bySize[size]
		if len(same) < 2 {
			continue
		}

		byEdges := map[[sha256.Size]byte][]candidate{}
		for _, c := range same {
			if err := s.ctx.Err(); err != nil {
				return nil, err
			}
			done++
			s.sendStatus("Comparing the start and end of files… %d of %d", done, total)
			sum, err := edgesHash(c)
			if err != nil {
				continue // unreadable, or changed: leave it out
			}
			byEdges[sum] = append(byEdges[sum], c)
		}

		for _, edgeSame := range byEdges {
			if len(edgeSame) < 2 {
				continue
			}
			byContent := map[[sha256.Size]byte][]File{}
			for _, c := range edgeSame {
				s.sendStatus("Reading %s…", filepath.Base(c.path))
				sum, f, err := s.contentHash(c)
				if err := s.ctx.Err(); err != nil {
					return nil, err
				}
				if err != nil {
					continue
				}
				byContent[sum] = append(byContent[sum], f)
			}
			for _, copies := range byContent {
				if len(copies) < 2 {
					continue
				}
				sortCopies(copies, s.opts.Keep)
				groups = append(groups, Group{Keep: copies[0], Extras: copies[1:]})
			}
		}
	}
	return groups, nil
}

// sortCopies puts the copy that stays first.
func sortCopies(files []File, keep Keep) {
	slices.SortFunc(files, func(a, b File) int {
		byPath := cmp.Or(cmp.Compare(len(a.Path), len(b.Path)), cmp.Compare(a.Path, b.Path))
		switch keep {
		case KeepNewest:
			return cmp.Or(cmp.Compare(b.born, a.born), byPath)
		case KeepShortestPath:
			return byPath
		case KeepOldest:
			// the order below
		}
		return cmp.Or(cmp.Compare(a.born, b.born), byPath)
	})
}

// open opens a regular file for reading. It refuses a symlink, and does not
// wait on a pipe that took the file's place.
func open(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("%s is not a regular file", path), err, f.Close())
	}
	return f, nil
}

// edgesHash is the SHA-256 of the first and last 64 KB of c.
func edgesHash(c candidate) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	f, err := open(c.path)
	if err != nil {
		return sum, err
	}
	defer func() { _ = f.Close() }() // read only: a failed close loses nothing

	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, min(c.size, edge))); err != nil {
		return sum, err
	}
	tail := max(c.size-edge, 0)
	if _, err := io.Copy(h, io.NewSectionReader(f, tail, c.size-tail)); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// contentHash is the SHA-256 of all of c. It fails when the file changed while
// it was read: its size or modification time differ from what the walk saw, or
// fewer or more bytes than that came out.
func (s *scanner) contentHash(c candidate) ([sha256.Size]byte, File, error) {
	var sum [sha256.Size]byte
	before, err := plan.IdentityOf(c.path)
	if err != nil {
		return sum, File{}, err
	}
	f, err := open(c.path)
	if err != nil {
		return sum, File{}, err
	}
	h := sha256.New()
	n, err := io.Copy(h, ctxReader{s.ctx, f})
	if err := errors.Join(err, f.Close()); err != nil {
		return sum, File{}, err
	}
	after, err := plan.IdentityOf(c.path)
	if err != nil {
		return sum, File{}, err
	}
	if before != after || after.Size != c.size || after.MtimeNs != c.mtime || n != c.size {
		return sum, File{}, fmt.Errorf("%s changed while it was read", c.path)
	}
	copy(sum[:], h.Sum(nil))
	return sum, File{Path: c.path, Bytes: c.blocks, Identity: after, born: c.born}, nil
}

// ctxReader stops reading when ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
