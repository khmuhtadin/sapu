package dupes

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// write creates a file under the home. Files made one after the other get
// birth times in that order.
func write(t *testing.T, e env.Env, rel string, data []byte) string {
	t.Helper()
	path := filepath.Join(e.Home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	return path
}

// content makes size bytes that depend on seed.
func content(seed byte, size int) []byte {
	return bytes.Repeat([]byte{seed}, size)
}

func find(t *testing.T, e env.Env, keep Keep, roots ...string) []Group {
	t.Helper()
	if len(roots) == 0 {
		roots = []string{e.Home}
	}
	groups, err := Find(context.Background(), e, roots, Options{MinSize: 1000, Keep: keep})
	if err != nil {
		t.Fatal(err)
	}
	return groups
}

// paths lists keeper and extras of every group, one line per group.
func paths(e env.Env, groups []Group) []string {
	var lines []string
	for _, g := range groups {
		parts := []string{strings.TrimPrefix(g.Keep.Path, e.Home+"/")}
		for _, f := range g.Extras {
			parts = append(parts, strings.TrimPrefix(f.Path, e.Home+"/"))
		}
		lines = append(lines, strings.Join(parts, " | "))
	}
	slices.Sort(lines)
	return lines
}

func wantGroups(t *testing.T, e env.Env, groups []Group, want ...string) {
	t.Helper()
	got := paths(e, groups)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("groups:\n got %q\nwant %q", got, want)
	}
}

func TestGroupsAcrossFolders(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, e, "Downloads/a.bin", content('a', 5000))
	write(t, e, "Documents/deep/a-copy.bin", content('a', 5000))
	write(t, e, "Desktop/b.bin", content('b', 3000))
	write(t, e, "Pictures/b.bin", content('b', 3000))
	write(t, e, "Pictures/unique.bin", content('u', 4000))

	groups := find(t, e, KeepOldest)
	wantGroups(t, e, groups,
		"Downloads/a.bin | Documents/deep/a-copy.bin",
		"Desktop/b.bin | Pictures/b.bin")
	// the largest group first
	if groups[0].Keep.Path != filepath.Join(e.Home, "Downloads/a.bin") {
		t.Errorf("first group is %s", groups[0].Keep.Path)
	}
}

func TestSameSizeDifferentContentIsNotADuplicate(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, e, "x.bin", content('x', 5000))
	write(t, e, "y.bin", content('y', 5000)) // differs in every byte: the edges differ

	// Larger than both edges together, so only the middle differs: the edges
	// match and the full hash has to tell them apart.
	big := content('m', 300<<10)
	write(t, e, "big1.bin", big)
	other := slices.Clone(big)
	other[150<<10] = 'z'
	write(t, e, "big2.bin", other)
	write(t, e, "big3.bin", big)

	wantGroups(t, e, find(t, e, KeepOldest), "big1.bin | big3.bin")
}

func TestSmallFilesAreIgnored(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, e, "a.bin", content('a', 999))
	write(t, e, "b.bin", content('a', 999))
	wantGroups(t, e, find(t, e, KeepOldest))
}

func TestHardLinksAreOneFile(t *testing.T) {
	e := env.New(t.TempDir())
	a := write(t, e, "a.bin", content('a', 5000))
	if err := os.Link(a, filepath.Join(e.Home, "link.bin")); err != nil {
		t.Fatal(err)
	}
	wantGroups(t, e, find(t, e, KeepOldest))

	// A real copy of the linked file is one duplicate, not two.
	write(t, e, "copy.bin", content('a', 5000))
	groups := find(t, e, KeepOldest)
	if len(groups) != 1 || len(groups[0].Extras) != 1 {
		t.Errorf("groups: %q", paths(e, groups))
	}
}

func TestSameFolderGivenTwiceCountsOnce(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, e, "d/a.bin", content('a', 5000))
	write(t, e, "d/b.bin", content('a', 5000))
	groups := find(t, e, KeepOldest, filepath.Join(e.Home, "d"), e.Home)
	wantGroups(t, e, groups, "d/a.bin | d/b.bin")
}

func TestSymlinksAreNotFollowed(t *testing.T) {
	e := env.New(t.TempDir())
	a := write(t, e, "a.bin", content('a', 5000))
	if err := os.Symlink(a, filepath.Join(e.Home, "link-to-file.bin")); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(e.Home, "outside")
	write(t, e, "outside/a-too.bin", content('a', 5000))
	if err := os.Symlink(other, filepath.Join(e.Home, "link-to-folder")); err != nil {
		t.Fatal(err)
	}
	// Only the real files under outside/ and the root: a.bin and a-too.bin.
	wantGroups(t, e, find(t, e, KeepOldest), "a.bin | outside/a-too.bin")

	// A root that is a symlink is refused.
	_, err := Find(context.Background(), e, []string{filepath.Join(e.Home, "link-to-folder")}, Options{MinSize: 1000, Keep: KeepOldest})
	if err == nil {
		t.Error("a symlink root was accepted")
	}
}

func TestExcludedFoldersAreNotSearched(t *testing.T) {
	e := env.New(t.TempDir())
	data := content('a', 5000)
	write(t, e, "Documents/real.bin", data)
	write(t, e, "Documents/real-copy.bin", data)
	for _, rel := range []string{
		"Library/Caches/x.bin",
		"Documents/proj/node_modules/pkg/x.bin",
		"Documents/proj/.git/objects/x.bin",
		"Applications/Thing.app/Contents/x.bin",
		"Pictures/Photos Library.photoslibrary/originals/x.bin",
		"Music/Library.MUSICLIBRARY/x.bin",
		"Music/Music/Media/x.bin",
		"Music/iTunes/x.bin",
		".sapu/quarantine/run/x.bin",
	} {
		write(t, e, rel, data)
	}
	// Music outside the library folders is searched.
	write(t, e, "Music/song.bin", data)

	groups := find(t, e, KeepOldest)
	if len(groups) != 1 {
		t.Fatalf("groups: %q", paths(e, groups))
	}
	got := []string{groups[0].Keep.Path}
	for _, f := range groups[0].Extras {
		got = append(got, f.Path)
	}
	for _, p := range got {
		rel := strings.TrimPrefix(p, e.Home+"/")
		if !slices.Contains([]string{"Documents/real.bin", "Documents/real-copy.bin", "Music/song.bin"}, rel) {
			t.Errorf("%s was searched", rel)
		}
	}
	if len(got) != 3 {
		t.Errorf("copies: %q", got)
	}
}

func TestRootInsideAnExcludedFolderIsAnError(t *testing.T) {
	e := env.New(t.TempDir())
	for _, rel := range []string{"Library/Caches", "p/node_modules/pkg", "Music/Music", "Pictures/x.photoslibrary", "Applications/T.app"} {
		write(t, e, rel+"/f.bin", content('a', 5000))
		_, err := Find(context.Background(), e, []string{filepath.Join(e.Home, rel)}, Options{MinSize: 1000, Keep: KeepOldest})
		if err == nil || !strings.Contains(err.Error(), "never searched") {
			t.Errorf("%s: error %v", rel, err)
		}
	}
	if _, err := Find(context.Background(), e, []string{filepath.Join(e.Home, "missing")}, Options{MinSize: 1000}); err == nil {
		t.Error("a missing root was accepted")
	}
}

func TestKeepPolicies(t *testing.T) {
	e := env.New(t.TempDir())
	data := content('a', 5000)
	// Created in this order: the first is the oldest.
	write(t, e, "long/folder/name/first.bin", data)
	write(t, e, "mid/second.bin", data)
	write(t, e, "z.bin", data)

	cases := []struct {
		keep Keep
		want string
	}{
		{KeepOldest, "long/folder/name/first.bin"},
		{KeepNewest, "z.bin"},
		{KeepShortestPath, "z.bin"},
	}
	for _, c := range cases {
		groups := find(t, e, c.keep)
		if len(groups) != 1 {
			t.Fatalf("%s: groups %q", c.keep, paths(e, groups))
		}
		if got := strings.TrimPrefix(groups[0].Keep.Path, e.Home+"/"); got != c.want {
			t.Errorf("--keep %s keeps %s, want %s", c.keep, got, c.want)
		}
		if len(groups[0].Extras) != 2 {
			t.Errorf("%s: %d extras", c.keep, len(groups[0].Extras))
		}
	}
}

func TestKeepTiesGoToShortestPathThenPathOrder(t *testing.T) {
	// The same birth time, so only the path decides.
	files := []File{
		{Path: "/h/bb/x.bin", born: 5},
		{Path: "/h/b/x.bin", born: 5},
		{Path: "/h/a/x.bin", born: 5},
	}
	for _, keep := range []Keep{KeepOldest, KeepNewest, KeepShortestPath} {
		sorted := slices.Clone(files)
		sortCopies(sorted, keep)
		if sorted[0].Path != "/h/a/x.bin" || sorted[1].Path != "/h/b/x.bin" {
			t.Errorf("%s: %v", keep, sorted)
		}
	}
}

func TestItems(t *testing.T) {
	e := env.New(t.TempDir())
	keep := write(t, e, "a.bin", content('a', 5000))
	extra := write(t, e, "b.bin", content('a', 5000))

	items := Items(find(t, e, KeepOldest))
	if len(items) != 1 {
		t.Fatalf("%d items", len(items))
	}
	it := items[0]
	wantID, err := plan.IdentityOf(extra)
	if err != nil {
		t.Fatal(err)
	}
	keepID, err := plan.IdentityOf(keep)
	if err != nil {
		t.Fatal(err)
	}
	if it.Source != "dupes" || it.Path != extra || it.Risk != rules.RiskRisky || it.Action != rules.ActionQuarantine || it.Identity != wantID {
		t.Errorf("item: %+v", it)
	}
	if it.KeepCopy == nil || it.KeepCopy.Path != keep || it.KeepCopy.Identity != keepID {
		t.Errorf("keep copy: %+v", it.KeepCopy)
	}
	if want := "Same content as " + keep + ". The copy listed first stays."; it.Explain != want {
		t.Errorf("explain %q, want %q", it.Explain, want)
	}
	if it.Bytes <= 0 {
		t.Errorf("bytes %d", it.Bytes)
	}
}

func TestCancel(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, e, "a.bin", content('a', 5000))
	write(t, e, "b.bin", content('a', 5000))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	groups, err := Find(ctx, e, []string{e.Home}, Options{MinSize: 1000, Keep: KeepOldest})
	if err == nil || groups != nil {
		t.Errorf("groups %v, err %v", groups, err)
	}
}

func TestDefaultRootsAreTheOnesThatExist(t *testing.T) {
	e := env.New(t.TempDir())
	for _, name := range []string{"Downloads", "Documents", "Library"} {
		if err := os.MkdirAll(filepath.Join(e.Home, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{filepath.Join(e.Home, "Downloads"), filepath.Join(e.Home, "Documents")}
	got := DefaultRoots(e)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("roots %q, want %q", got, want)
	}
}

func TestProgress(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, e, "a.bin", content('a', 5000))
	write(t, e, "b.bin", content('a', 5000))
	var last int64
	_, err := Find(context.Background(), e, []string{e.Home}, Options{
		MinSize: 1000, Keep: KeepOldest,
		Scan: func(ev walk.Event) { last = ev.Files },
	})
	if err != nil {
		t.Fatal(err)
	}
	if last != 2 {
		t.Errorf("the last scan event counted %d files, want 2", last)
	}
}

// Copies inside code projects are usually files git tracks; moving them
// breaks the project. Found on the owner's Mac: the same seed files in six
// project folders.
func TestGitRepositoriesAreNotSearched(t *testing.T) {
	e := env.New(t.TempDir())
	data := content('r', 5000)
	write(t, e, "Documents/a.bin", data)
	write(t, e, "Documents/b.bin", data)
	write(t, e, "Documents/proj1/.git/HEAD", []byte("ref"))
	write(t, e, "Documents/proj1/seed.sql", data)
	write(t, e, "Documents/proj2/.git", []byte("gitdir: /elsewhere")) // a worktree
	write(t, e, "Documents/proj2/seed.sql", data)

	groups := find(t, e, KeepOldest)
	if len(groups) != 1 || len(groups[0].Extras) != 1 {
		t.Fatalf("groups: %q", paths(e, groups))
	}
	for _, p := range append([]string{groups[0].Keep.Path}, groups[0].Extras[0].Path) {
		if strings.Contains(p, "proj") {
			t.Errorf("%s inside a repository was searched", p)
		}
	}

	_, err := Find(t.Context(), e, []string{filepath.Join(e.Home, "Documents", "proj1")}, Options{MinSize: 1, Keep: KeepOldest})
	if err == nil || !strings.Contains(err.Error(), "git repository") {
		t.Errorf("a root inside a repository: err %v", err)
	}
}
