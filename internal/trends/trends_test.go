package trends

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/walk"
)

const gb = 1_000_000_000

func dir(name string, bytes int64, children ...*walk.Node) *walk.Node {
	return &walk.Node{Name: name, Bytes: bytes, IsDir: true, Children: children}
}

// at sets the path of n and of every folder below it.
func at(parent string, n *walk.Node) *walk.Node {
	n.Path = filepath.Join(parent, n.Name)
	for _, c := range n.Children {
		at(n.Path, c)
	}
	return n
}

func TestFromTreeKeepsBigFoldersUpToDepth3(t *testing.T) {
	e := env.New("/home/u")
	tree := dir("u", 90*gb,
		dir("Library", 60*gb,
			dir("Caches", 50*gb,
				dir("CapCut", 40*gb,
					dir("deep", 30*gb)), // depth 4: left out
				dir("small", 10_000)), // under the minimum
		),
		dir(".sapu", 20*gb), // Sapu's own folder
		&walk.Node{Name: "big.bin", Bytes: 5 * gb},
	)
	tree.Path = "/home/u"
	for _, c := range tree.Children {
		at("/home/u", c)
	}
	s := FromTree(tree, e, time.Date(2026, 11, 8, 9, 0, 0, 0, time.Local))

	var got []string
	for _, f := range s.Folders {
		got = append(got, f.Path)
	}
	want := []string{"Library", "Library/Caches", "Library/Caches/CapCut"}
	if !slices.Equal(got, want) {
		t.Errorf("folders %v, want %v", got, want)
	}
	if s.HomeBytes != 90*gb || s.Date != "2026-11-08" || s.MinBytes != MinBytes {
		t.Errorf("snapshot %+v", s)
	}
}

func snap(date string, home int64, folders ...Folder) Snapshot {
	return Snapshot{Date: date, HomeBytes: home, MinBytes: MinBytes, Folders: folders}
}

func TestPickNewestAndOldEnough(t *testing.T) {
	dates := []string{"2026-11-08", "2026-11-05", "2026-11-01", "2026-10-20"}
	newer, older, ok := Pick(dates, 7)
	if !ok || newer != "2026-11-08" || older != "2026-11-01" {
		t.Errorf("7 days: %s %s %v; want 2026-11-08 2026-11-01 true", newer, older, ok)
	}
	// 2026-11-01 is exactly 7 days older: "at least" includes it.
	if _, older, _ := Pick(dates, 3); older != "2026-11-05" {
		t.Errorf("3 days: older %s, want 2026-11-05", older)
	}
	if _, older, _ := Pick(dates, 30); older != "" {
		t.Errorf("30 days: older %s, want none", older)
	}
	if _, _, ok := Pick(dates[:1], 7); ok {
		t.Error("one snapshot cannot be compared")
	}
	if _, _, ok := Pick(nil, 7); ok {
		t.Error("no snapshots cannot be compared")
	}
}

func paths(cs []Change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Path)
	}
	return out
}

func TestCompareRanksAndReportsDeepestFolder(t *testing.T) {
	older := snap("2026-11-01", 100*gb,
		Folder{Path: "Library", Bytes: 50 * gb},
		Folder{Path: "Library/Caches", Bytes: 20 * gb},
		Folder{Path: "Library/Caches/CapCut", Bytes: 5 * gb},
		Folder{Path: "Library/Caches/Spread", Bytes: 15 * gb},
		Folder{Path: "Movies", Bytes: 30 * gb},
		Folder{Path: "Downloads", Bytes: 10 * gb},
	)
	newer := snap("2026-11-08", 140*gb,
		Folder{Path: "Library", Bytes: 90 * gb},
		Folder{Path: "Library/Caches", Bytes: 60 * gb},
		Folder{Path: "Library/Caches/CapCut", Bytes: 43 * gb}, // +38: all of Caches' +40 is nearly this
		Folder{Path: "Library/Caches/Spread", Bytes: 17 * gb},
		Folder{Path: "Movies", Bytes: 20 * gb},            // -10
		Folder{Path: "Downloads", Bytes: 10 * gb},         // same
		Folder{Path: "Library/Caches/New", Bytes: 1 * gb}, // new: was under the minimum
	)
	d := Compare(newer, older, 10)

	if got, want := paths(d.Growth), []string{"Library/Caches/CapCut", "Library/Caches/Spread", "Library/Caches/New"}; !slices.Equal(got, want) {
		// Library and Caches are explained by CapCut and not listed.
		t.Errorf("growth %v, want %v", got, want)
	}
	if got := paths(d.Shrink); !slices.Equal(got, []string{"Movies"}) {
		t.Errorf("shrink %v, want [Movies]", got)
	}
	top := d.Growth[0]
	if top.Delta != 38*gb || top.Before != 5*gb || top.After != 43*gb || top.Approx {
		t.Errorf("top growth %+v", top)
	}
	if n := d.Growth[2]; !n.Approx || n.Before != 0 {
		t.Errorf("a folder missing from the old snapshot must be approximate: %+v", n)
	}
	if d.HomeBefore != 100*gb || d.HomeAfter != 140*gb || d.Older != "2026-11-01" || d.Newer != "2026-11-08" {
		t.Errorf("diff header %+v", d)
	}
}

func TestCompareKeepsParentWhenChangeIsSpread(t *testing.T) {
	older := snap("2026-11-01", 50*gb, Folder{Path: "Library", Bytes: 20 * gb}, Folder{Path: "Library/A", Bytes: 5 * gb}, Folder{Path: "Library/B", Bytes: 5 * gb})
	newer := snap("2026-11-08", 70*gb, Folder{Path: "Library", Bytes: 40 * gb}, Folder{Path: "Library/A", Bytes: 15 * gb}, Folder{Path: "Library/B", Bytes: 15 * gb})
	d := Compare(newer, older, 10)
	// A and B carry half each: neither explains Library alone.
	if got := paths(d.Growth); !slices.Equal(got, []string{"Library", "Library/A", "Library/B"}) {
		t.Errorf("growth %v", got)
	}
}

func TestCompareLimitsAndIgnoresSmallChanges(t *testing.T) {
	var of, nf []Folder
	for i := range 15 {
		name := string(rune('a' + i))
		of = append(of, Folder{Path: name, Bytes: 1 * gb})
		nf = append(nf, Folder{Path: name, Bytes: int64(1+i+1) * gb})
	}
	of = append(of, Folder{Path: "tiny", Bytes: 1 * gb})
	nf = append(nf, Folder{Path: "tiny", Bytes: 1*gb + 1_000_000})
	d := Compare(snap("2026-11-08", 0, nf...), snap("2026-11-01", 0, of...), 10)
	if len(d.Growth) != 10 {
		t.Fatalf("%d growth rows, want 10", len(d.Growth))
	}
	if d.Growth[0].Path != "o" || d.Growth[0].Delta != 15*gb {
		t.Errorf("biggest %+v", d.Growth[0])
	}
	for _, c := range d.Growth {
		if c.Path == "tiny" {
			t.Error("a 1 MB change is not reported")
		}
	}
	if d.Shrink == nil {
		t.Error("shrink must be an empty list, not nil, for JSON")
	}
}

func TestRecordWritesOneFileADay(t *testing.T) {
	e := env.New(t.TempDir())
	if err := os.WriteFile(filepath.Join(e.Home, "a.txt"), make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 8, 8, 0, 0, 0, time.Local)

	if Has(e, day) {
		t.Fatal("Has before any snapshot")
	}
	first, err := Record(context.Background(), e, day, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.HomeBytes <= 0 {
		t.Errorf("home bytes %d", first.HomeBytes)
	}
	if err := os.WriteFile(filepath.Join(e.Home, "b.txt"), make([]byte, 50_000), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Record(context.Background(), e, day.Add(5*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.HomeBytes <= first.HomeBytes {
		t.Errorf("the second snapshot of the day must replace the first: %d then %d", first.HomeBytes, second.HomeBytes)
	}

	entries, err := os.ReadDir(Dir(e))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "2026-11-08.json" {
		t.Fatalf("files in %s: %v", Dir(e), entries)
	}
	got, err := Load(e, "2026-11-08")
	if err != nil || got.HomeBytes != second.HomeBytes {
		t.Errorf("loaded %+v, %v", got, err)
	}
	if !Has(e, day.Add(10*time.Hour)) {
		t.Error("Has after a snapshot")
	}

	// The next day is a new file.
	if _, err := Record(context.Background(), e, day.AddDate(0, 0, 1), nil); err != nil {
		t.Fatal(err)
	}
	dates, err := Dates(e)
	if err != nil || !slices.Equal(dates, []string{"2026-11-09", "2026-11-08"}) {
		t.Errorf("dates %v, %v", dates, err)
	}
}

func TestRecordCancelledWritesNothing(t *testing.T) {
	e := env.New(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Record(ctx, e, time.Now(), nil); err == nil {
		t.Fatal("a cancelled walk must fail")
	}
	if _, err := os.Stat(Dir(e)); err == nil {
		t.Error("a cancelled walk wrote the trends folder")
	}
}

func TestDatesIgnoresOtherFiles(t *testing.T) {
	e := env.New(t.TempDir())
	if dates, err := Dates(e); err != nil || dates != nil {
		t.Errorf("no folder: %v, %v", dates, err)
	}
	if err := os.MkdirAll(Dir(e), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2026-11-01.json", "notes.json", "2026-13-45.json", "2026-11-02.txt"} {
		if err := os.WriteFile(filepath.Join(Dir(e), name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dates, err := Dates(e)
	if err != nil || !slices.Equal(dates, []string{"2026-11-01"}) {
		t.Errorf("dates %v, %v", dates, err)
	}
}

func TestChangesErrors(t *testing.T) {
	e := env.New(t.TempDir())
	if _, err := Changes(e, 7, 10); !errors.Is(err, ErrNoSnapshots) {
		t.Errorf("no snapshots: %v", err)
	}
	if err := Save(e, snap("2026-11-08", gb)); err != nil {
		t.Fatal(err)
	}
	if _, err := Changes(e, 7, 10); !errors.Is(err, ErrTooRecent) {
		t.Errorf("one snapshot: %v", err)
	}
	if err := Save(e, snap("2026-11-01", gb, Folder{Path: "Movies", Bytes: 2 * gb})); err != nil {
		t.Fatal(err)
	}
	d, err := Changes(e, 7, 10)
	if err != nil || d.Older != "2026-11-01" || len(d.Shrink) != 1 {
		t.Errorf("diff %+v, %v", d, err)
	}
}

// A part of home that never answers (on the owner's Mac, an iCloud Drive
// folder blocked every listing) is left out and named, and the snapshot of
// everything else is still saved.
func TestRecordLeavesOutAPartThatDoesNotAnswer(t *testing.T) {
	e := env.New(t.TempDir())
	for _, rel := range []string{"Documents/a.bin", "Library/Caches/x/b.bin", "Library/Mobile Documents/y/c.bin"} {
		p := filepath.Join(e.Home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 50_000), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stuck := filepath.Join(e.Home, "Library", "Mobile Documents")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	oldWalk, oldTimeout := walkTree, PartTimeout
	walkTree = func(ctx context.Context, path string, progress walk.Progress) (*walk.Node, error) {
		if path == stuck {
			<-release // like a system call that cannot be cancelled
			return nil, errors.New("released")
		}
		return walk.Tree(ctx, path, progress)
	}
	PartTimeout = 100 * time.Millisecond
	t.Cleanup(func() { walkTree, PartTimeout = oldWalk, oldTimeout })

	s, err := Record(context.Background(), e, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.Unmeasured, []string{"Library/Mobile Documents"}) {
		t.Errorf("unmeasured %q", s.Unmeasured)
	}
	if s.HomeBytes <= 0 {
		t.Errorf("home bytes %d, want the measured parts", s.HomeBytes)
	}
	saved, err := Load(e, s.Date)
	if err != nil || !slices.Equal(saved.Unmeasured, s.Unmeasured) {
		t.Errorf("saved %+v, %v", saved, err)
	}
}

func TestCompareSkipsPartsLeftOut(t *testing.T) {
	older := Snapshot{Date: "2026-11-01", Folders: []Folder{
		{Path: "Library", Bytes: 10_000_000_000},
		{Path: "Library/Mobile Documents", Bytes: 5_000_000_000},
		{Path: "Library/Caches", Bytes: 2_000_000_000},
	}}
	newer := Snapshot{Date: "2026-11-08", Unmeasured: []string{"Library/Mobile Documents"}, Folders: []Folder{
		{Path: "Library", Bytes: 7_000_000_000},
		{Path: "Library/Caches", Bytes: 4_000_000_000},
	}}
	d := Compare(newer, older, 10)
	if len(d.Growth) != 1 || d.Growth[0].Path != "Library/Caches" {
		t.Errorf("growth %+v, want only Library/Caches", d.Growth)
	}
	if len(d.Shrink) != 0 {
		t.Errorf("shrink %+v: a part left out must not look like a shrink", d.Shrink)
	}
	if !slices.Equal(d.Unmeasured, []string{"Library/Mobile Documents"}) {
		t.Errorf("unmeasured %q", d.Unmeasured)
	}
}

// M2 review: a folder that becomes unreadable must not look like it shrank.
// Unreadable entries are counted per folder, and diff skips a folder whose
// count changed between snapshots.
func TestUnreadableEntriesAreCountedAndChangesSkipped(t *testing.T) {
	e := env.New(t.TempDir())
	older := Snapshot{Date: "2026-11-01", Folders: []Folder{
		{Path: "Library", Bytes: 9_000_000_000},
		{Path: "Library/Containers", Bytes: 5_000_000_000},
		{Path: "Library/Caches", Bytes: 2_000_000_000, Errors: 3},
	}}
	newer := Snapshot{Date: "2026-11-08", Folders: []Folder{
		{Path: "Library", Bytes: 6_000_000_000, Errors: 4},
		{Path: "Library/Containers", Bytes: 1_000_000_000, Errors: 1}, // became partly unreadable
		{Path: "Library/Caches", Bytes: 5_000_000_000, Errors: 3},     // same readability: compared
	}}
	d := Compare(newer, older, 10)
	if len(d.Growth) != 1 || d.Growth[0].Path != "Library/Caches" {
		t.Errorf("growth %+v, want only Library/Caches", d.Growth)
	}
	if len(d.Shrink) != 0 {
		t.Errorf("shrink %+v: a folder that became unreadable must not look like a shrink", d.Shrink)
	}
	if !slices.Equal(d.Unreadable, []string{"Library"}) {
		t.Errorf("unreadable %q, want the topmost changed folder", d.Unreadable)
	}

	// Record keeps measuring a partly unreadable part and counts the errors.
	for _, rel := range []string{"Library/Containers/app/secret/c.bin", "Library/Containers/app/open/d.bin"} {
		p := filepath.Join(e.Home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(e.Home, "Library", "Containers", "app", "secret")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	s, err := Record(context.Background(), e, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Unmeasured) != 0 {
		t.Errorf("unmeasured %q: a partly unreadable part is still measured", s.Unmeasured)
	}
}

// M2 review: a snapshot file that is a symlink to a document must not
// overwrite the document.
func TestSaveRefusesASymlinkedSnapshot(t *testing.T) {
	e := env.New(t.TempDir())
	if err := os.MkdirAll(Dir(e), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(e.Home, "thesis.txt")
	if err := os.WriteFile(doc, []byte("my thesis"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(doc, filepath.Join(Dir(e), "2026-11-08.json")); err != nil {
		t.Fatal(err)
	}
	err := Save(e, Snapshot{Date: "2026-11-08", Folders: []Folder{}})
	if err == nil {
		t.Fatal("Save wrote through a symlink")
	}
	if b, _ := os.ReadFile(doc); string(b) != "my thesis" {
		t.Fatalf("the document was overwritten: %q", b)
	}
}
