package similar

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
)

// thumbnail is a 9x8 PNG whose dHash is exactly hash: each row starts at 128
// and goes up (bit set) or down by 10 for the next pixel.
func thumbnail(hash uint64) []byte {
	img := image.NewGray(image.Rect(0, 0, 9, 8))
	for y := range 8 {
		v := 128
		img.SetGray(0, y, color.Gray{uint8(v)})
		for x := range 8 {
			if hash>>(63-(y*8+x))&1 == 1 {
				v += 10
			} else {
				v -= 10
			}
			img.SetGray(x+1, y, color.Gray{uint8(v)})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// fakeSips stands in for sips. A photo file starts with "<16 hex digits of
// the hash> <width>x<height>"; the fake builds the thumbnail from that.
type fakeSips struct {
	mu     sync.Mutex
	seen   []string // paths sips was asked about
	broken string   // a path sips cannot read
}

func (f *fakeSips) run(ctx context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if args[0] == "-g" {
		path := args[len(args)-1]
		f.seen = append(f.seen, path)
		var hash uint64
		var w, h int
		if err := readHeader(path, &hash, &w, &h); err != nil {
			return nil, err
		}
		return []byte(fmt.Sprintf("%s\n  pixelWidth: %d\n  pixelHeight: %d\n", path, w, h)), nil
	}
	in, out := args[5], args[7]
	f.seen = append(f.seen, in)
	if in == f.broken {
		return nil, fmt.Errorf("cannot read %s", in)
	}
	var hash uint64
	var w, h int
	if err := readHeader(in, &hash, &w, &h); err != nil {
		return nil, err
	}
	return nil, os.WriteFile(out, thumbnail(hash), 0o600)
}

func readHeader(path string, hash *uint64, w, h *int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = fmt.Sscanf(string(data[:40]), "%016x %dx%d", hash, w, h)
	return err
}

func useFake(t *testing.T) *fakeSips {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir()) // Find leaves one thumbnail file in $TMPDIR
	f := &fakeSips{}
	old := Sips
	Sips = f.run
	t.Cleanup(func() { Sips = old })
	return f
}

// photo writes a 60 KB file at rel under home that the fake reads as the
// given hash and size.
func photo(t *testing.T, home, rel string, hash uint64, w, h int, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte(" "), 60_000)
	copy(data, fmt.Sprintf("%016x %dx%d", hash, w, h))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

const base uint64 = 0xF0F0_1234_ABCD_5678

func TestDHashMatchesTheCraftedThumbnail(t *testing.T) {
	for _, hash := range []uint64{0, ^uint64(0), base, 1, 1 << 63} {
		img, err := png.Decode(bytes.NewReader(thumbnail(hash)))
		if err != nil {
			t.Fatal(err)
		}
		if got := dHash(img); got != hash {
			t.Errorf("dHash = %016x, want %016x", got, hash)
		}
	}
}

func TestDHashOfALargerThumbnail(t *testing.T) {
	// 64x48: left half dark, right half bright. Only the pairs that cross the middle differ.
	img := image.NewGray(image.Rect(0, 0, 64, 48))
	for y := range 48 {
		for x := range 64 {
			v := uint8(20)
			if x >= 32 {
				v = 220
			}
			img.SetGray(x, y, color.Gray{v})
		}
	}
	// The 9 parts are 7 or 8 px wide. Part 4 (28..34 px) is mixed: the two pairs around it rise, so 2 bits per row.
	if n := bits.OnesCount64(dHash(img)); n != 16 {
		t.Errorf("%d bits set, want 16 (two rises per row)", n)
	}
}

func TestClusterChainsPhotosThroughUnionFind(t *testing.T) {
	flip := func(n int) uint64 { return base ^ (1<<n - 1) } // n bits flipped
	a := Photo{Path: "a", Hash: base}
	b := Photo{Path: "b", Hash: flip(6)}        // 6 from a
	c := Photo{Path: "c", Hash: base ^ 0x3F000} // 6 from a, but 12 from b
	far := Photo{Path: "far", Hash: ^base}
	groups := cluster([]Photo{a, b, c, far}, 6)
	if len(groups) != 1 || len(groups[0]) != 3 {
		t.Fatalf("groups %+v", groups)
	}
	if groups := cluster([]Photo{a, b, c, far}, 5); len(groups) != 0 {
		t.Errorf("threshold 5 groups %+v", groups)
	}
}

func TestFindPicksTheKeeperAndSkipsWhatItMust(t *testing.T) {
	fake := useFake(t)
	e := env.New(t.TempDir())
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-time.Hour)

	big := photo(t, e.Home, "Pictures/big.jpg", base, 4000, 3000, old)      // most pixels
	photo(t, e.Home, "Pictures/small-new.jpg", base^1, 2000, 1500, recent)  // fewer pixels, newer
	photo(t, e.Home, "Pictures/trip/IMG_1.jpg", base^2, 2000, 1500, recent) // same pixels, same time, longer path
	photo(t, e.Home, "Downloads/other.jpg", ^base, 4000, 3000, old)         // unrelated
	tiny := filepath.Join(e.Home, "Pictures/tiny.jpg")
	if err := os.WriteFile(tiny, []byte(fmt.Sprintf("%016x 1x1", base)), 0o644); err != nil { // under 50 KB
		t.Fatal(err)
	}
	photo(t, e.Home, "Pictures/Lib.photoslibrary/originals/p.jpg", base, 4000, 3000, old)
	photo(t, e.Home, "Library/Caches/c.jpg", base, 4000, 3000, old)
	photo(t, e.Home, "Pictures/App.app/Contents/Resources/i.png", base, 4000, 3000, old)
	photo(t, e.Home, "Pictures/proj/shot.jpg", base, 4000, 3000, old)
	if err := os.MkdirAll(filepath.Join(e.Home, "Pictures/proj/.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	photo(t, e.Home, "Pictures/video.mov", base, 4000, 3000, old) // not an image type
	if err := os.Symlink(big, filepath.Join(e.Home, "Pictures/link.jpg")); err != nil {
		t.Fatal(err)
	}

	found, err := Find(context.Background(), e, DefaultRoots(e), Options{Threshold: 6})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Groups) != 1 {
		t.Fatalf("groups %+v", found.Groups)
	}
	g := found.Groups[0]
	if g.Keep.Path != big {
		t.Errorf("keeper %s, want %s", g.Keep.Path, big)
	}
	if len(g.Extras) != 2 || g.Distance(g.Extras[0]) != 1 && g.Distance(g.Extras[1]) != 1 {
		t.Errorf("extras %+v", g.Extras)
	}
	if found.Images != 4 {
		t.Errorf("looked at %d images, want 4", found.Images)
	}
	for _, path := range fake.seen {
		for _, banned := range []string{"photoslibrary", "Library/Caches", ".app", "proj", "tiny", "link.jpg", "video"} {
			if strings.Contains(path, banned) {
				t.Errorf("sips was asked about %s", path)
			}
		}
	}
}

func TestKeeperTieBreaks(t *testing.T) {
	useFake(t)
	e := env.New(t.TempDir())
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-time.Hour)

	// Same pixels: the newest stays.
	photo(t, e.Home, "Pictures/a-old.jpg", base, 100, 100, old)
	newest := photo(t, e.Home, "Pictures/a-new.jpg", base, 100, 100, recent)
	// Same pixels and time: the shortest path stays.
	long := photo(t, e.Home, "Downloads/very/long/b.jpg", ^base, 100, 100, old)
	short := photo(t, e.Home, "Downloads/b.jpg", ^base, 100, 100, old)
	_ = long

	found, err := Find(context.Background(), e, DefaultRoots(e), Options{Threshold: 0})
	if err != nil {
		t.Fatal(err)
	}
	keepers := map[string]bool{}
	for _, g := range found.Groups {
		keepers[g.Keep.Path] = true
	}
	if len(found.Groups) != 2 || !keepers[newest] || !keepers[short] {
		t.Errorf("keepers %v", keepers)
	}
}

func TestFindLeavesOutUnreadableImages(t *testing.T) {
	fake := useFake(t)
	e := env.New(t.TempDir())
	now := time.Now()
	photo(t, e.Home, "Pictures/a.jpg", base, 100, 100, now)
	photo(t, e.Home, "Pictures/b.jpg", base, 100, 100, now)
	fake.broken = photo(t, e.Home, "Pictures/c.jpg", base, 100, 100, now)

	found, err := Find(context.Background(), e, DefaultRoots(e), Options{Threshold: 6})
	if err != nil {
		t.Fatal(err)
	}
	if found.Unreadable != 1 || found.Images != 2 || len(found.Groups) != 1 || len(found.Groups[0].Extras) != 1 {
		t.Errorf("found %+v", found)
	}
}

func TestFindAsksForANarrowerFolder(t *testing.T) {
	useFake(t)
	old := MaxImages
	MaxImages = 2
	t.Cleanup(func() { MaxImages = old })
	e := env.New(t.TempDir())
	for _, name := range []string{"a", "b", "c"} {
		photo(t, e.Home, "Pictures/"+name+".jpg", base, 100, 100, time.Now())
	}
	_, err := Find(context.Background(), e, DefaultRoots(e), Options{})
	if _, ok := err.(*TooManyError); !ok {
		t.Fatalf("error %v", err)
	}
}

func TestFindRefusesRootsItNeverSearches(t *testing.T) {
	useFake(t)
	e := env.New(t.TempDir())
	photo(t, e.Home, "Pictures/Lib.photoslibrary/p.jpg", base, 100, 100, time.Now())
	photo(t, e.Home, "Library/x/p.jpg", base, 100, 100, time.Now())
	photo(t, e.Home, "code/repo/p.jpg", base, 100, 100, time.Now())
	if err := os.MkdirAll(filepath.Join(e.Home, "code/repo/.git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"Pictures/Lib.photoslibrary", "Library/x", "code/repo", "code/repo/sub"} {
		if err := os.MkdirAll(filepath.Join(e.Home, root), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Find(context.Background(), e, []string{filepath.Join(e.Home, root)}, Options{}); err == nil {
			t.Errorf("root %s was searched", root)
		}
	}
}

func TestItems(t *testing.T) {
	useFake(t)
	e := env.New(t.TempDir())
	now := time.Now()
	keep := photo(t, e.Home, "Pictures/a.jpg", base, 200, 200, now)
	extra := photo(t, e.Home, "Pictures/b.jpg", base^0b11, 100, 100, now)
	found, err := Find(context.Background(), e, DefaultRoots(e), Options{Threshold: 6})
	if err != nil {
		t.Fatal(err)
	}
	items := Items(found.Groups)
	if len(items) != 1 {
		t.Fatalf("items %+v", items)
	}
	it := items[0]
	want := fmt.Sprintf("Looks like %s (difference 2/64). Check before moving: similar is not identical.", keep)
	if it.Path != extra || it.Source != "similar" || it.Risk != "risky" || it.Explain != want || it.KeepCopy == nil || it.KeepCopy.Path != keep {
		t.Errorf("item %+v", it)
	}
}
