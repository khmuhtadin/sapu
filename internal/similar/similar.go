// Package similar finds photos that look alike (blueprint H2): burst shots,
// edits, screenshots of the same thing. Find only reads the disk; the items go
// through engine.Plan like any other.
//
// How Find decides:
//   - It walks the roots and keeps regular files of at least MinBytes whose
//     extension is an image type. Symlinks are never followed.
//   - It never descends into ~/Library, .app bundles, *.photoslibrary (the
//     Photos library stays read-only and untouched), .git, node_modules or
//     Sapu's own state folder. A root that is inside one of them is an error.
//     It never descends into a git repository (a folder with a .git entry),
//     and a root inside a repository below the home folder is an error.
//   - With more than MaxImages images it stops with *TooManyError.
//   - Every image gets a 64x64 PNG thumbnail from macOS sips (Sips), and from
//     it a dHash: 9x8 grayscale pixels, one bit for each pair of neighbours
//     that gets brighter. Two photos are similar when the Hamming distance of
//     their hashes is at most Options.Threshold of 64 bits.
//   - Similar photos are grouped with union-find. In each group the photo
//     that stays has the most pixels, then the newest modification time, then
//     the shortest path. Every other photo is an Extra. Items turns the
//     extras into risky plan items whose KeepCopy names the photo that stays.
//   - A photo that changes while it is looked at is left out, and so is one
//     that sips cannot read (counted in Found.Unreadable).
//
// Similar is not identical: a person must look at a group before moving
// anything. Videos are not handled.
//
// All thumbnails go through one temp file in $TMPDIR. Only internal/fsop may
// delete files, so Find does not remove it: one small file stays behind.
package similar

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/png" // sips writes the thumbnails as PNG
	"io/fs"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Source is the Source of every item, and the name of the command.
const Source = "similar"

const (
	MinBytes         = 50_000 // smaller files are not looked at
	DefaultThreshold = 6
	hashBits         = 64
)

// MaxImages is how many images Find looks at; with more it asks for a
// narrower folder. Tests lower it.
var MaxImages = 5000

// Sips runs /usr/bin/sips with args and returns what it printed. Tests
// replace it.
var Sips = func(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "/usr/bin/sips", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("sips %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

var imageTypes = []string{".jpg", ".jpeg", ".png", ".heic", ".heif", ".gif", ".tif", ".tiff", ".webp"}

type Options struct {
	Threshold int               // largest Hamming distance, 0 to 64, of two photos that look alike
	Status    func(text string) // a sentence for progress; may be nil
}

// TooManyError says there are too many images to look at in one go.
type TooManyError struct{ Count int }

func (e *TooManyError) Error() string {
	return fmt.Sprintf("found more than %d images (%d so far); look at a narrower folder, for example sapu similar ~/Pictures/2024", MaxImages, e.Count)
}

// Photo is one image.
type Photo struct {
	Path     string
	Bytes    int64 // allocated bytes
	Pixels   int64 // width times height
	Identity plan.Identity
	Hash     uint64

	mtime int64
}

// Group is photos that look alike: the one that stays and the others.
type Group struct {
	Keep   Photo
	Extras []Photo
}

// Distance is how many of the 64 bits of its hash differ from the photo that stays.
func (g Group) Distance(p Photo) int { return bits.OnesCount64(g.Keep.Hash ^ p.Hash) }

// Found is what Find returns.
type Found struct {
	Groups     []Group // the largest first
	Images     int     // images looked at
	Unreadable int     // images sips could not read, left out
}

// DefaultRoots are the folders searched when the user names none, those that
// exist.
func DefaultRoots(e env.Env) []string {
	var roots []string
	for _, name := range []string{"Pictures", "Downloads", "Desktop"} {
		path := filepath.Join(e.Home, name)
		if fi, err := os.Lstat(path); err == nil && fi.IsDir() {
			roots = append(roots, path)
		}
	}
	return roots
}

type file struct {
	path   string
	blocks int64
	mtime  int64
}

// Find returns the groups of similar photos under roots.
func Find(ctx context.Context, e env.Env, roots []string, opts Options) (Found, error) {
	w := &walker{ctx: ctx, opts: opts, home: filepath.Clean(e.Home), state: strings.ToLower(e.StateDir),
		lib: strings.ToLower(filepath.Join(e.Home, "Library")), seen: map[[2]uint64]bool{}}
	for _, root := range roots {
		if err := w.walkRoot(root); err != nil {
			return Found{}, err
		}
	}

	found := Found{Images: len(w.files)}
	if len(w.files) < 2 { // nothing to compare
		return found, nil
	}
	tmp, err := os.CreateTemp("", "sapu-similar-*.png")
	if err != nil {
		return found, err
	}
	if err := tmp.Close(); err != nil {
		return found, err
	}

	var photos []Photo
	for i, f := range w.files {
		if err := ctx.Err(); err != nil {
			return Found{}, err
		}
		w.status("Looking at photos… %d of %d", i+1, len(w.files))
		p, err := hashPhoto(ctx, f, tmp.Name())
		if err := ctx.Err(); err != nil {
			return Found{}, err
		}
		if err != nil {
			found.Unreadable++
			continue
		}
		photos = append(photos, p)
	}
	found.Images = len(photos)

	for _, members := range cluster(photos, opts.Threshold) {
		for i := range members {
			if err := ctx.Err(); err != nil {
				return Found{}, err
			}
			pixels, err := pixelCount(ctx, members[i].Path)
			if err != nil && ctx.Err() != nil {
				return Found{}, ctx.Err()
			}
			members[i].Pixels = pixels // 0 when sips gave no size: the photo then loses to any with a known size
		}
		slices.SortFunc(members, func(a, b Photo) int {
			return cmp.Or(cmp.Compare(b.Pixels, a.Pixels), cmp.Compare(b.mtime, a.mtime),
				cmp.Compare(len(a.Path), len(b.Path)), cmp.Compare(a.Path, b.Path))
		})
		extras := slices.Clone(members[1:])
		slices.SortFunc(extras, func(a, b Photo) int { return cmp.Compare(a.Path, b.Path) })
		found.Groups = append(found.Groups, Group{Keep: members[0], Extras: extras})
	}
	slices.SortFunc(found.Groups, func(a, b Group) int {
		return cmp.Or(cmp.Compare(extraBytes(b), extraBytes(a)), cmp.Compare(a.Keep.Path, b.Keep.Path))
	})
	return found, nil
}

func extraBytes(g Group) int64 {
	var n int64
	for _, p := range g.Extras {
		n += p.Bytes
	}
	return n
}

// Items turns the extra photos into plan items: risky, quarantine, each
// naming the photo that stays.
func Items(groups []Group) []plan.Item {
	var items []plan.Item
	for _, g := range groups {
		for _, p := range g.Extras {
			items = append(items, plan.Item{
				Source:   Source,
				Path:     p.Path,
				Bytes:    p.Bytes,
				Risk:     rules.RiskRisky,
				Explain:  fmt.Sprintf("Looks like %s (difference %d/%d). Check before moving: similar is not identical.", g.Keep.Path, g.Distance(p), hashBits),
				Action:   rules.ActionQuarantine,
				Identity: p.Identity,
				KeepCopy: &plan.KeepCopy{Path: g.Keep.Path, Identity: g.Keep.Identity},
			})
		}
	}
	return items
}

// cluster returns the groups of two or more photos whose hashes are at most
// threshold bits apart, directly or through other photos (union-find).
func cluster(photos []Photo, threshold int) [][]Photo {
	parent := make([]int, len(photos))
	for i := range parent {
		parent[i] = i
	}
	var root func(i int) int
	root = func(i int) int {
		if parent[i] != i {
			parent[i] = root(parent[i])
		}
		return parent[i]
	}
	for i := range photos {
		for j := i + 1; j < len(photos); j++ {
			if bits.OnesCount64(photos[i].Hash^photos[j].Hash) <= threshold {
				parent[root(i)] = root(j)
			}
		}
	}
	byRoot := map[int][]Photo{}
	for i, p := range photos {
		byRoot[root(i)] = append(byRoot[root(i)], p)
	}
	var groups [][]Photo
	for _, members := range byRoot {
		if len(members) > 1 {
			groups = append(groups, members)
		}
	}
	return groups
}

// hashPhoto makes the thumbnail of f in thumb and hashes it. It fails when
// the file changed while it was read.
func hashPhoto(ctx context.Context, f file, thumb string) (Photo, error) {
	before, err := plan.IdentityOf(f.path)
	if err != nil {
		return Photo{}, err
	}
	if err := os.WriteFile(thumb, nil, 0o600); err != nil { // so a failed sips cannot leave the last thumbnail
		return Photo{}, err
	}
	if _, err := Sips(ctx, "-s", "format", "png", "-Z", "64", f.path, "--out", thumb); err != nil {
		return Photo{}, err
	}
	data, err := os.ReadFile(thumb)
	if err != nil {
		return Photo{}, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Photo{}, err
	}
	after, err := plan.IdentityOf(f.path)
	if err != nil {
		return Photo{}, err
	}
	if before != after {
		return Photo{}, fmt.Errorf("%s changed while it was read", f.path)
	}
	return Photo{Path: f.path, Bytes: f.blocks, Identity: after, Hash: dHash(img), mtime: f.mtime}, nil
}

// dHash is 9x8 gray values (each the average of its part of img) and one bit
// per row and pair of neighbours: set when the right one is brighter.
func dHash(img image.Image) uint64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var gray [8][9]int
	for y := range 8 {
		for x := range 9 {
			x0, x1 := x*w/9, max((x+1)*w/9, x*w/9+1)
			y0, y1 := y*h/8, max((y+1)*h/8, y*h/8+1)
			sum, n := 0, 0
			for py := y0; py < y1; py++ {
				for px := x0; px < x1; px++ {
					sum += int(color.GrayModel.Convert(img.At(b.Min.X+px, b.Min.Y+py)).(color.Gray).Y)
					n++
				}
			}
			gray[y][x] = sum / n
		}
	}
	var hash uint64
	for _, row := range gray {
		for x := 0; x+1 < len(row); x++ {
			hash <<= 1
			if row[x] < row[x+1] {
				hash |= 1
			}
		}
	}
	return hash
}

// pixelCount reads the size in pixels of the image at path from sips.
func pixelCount(ctx context.Context, path string) (int64, error) {
	out, err := Sips(ctx, "-g", "pixelWidth", "-g", "pixelHeight", path)
	if err != nil {
		return 0, err
	}
	var width, height int64
	for _, line := range strings.Split(string(out), "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		switch name {
		case "pixelWidth":
			width = n
		case "pixelHeight":
			height = n
		}
	}
	if width < 1 || height < 1 {
		return 0, fmt.Errorf("sips gave no size for %s", path)
	}
	return width * height, nil
}

type walker struct {
	ctx   context.Context
	opts  Options
	home  string
	state string // lower case
	lib   string // lower case

	seen  map[[2]uint64]bool // files already taken, whatever name they were found by
	files []file
	next  time.Time
}

// status sends a progress sentence, at most ten a second.
func (w *walker) status(format string, args ...any) {
	if w.opts.Status == nil || time.Now().Before(w.next) {
		return
	}
	w.next = time.Now().Add(100 * time.Millisecond)
	w.opts.Status(fmt.Sprintf(format, args...))
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
			return fmt.Errorf("%s is never searched: it is, or is inside, a folder Sapu keeps out of photo searches", root)
		}
		if strings.HasPrefix(p, w.home+"/") && isRepo(p) {
			return fmt.Errorf("%s is never searched: it is, or is inside, the git repository %s", root, p)
		}
		if p == filepath.Dir(p) {
			break
		}
	}

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return nil // unreadable: leave it out
		}
		if d.IsDir() {
			if w.excluded(path) || isRepo(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !slices.Contains(imageTypes, strings.ToLower(filepath.Ext(path))) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Size < MinBytes {
			return nil
		}
		key := [2]uint64{uint64(st.Dev), st.Ino}
		if w.seen[key] {
			return nil
		}
		w.seen[key] = true
		w.files = append(w.files, file{path: path, blocks: st.Blocks * 512, mtime: st.Mtimespec.Nano()})
		if len(w.files) > MaxImages {
			return &TooManyError{Count: len(w.files)}
		}
		w.status("Looking for photos… %d found", len(w.files))
		return nil
	})
}
