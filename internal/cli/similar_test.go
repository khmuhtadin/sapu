package cli

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/similar"
)

const similarBase uint64 = 0xF0F0_1234_ABCD_5678

// similarThumbnail is a 9x8 PNG whose dHash is exactly hash.
func similarThumbnail(hash uint64) []byte {
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

// fakeSips replaces sips for the test. A photo file starts with "<16 hex
// digits of its hash> <width>x<height>". It returns the paths it was asked about.
func fakeSips(t *testing.T) *[]string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir()) // Find leaves one thumbnail file in $TMPDIR
	var seen []string
	header := func(path string) (hash uint64, w, h int, err error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return 0, 0, 0, err
		}
		_, err = fmt.Sscanf(string(data[:40]), "%016x %dx%d", &hash, &w, &h)
		return hash, w, h, err
	}
	old := similar.Sips
	similar.Sips = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "-g" {
			path := args[len(args)-1]
			seen = append(seen, path)
			_, w, h, err := header(path)
			return []byte(fmt.Sprintf("%s\n  pixelWidth: %d\n  pixelHeight: %d\n", path, w, h)), err
		}
		in, out := args[5], args[7]
		seen = append(seen, in)
		hash, _, _, err := header(in)
		if err != nil {
			return nil, err
		}
		return nil, os.WriteFile(out, similarThumbnail(hash), 0o600)
	}
	t.Cleanup(func() { similar.Sips = old })
	return &seen
}

// similarPhoto writes a 60 KB file that the fake sips reads as hash and size.
func similarPhoto(t *testing.T, e env.Env, rel string, hash uint64, w, h int, mtime time.Time) string {
	t.Helper()
	data := bytes.Repeat([]byte(" "), 60_000)
	copy(data, fmt.Sprintf("%016x %dx%d", hash, w, h))
	path := filepath.Join(e.Home, rel)
	writeFile(t, path, string(data))
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

// similarHome has a group of three (big.jpg stays: most pixels), a photo that
// looks nothing like them, and look-alikes in places that are never searched.
func similarHome(t *testing.T) env.Env {
	t.Helper()
	e := env.New(t.TempDir())
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-time.Hour)
	similarPhoto(t, e, "Pictures/big.jpg", similarBase, 4000, 3000, old)
	similarPhoto(t, e, "Pictures/small.jpg", similarBase^0b11, 2000, 1500, recent)
	similarPhoto(t, e, "Downloads/edit.png", similarBase^0b111100, 2000, 1500, old)
	similarPhoto(t, e, "Desktop/other.jpg", ^similarBase, 4000, 3000, old)
	// never searched
	similarPhoto(t, e, "Pictures/Lib.photoslibrary/originals/p.jpg", similarBase, 4000, 3000, old)
	similarPhoto(t, e, "Library/Caches/c.jpg", similarBase, 4000, 3000, old)
	similarPhoto(t, e, "Pictures/App.app/Contents/Resources/i.png", similarBase, 4000, 3000, old)
	similarPhoto(t, e, "Pictures/proj/shot.jpg", similarBase, 4000, 3000, old)
	writeFile(t, filepath.Join(e.Home, "Pictures/proj/.git/HEAD"), "ref: refs/heads/main\n")
	if err := os.Symlink(filepath.Join(e.Home, "Pictures/big.jpg"), filepath.Join(e.Home, "Pictures/link.jpg")); err != nil {
		t.Fatal(err)
	}
	return e
}

func assertSimilarSearchedOnly(t *testing.T, seen []string) {
	t.Helper()
	if len(seen) == 0 {
		t.Error("sips was never called")
	}
	for _, path := range seen {
		for _, banned := range []string{"photoslibrary", "/Library/", ".app/", "proj", "link.jpg"} {
			if strings.Contains(path, banned) {
				t.Errorf("sips was asked about %s", path)
			}
		}
	}
}

func similarExtras(e env.Env) []string {
	return dupesPaths(e, "Pictures/small.jpg", "Downloads/edit.png")
}

func TestSimilarListsGroupsAndChangesNothing(t *testing.T) {
	seen := fakeSips(t)
	e := similarHome(t)
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "similar")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu similar\n%s", out)
	checkHuman(t, out)
	for _, line := range []string{
		"Looks alike · 3 photos", "keep  ~/Pictures/big.jpg", "12.0 MP",
		"1  move  ~/Downloads/edit.png", "2  move  ~/Pictures/small.jpg", "4/64", "2/64",
		"Total · 2 extra photos in 1 group", "Similar is not identical",
		"Nothing was changed. Pick with --pick 1,3 or --pick-all.",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
	if strings.Contains(out, "other.jpg") {
		t.Errorf("a photo with no look-alike is listed:\n%s", out)
	}
	assertSimilarSearchedOnly(t, *seen)
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestSimilarThreshold(t *testing.T) {
	fakeSips(t)
	e := similarHome(t)
	out, _, code := sapu(t, e, "", "similar", "--threshold", "2")
	if code != 0 || !strings.Contains(out, "Looks alike · 2 photos") || strings.Contains(out, "edit.png") {
		t.Errorf("--threshold 2: exit %d\n%s", code, out)
	}
	out, _, code = sapu(t, e, "", "similar", "--threshold", "1")
	if code != 0 || !strings.Contains(out, "No similar photos found among 4 images.") {
		t.Errorf("--threshold 1: exit %d\n%s", code, out)
	}
	for _, args := range [][]string{
		{"similar", "--threshold", "65"},
		{"similar", "--threshold", "-1"},
		{"similar", "--pick", "1", "--pick-all"},
		{"similar", "--pick", "0"},
		{"similar", "--pick", "1", "--json"}, // cannot ask
	} {
		if _, _, code := sapu(t, e, "", args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestSimilarKeeperAndPlan(t *testing.T) {
	fakeSips(t)
	e := similarHome(t)
	before := snapshot(t, e)
	file := filepath.Join(t.TempDir(), "plan.json")
	out, errOut, code := sapu(t, e, "", "similar", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	p := readPlanFile(t, file)
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
	if p.Kind != "similar" || len(p.Items) != 2 || len(selectedPaths(p)) != 0 {
		t.Fatalf("plan %+v", p)
	}
	for _, it := range p.Items {
		wantExplain := "Looks like " + filepath.Join(e.Home, "Pictures/big.jpg") + " (difference "
		if it.Source != "similar" || it.Risk != "risky" || it.KeepCopy == nil || it.KeepCopy.Path != filepath.Join(e.Home, "Pictures/big.jpg") ||
			!strings.HasPrefix(it.Explain, wantExplain) || !strings.HasSuffix(it.Explain, "/64). Check before moving: similar is not identical.") {
			t.Errorf("item %+v", it)
		}
	}
}

func TestSimilarPickAllMovesOnlyExtrasAndUndoRestores(t *testing.T) {
	fakeSips(t)
	e := similarHome(t)
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "similar", "--pick-all", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "Moved 2 items") {
		t.Errorf("output lacks the result:\n%s", out)
	}
	for _, path := range similarExtras(e) {
		if exists(path) {
			t.Errorf("%s was not moved", path)
		}
	}
	for _, rel := range []string{"Pictures/big.jpg", "Desktop/other.jpg", "Pictures/Lib.photoslibrary/originals/p.jpg", "Library/Caches/c.jpg", "Pictures/proj/shot.jpg", "Pictures/link.jpg"} {
		if !exists(filepath.Join(e.Home, rel)) {
			t.Errorf("%s is gone", rel)
		}
	}

	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))
}

func TestSimilarPickByNumberAndAsksFirst(t *testing.T) {
	fakeSips(t)
	e := similarHome(t)
	before := snapshot(t, e)

	if _, _, code := sapu(t, e, "n\n", "similar", "--pick", "1"); code != 2 {
		t.Errorf("answer n: exit %d, want 2", code)
	}
	sameSnapshot(t, before, snapshot(t, e))

	out, errOut, code := sapu(t, e, "y\n", "similar", "--pick", "2")
	if code != 0 || !strings.Contains(out, "Moved 1 item") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if exists(filepath.Join(e.Home, "Pictures/small.jpg")) || !exists(filepath.Join(e.Home, "Downloads/edit.png")) {
		t.Errorf("--pick 2 moved the wrong photo")
	}
	if _, _, code := sapu(t, e, "", "similar", "--pick", "5", "--yes"); code != 2 {
		t.Errorf("--pick 5 of a list of 1: exit %d, want 2", code)
	}
}

// The safety case: the photo that stays is edited after the plan was made.
func TestSimilarKeeperEditedAfterPlanningLeavesExtraInPlace(t *testing.T) {
	fakeSips(t)
	e := similarHome(t)
	file := filepath.Join(t.TempDir(), "plan.json")
	if _, _, code := sapu(t, e, "", "similar", "--plan", file, "--pick-all"); code != 0 {
		t.Fatal("similar --plan failed")
	}
	p := readPlanFile(t, file)
	if got := selectedPaths(p); !slices.Equal(got, similarExtras(e)) {
		t.Fatalf("selected %q, want %q", got, similarExtras(e))
	}

	keeper := filepath.Join(e.Home, "Pictures/big.jpg")
	if err := os.WriteFile(keeper, []byte("edited after the plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(keeper, later, later); err != nil {
		t.Fatal(err)
	}

	out, errOut, code := sapu(t, e, "", "apply", file, "--yes")
	if code != 3 {
		t.Fatalf("exit %d, want 3\n%s\n%s", code, out, errOut)
	}
	for _, line := range []string{"Nothing was moved.", "the copy that stays"} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
	for _, path := range similarExtras(e) {
		if !exists(path) {
			t.Errorf("%s was moved although the photo that stays changed", path)
		}
	}
}

func TestSimilarTooManyImages(t *testing.T) {
	fakeSips(t)
	old := similar.MaxImages
	similar.MaxImages = 3
	t.Cleanup(func() { similar.MaxImages = old })
	e := similarHome(t)
	out, errOut, code := sapu(t, e, "", "similar")
	if code != 2 || !strings.Contains(errOut, "look at a narrower folder") {
		t.Errorf("exit %d\n%s\n%s", code, out, errOut)
	}
	out, errOut, code = sapu(t, e, "", "similar", filepath.Join(e.Home, "Pictures"))
	if code != 0 {
		t.Errorf("a narrower folder: exit %d\n%s\n%s", code, out, errOut)
	}
}

func TestSimilarDirArguments(t *testing.T) {
	fakeSips(t)
	e := similarHome(t)
	out, _, code := sapu(t, e, "", "similar", filepath.Join(e.Home, "Desktop"))
	if code != 0 || !strings.Contains(out, "No similar photos found among 1 image.") {
		t.Errorf("exit %d\n%s", code, out)
	}
	for _, dir := range []string{"Pictures/Lib.photoslibrary", "Pictures/proj", "Library/Caches", "nowhere"} {
		if _, _, code := sapu(t, e, "", "similar", filepath.Join(e.Home, dir)); code == 0 {
			t.Errorf("%s was searched", dir)
		}
	}
}

func TestSimilarJSON(t *testing.T) {
	fakeSips(t)
	e := similarHome(t)

	out, errOut, code := sapu(t, e, "", "similar", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "similar", out)
	var d similarList
	decodeData(t, out, &d)
	if len(d.Groups) != 1 || len(d.Groups[0].Extras) != 2 || d.Images != 4 || d.Groups[0].Keep.Pixels != 12_000_000 {
		t.Errorf("data %+v", d)
	}
	if first := d.Groups[0].Extras[0]; first.Number != 1 || first.Path != filepath.Join(e.Home, "Downloads/edit.png") || first.Difference != 4 {
		t.Errorf("first extra %+v", first)
	}

	file := filepath.Join(t.TempDir(), "plan.json")
	out, errOut, code = sapu(t, e, "", "similar", "--plan", file, "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "similar", out)

	out, errOut, code = sapu(t, e, "", "similar", "--pick-all", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "similar", out)
	var run runData
	decodeData(t, out, &run)
	if run.Result == nil || len(run.Result.Moved) != 2 {
		t.Errorf("data %+v", run)
	}

	// Nothing to find: groups is an empty list, not null.
	empty := env.New(t.TempDir())
	out, _, code = sapu(t, empty, "", "similar", "--pick-all", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "similar", out)
	if !strings.Contains(out, `"groups": []`) && !strings.Contains(out, `"groups":[]`) {
		t.Errorf("groups is not an empty list:\n%s", out)
	}
}
