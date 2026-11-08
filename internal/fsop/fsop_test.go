package fsop

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

var now = time.Date(2026, 11, 8, 11, 15, 0, 0, time.Local)

// Invariant 1 and 4: undo of 10,000 files is identical.
func TestUndoTenThousandFilesIsIdentical(t *testing.T) {
	e := newHome(t)
	data := filepath.Join(e.Home, "data")
	n := buildTree(t, data, 60, 10_000, 1)
	if n < 10_000 {
		t.Fatalf("tree has %d files, want at least 10000", n)
	}
	before := snapshot(t, data)
	items := itemsFor(t, data)

	id := quarantineAll(t, e, items, now)

	left, err := os.ReadDir(data)
	must(t, err)
	if len(left) != 0 {
		t.Fatalf("%d entries left in data after quarantine", len(left))
	}
	inQuarantine, err := os.ReadDir(filepath.Join(e.QuarantineDir(), id))
	must(t, err)
	if len(inQuarantine) != len(items) {
		t.Fatalf("quarantine holds %d items, want %d", len(inQuarantine), len(items))
	}
	sizes, err := QuarantineSize(e)
	must(t, err)
	if sizes[id] != int64(100*len(items)) {
		t.Fatalf("QuarantineSize = %v, want %d for %s", sizes, 100*len(items), id)
	}
	s := summary(t, e, id)
	if s.Status != runlog.StatusDone || s.Items != len(items) || s.Bytes != int64(100*len(items)) {
		t.Fatalf("summary after apply = %+v", s)
	}

	restored, err := Restore(e, id)
	must(t, err)
	if len(restored) != len(items) {
		t.Fatalf("restored %d items, want %d", len(restored), len(items))
	}
	for _, r := range restored {
		if r.Conflict {
			t.Fatalf("unexpected conflict: %+v", r)
		}
	}
	requireSame(t, before, snapshot(t, data))
	if got := summary(t, e, id).Status; got != runlog.StatusUndone {
		t.Fatalf("status after undo = %q, want undone", got)
	}
	if exists(t, filepath.Join(e.QuarantineDir(), id)) {
		t.Fatal("empty quarantine folder of the run was left behind")
	}
	sizes, err = QuarantineSize(e)
	must(t, err)
	if len(sizes) != 0 {
		t.Fatalf("QuarantineSize after undo = %v, want none", sizes)
	}
}

// Invariant 4: a crash after any journal write or rename can be undone, and
// so can a crash in the middle of undo itself.
func TestCrashAtEveryStepIsUndone(t *testing.T) {
	const items = 6
	steps := 3 * items // move_begin, rename, move_done per item
	for k := 1; k <= steps; k++ {
		for _, torn := range []bool{false, true} {
			t.Run("quarantine/step"+strconv.Itoa(k)+tornName(torn), func(t *testing.T) {
				e := newHome(t)
				data := filepath.Join(e.Home, "data")
				buildTree(t, data, items, 60, uint64(k))
				before := snapshot(t, data)
				list := itemsFor(t, data)

				run := startRun(t, e, list, now)
				setHook(t, k)
				crashed := false
				for _, it := range list {
					if err := Quarantine(e, run, it); err != nil {
						if !errors.Is(err, errCrash) {
							t.Fatal(err)
						}
						crashed = true
						break
					}
				}
				if !crashed {
					t.Fatalf("hook step %d never ran", k)
				}
				must(t, run.Close(now, false)) // the process died: no end event
				if torn {
					appendTorn(t, e, run.ID)
				}
				afterStep = func() error { return nil }

				if got := summary(t, e, run.ID).Status; got != runlog.StatusInterrupted {
					t.Fatalf("status after crash = %q, want interrupted", got)
				}
				_, err := Restore(e, run.ID)
				must(t, err)
				requireSame(t, before, snapshot(t, data))
			})
		}
	}
	for k := 1; k <= steps; k++ {
		t.Run("restore/step"+strconv.Itoa(k), func(t *testing.T) {
			e := newHome(t)
			data := filepath.Join(e.Home, "data")
			buildTree(t, data, items, 60, uint64(100+k))
			before := snapshot(t, data)
			id := quarantineAll(t, e, itemsFor(t, data), now)

			setHook(t, k)
			if _, err := Restore(e, id); !errors.Is(err, errCrash) {
				t.Fatalf("first undo: err = %v, want the simulated crash", err)
			}
			afterStep = func() error { return nil }
			_, err := Restore(e, id)
			must(t, err)
			requireSame(t, before, snapshot(t, data))
		})
	}
}

func tornName(torn bool) string {
	if torn {
		return "/torn"
	}
	return ""
}

// Invariant 3.
func TestQuarantineRefusesPathsOutsideItsArea(t *testing.T) {
	e := newHome(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "secret")
	writeFile(t, filepath.Join(e.Home, "data", "a.txt"), "a")
	must(t, os.Symlink(outside, filepath.Join(e.Home, "escape")))
	must(t, os.Symlink(e.StateDir, filepath.Join(e.Home, "state-link")))
	writeFile(t, filepath.Join(e.Home, ".sapu-quarantine", "501", "x"), "x")

	run := startRun(t, e, nil, now) // creates the state folder
	writeFile(t, filepath.Join(e.QuarantineDir(), "20200101-000000-abcd", "0001-old"), "old")

	cases := map[string]string{
		"home itself":                 e.Home,
		"root":                        "/",
		"relative":                    "data/a.txt",
		"not clean":                   e.Home + "/data/../data/a.txt",
		"trailing slash":              e.Home + "/data/",
		"state folder":                e.StateDir,
		"inside state folder":         e.RunsDir(),
		"journal":                     filepath.Join(e.RunsDir(), run.ID, "journal.jsonl"),
		"quarantine folder":           e.QuarantineDir(),
		"inside quarantine":           filepath.Join(e.QuarantineDir(), "20200101-000000-abcd", "0001-old"),
		"volume quarantine folder":    filepath.Join(e.Home, ".sapu-quarantine", "501", "x"),
		"outside home":                filepath.Join(outside, "secret.txt"),
		"volume root":                 "/Volumes/Data",
		"parent links outside home":   filepath.Join(e.Home, "escape", "secret.txt"),
		"parent links to state":       filepath.Join(e.Home, "state-link", "runs"),
		"home through other spelling": e.Home + "/.",
	}
	if exists(t, filepath.Join(e.Home, ".SAPU")) { // APFS ignores case by default
		cases["state folder in other case"] = filepath.Join(e.Home, ".SAPU", "runs")
	}
	before := snapshotWithout(t, e.Home, "Library")
	beforeOutside := snapshot(t, outside)
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			it := plan.Item{ID: "0001", Path: path}
			if ident, err := plan.IdentityOf(path); err == nil {
				it.Identity = ident
			}
			err := Quarantine(e, run, it)
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Quarantine(%s) = %v, want ErrRefused", path, err)
			}
		})
	}
	t.Run("bad item id", func(t *testing.T) {
		it := itemFor(t, "../x", filepath.Join(e.Home, "data", "a.txt"), 1)
		if err := Quarantine(e, run, it); !errors.Is(err, ErrRefused) {
			t.Fatalf("err = %v, want ErrRefused", err)
		}
	})
	t.Run("item contains the state folder", func(t *testing.T) {
		e2 := e
		e2.StateDir = filepath.Join(e.Home, "Library", "sapu")
		run2 := startRun(t, e2, nil, now)
		defer func() { must(t, run2.Close(now, false)) }()
		it := itemFor(t, "0001", filepath.Join(e.Home, "Library"), 1)
		if err := Quarantine(e2, run2, it); !errors.Is(err, ErrRefused) {
			t.Fatalf("err = %v, want ErrRefused", err)
		}
		requireSame(t, before, snapshotWithout(t, e.Home, "Library"))
	})
	must(t, run.Close(now, true))

	requireSame(t, before, snapshotWithout(t, e.Home, "Library"))
	requireSame(t, beforeOutside, snapshot(t, outside))
	if readFile(t, filepath.Join(e.QuarantineDir(), "20200101-000000-abcd", "0001-old")) != "old" {
		t.Fatal("a quarantined file was touched")
	}
	if got := journalTypes(t, e, run.ID); strings.Join(got, ",") != "start,end" {
		t.Fatalf("journal = %v, want only start and end", got)
	}
}

// snapshotWithout leaves out one top-level entry and the files the runs write.
func snapshotWithout(t *testing.T, root, skip string) map[string]entry {
	t.Helper()
	s := snapshot(t, root)
	for p := range s {
		if p == skip || strings.HasPrefix(p, skip+"/") || p == ".sapu" || strings.HasPrefix(p, ".sapu/") {
			delete(s, p)
		}
	}
	return s
}

// Invariant 2.
func TestQuarantineChangedOrGone(t *testing.T) {
	e := newHome(t)
	changed := filepath.Join(e.Home, "data", "changed.txt")
	replaced := filepath.Join(e.Home, "data", "replaced.txt")
	gone := filepath.Join(e.Home, "data", "gone.txt")
	goneDir := filepath.Join(e.Home, "data", "gone-dir", "x.txt")
	for _, p := range []string{changed, replaced, gone, goneDir} {
		writeFile(t, p, "v1")
	}
	items := []plan.Item{
		itemFor(t, "0001", changed, 2),
		itemFor(t, "0002", replaced, 2),
		itemFor(t, "0003", gone, 2),
		itemFor(t, "0004", goneDir, 2),
	}
	run := startRun(t, e, items, now)
	defer func() { must(t, run.Close(now, true)) }()

	must(t, os.WriteFile(changed, []byte("v2, longer"), 0o644))
	must(t, os.Remove(replaced))
	writeFile(t, replaced, "v1") // same content, new inode
	must(t, os.Remove(gone))
	must(t, os.RemoveAll(filepath.Dir(goneDir)))

	for i, want := range []error{ErrChanged, ErrChanged, ErrGone, ErrGone} {
		if err := Quarantine(e, run, items[i]); !errors.Is(err, want) {
			t.Fatalf("%s: err = %v, want %v", items[i].Path, err, want)
		}
	}
	if readFile(t, changed) != "v2, longer" || readFile(t, replaced) != "v1" {
		t.Fatal("a changed item was touched")
	}
	if got := journalTypes(t, e, run.ID); len(got) != 1 {
		t.Fatalf("journal = %v, want only start", got)
	}
}

// Invariant 5.
func TestRestoreNeverOverwrites(t *testing.T) {
	e := newHome(t)
	file := filepath.Join(e.Home, "data", "a.txt")
	dir := filepath.Join(e.Home, "data", "cache")
	writeFile(t, file, "old file")
	writeFile(t, filepath.Join(dir, "x"), "old dir")
	id := quarantineAll(t, e, []plan.Item{itemFor(t, "0001", file, 8), itemFor(t, "0002", dir, 7)}, now)

	writeFile(t, file, "new file")
	writeFile(t, filepath.Join(dir, "y"), "new dir")

	restored, err := Restore(e, id)
	must(t, err)
	if len(restored) != 2 {
		t.Fatalf("restored = %+v", restored)
	}
	for _, r := range restored {
		if !r.Conflict || !strings.HasSuffix(r.To, " (sapu "+id+")") {
			t.Fatalf("want a conflict restore beside the original, got %+v", r)
		}
	}
	if readFile(t, file) != "new file" || readFile(t, file+" (sapu "+id+")") != "old file" {
		t.Fatal("file conflict: wrong contents")
	}
	if readFile(t, filepath.Join(dir, "y")) != "new dir" || exists(t, filepath.Join(dir, "x")) {
		t.Fatal("folder conflict: the new folder was changed")
	}
	if readFile(t, filepath.Join(dir+" (sapu "+id+")", "x")) != "old dir" {
		t.Fatal("folder conflict: old folder not restored beside the new one")
	}
}

func TestRestoreConflictPathTakenStaysInQuarantine(t *testing.T) {
	e := newHome(t)
	file := filepath.Join(e.Home, "a.txt")
	writeFile(t, file, "old")
	id := quarantineAll(t, e, []plan.Item{itemFor(t, "0001", file, 3)}, now)
	writeFile(t, file, "new")
	writeFile(t, file+" (sapu "+id+")", "also taken")

	if _, err := Restore(e, id); err == nil {
		t.Fatal("Restore succeeded although both target names were taken")
	}
	if readFile(t, file) != "new" || readFile(t, file+" (sapu "+id+")") != "also taken" {
		t.Fatal("an existing file was overwritten")
	}
	if readFile(t, filepath.Join(e.QuarantineDir(), id, "0001-a.txt")) != "old" {
		t.Fatal("the item left quarantine")
	}
}

func TestRestoreRecreatesMissingParent(t *testing.T) {
	e := newHome(t)
	file := filepath.Join(e.Home, "deep", "er", "a.txt")
	writeFile(t, file, "a")
	id := quarantineAll(t, e, []plan.Item{itemFor(t, "0001", file, 1)}, now)
	must(t, os.RemoveAll(filepath.Join(e.Home, "deep")))

	_, err := Restore(e, id)
	must(t, err)
	if readFile(t, file) != "a" {
		t.Fatal("item not restored")
	}
	if _, err := Restore(e, id); err != nil {
		t.Fatalf("second undo: %v", err)
	}
}

func TestRestoreRefusesEmptiedRun(t *testing.T) {
	e := newHome(t)
	file := filepath.Join(e.Home, "a.txt")
	writeFile(t, file, "a")
	id := quarantineAll(t, e, []plan.Item{itemFor(t, "0001", file, 1)}, now)
	_, err := Empty(e, id)
	must(t, err)
	if _, err := Restore(e, id); err == nil {
		t.Fatal("Restore of an emptied run succeeded")
	}
}

func TestRestoreRejectsBadRunID(t *testing.T) {
	e := newHome(t)
	for _, id := range []string{"", "..", "../x", "20261108-111500-zzzz"} {
		if _, err := Restore(e, id); err == nil {
			t.Fatalf("Restore(%q) succeeded", id)
		}
		if _, err := Empty(e, id); err == nil {
			t.Fatalf("Empty(%q) succeeded", id)
		}
	}
}

// Invariant 6.
func TestEmptyDeletesOnlyQuarantine(t *testing.T) {
	e := newHome(t)
	writeFile(t, filepath.Join(e.Home, "Documents", "keep.txt"), "keep")
	writeFile(t, filepath.Join(e.Home, "data", "keep.txt"), "keep")
	a := filepath.Join(e.Home, "a")
	b := filepath.Join(e.Home, "b.txt")
	writeFile(t, filepath.Join(a, "x"), "x")
	writeFile(t, b, "b")
	other := quarantineAll(t, e, []plan.Item{itemFor(t, "0001", filepath.Join(e.Home, "data", "keep.txt"), 4)}, now.Add(-time.Hour))
	_, err := Restore(e, other) // a second, undone run
	must(t, err)

	before := snapshotWithout(t, e.Home, "a")
	delete(before, "b.txt")
	id := quarantineAll(t, e, []plan.Item{itemFor(t, "0001", a, 10), itemFor(t, "0002", b, 20)}, now)

	freed, err := Empty(e, id)
	must(t, err)
	if freed != 30 {
		t.Fatalf("freed = %d, want 30", freed)
	}
	if exists(t, filepath.Join(e.QuarantineDir(), id)) {
		t.Fatal("quarantine of the run still exists")
	}
	requireSame(t, before, snapshotWithout(t, e.Home, "a"))
	if s := summary(t, e, id); s.Status != runlog.StatusEmptied {
		t.Fatalf("status = %q, want emptied", s.Status)
	}
	if freed, err := Empty(e, id); err != nil || freed != 0 {
		t.Fatalf("second Empty = %d, %v; want 0, nil", freed, err)
	}
}

func TestEmptyRefusesJournalPointingOutsideQuarantine(t *testing.T) {
	e := newHome(t)
	precious := filepath.Join(e.Home, "precious", "0001-photo.jpg")
	writeFile(t, precious, "photo")
	run := startRun(t, e, nil, now)
	for _, typ := range []string{runlog.EventMoveBegin, runlog.EventMoveDone} {
		must(t, run.Log(runlog.Event{Time: now, Type: typ, Item: "0001", From: filepath.Join(e.Home, "photo.jpg"), To: precious, Bytes: 5}))
	}
	must(t, run.Close(now, true))

	if _, err := Empty(e, run.ID); !errors.Is(err, ErrRefused) {
		t.Fatalf("Empty = %v, want ErrRefused", err)
	}
	if readFile(t, precious) != "photo" {
		t.Fatal("a file outside quarantine was deleted")
	}
	if _, err := Expire(e, now.Add(30*24*time.Hour), time.Hour, 0); !errors.Is(err, ErrRefused) {
		t.Fatalf("Expire = %v, want ErrRefused", err)
	}
	if readFile(t, precious) != "photo" {
		t.Fatal("Expire deleted a file outside quarantine")
	}
	if _, err := Restore(e, run.ID); !errors.Is(err, ErrRefused) {
		t.Fatalf("Restore = %v, want ErrRefused", err)
	}
}

func TestEmptyReadOnlyTree(t *testing.T) {
	e := newHome(t)
	mod := filepath.Join(e.Home, "go", "pkg", "mod")
	writeFile(t, filepath.Join(mod, "golang.org", "x", "sys@v0.38.0", "unix", "a.go"), "package unix")
	writeFile(t, filepath.Join(mod, "golang.org", "x", "sys@v0.38.0", "go.mod"), "module x")
	must(t, os.Chmod(filepath.Join(mod, "golang.org", "x", "sys@v0.38.0", "unix", "a.go"), 0o444))
	for _, d := range []string{"golang.org/x/sys@v0.38.0/unix", "golang.org/x/sys@v0.38.0", "golang.org/x", "golang.org"} {
		must(t, os.Chmod(filepath.Join(mod, d), 0o555))
	}
	id := quarantineAll(t, e, []plan.Item{itemFor(t, "0001", mod, 1000)}, now)

	freed, err := Empty(e, id)
	must(t, err)
	if freed != 1000 || exists(t, filepath.Join(e.QuarantineDir(), id)) {
		t.Fatalf("freed = %d, quarantine left: %v", freed, exists(t, filepath.Join(e.QuarantineDir(), id)))
	}
	if !exists(t, filepath.Join(e.Home, "go", "pkg")) {
		t.Fatal("parent of the item was deleted")
	}
}

// APFS needs write permission on a folder to give it a new parent. A
// read-only folder item is not moved; the failure is journaled and undo
// leaves it alone.
func TestReadOnlyFolderItemFailsCleanly(t *testing.T) {
	e := newHome(t)
	dir := filepath.Join(e.Home, "go", "pkg", "mod", "example.com", "m@v1.0.0")
	writeFile(t, filepath.Join(dir, "go.mod"), "module m")
	must(t, os.Chmod(dir, 0o555))
	before := snapshot(t, filepath.Join(e.Home, "go"))
	it := itemFor(t, "0001", dir, 8)
	run := startRun(t, e, []plan.Item{it}, now)
	err := Quarantine(e, run, it)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v, want permission denied", err)
	}
	must(t, run.Close(now, true))
	if got := strings.Join(journalTypes(t, e, run.ID), ","); got != "start,move_begin,move_failed,end" {
		t.Fatalf("journal = %s", got)
	}
	restored, err := Restore(e, run.ID)
	must(t, err)
	if len(restored) != 0 {
		t.Fatalf("restored %+v, want nothing", restored)
	}
	requireSame(t, before, snapshot(t, filepath.Join(e.Home, "go")))
	if s := summary(t, e, run.ID); s.Items != 0 || s.Status != runlog.StatusDone {
		t.Fatalf("summary = %+v", s)
	}
}

func TestExpireByAge(t *testing.T) {
	e := newHome(t)
	writeFile(t, filepath.Join(e.Home, "Documents", "keep.txt"), "keep")
	var ids []string
	for i, age := range []time.Duration{10 * 24 * time.Hour, 8 * 24 * time.Hour, 24 * time.Hour} {
		p := filepath.Join(e.Home, "data", strconv.Itoa(i))
		writeFile(t, p, "x")
		ids = append(ids, quarantineAll(t, e, []plan.Item{itemFor(t, "0001", p, int64(100*(i+1)))}, now.Add(-age)))
	}
	before := snapshot(t, filepath.Join(e.Home, "Documents"))

	freed, err := Expire(e, now, DefaultMaxAge, DefaultMaxBytes)
	must(t, err)
	if freed != 300 {
		t.Fatalf("freed = %d, want 300", freed)
	}
	sizes, err := QuarantineSize(e)
	must(t, err)
	if len(sizes) != 1 || sizes[ids[2]] != 300 {
		t.Fatalf("QuarantineSize = %v, want only %s with 300", sizes, ids[2])
	}
	for _, id := range ids[:2] {
		if s := summary(t, e, id); s.Status != runlog.StatusEmptied {
			t.Fatalf("%s status = %q, want emptied", id, s.Status)
		}
	}
	requireSame(t, before, snapshot(t, filepath.Join(e.Home, "Documents")))
}

func TestExpireBySizeCap(t *testing.T) {
	e := newHome(t)
	var ids []string
	for i, size := range []int64{100, 200, 300} {
		p := filepath.Join(e.Home, "data", strconv.Itoa(i))
		writeFile(t, p, "x")
		ids = append(ids, quarantineAll(t, e, []plan.Item{itemFor(t, "0001", p, size)}, now.Add(time.Duration(i-3)*time.Hour)))
	}
	// An undone run holds nothing and is left alone.
	p := filepath.Join(e.Home, "data", "undone")
	writeFile(t, p, "x")
	undone := quarantineAll(t, e, []plan.Item{itemFor(t, "0001", p, 999)}, now.Add(-5*time.Hour))
	_, err := Restore(e, undone)
	must(t, err)

	freed, err := Expire(e, now, DefaultMaxAge, 450)
	must(t, err)
	if freed != 300 {
		t.Fatalf("freed = %d, want 300 (the two oldest runs)", freed)
	}
	sizes, err := QuarantineSize(e)
	must(t, err)
	if len(sizes) != 1 || sizes[ids[2]] != 300 {
		t.Fatalf("QuarantineSize = %v", sizes)
	}
	if s := summary(t, e, undone); s.Status != runlog.StatusUndone {
		t.Fatalf("undone run status = %q", s.Status)
	}
	if !exists(t, p) {
		t.Fatal("restored item was deleted")
	}
}

// Invariant 1, other volumes: the folder choice.
func TestRunDirPicksTheItemsVolume(t *testing.T) {
	e := newHome(t)
	id := "20261108-111500-a3f9"
	fakeMount(t, "/Volumes/Ext")
	must(t, os.MkdirAll(e.QuarantineDir(), 0o700))
	q, err := plan.IdentityOf(e.QuarantineDir())
	must(t, err)

	got, err := runDir(e, id, q.Dev, e.Home)
	must(t, err)
	if want := filepath.Join(e.QuarantineDir(), id); got != want {
		t.Fatalf("same volume: %s, want %s", got, want)
	}
	got, err = runDir(e, id, q.Dev+1, "/Volumes/Ext/cache")
	must(t, err)
	if want := "/Volumes/Ext/.sapu-quarantine/" + strconv.Itoa(os.Getuid()) + "/" + id; got != want {
		t.Fatalf("other volume: %s, want %s", got, want)
	}
}

func TestCheckRunDir(t *testing.T) {
	e := newHome(t)
	id := "20261108-111500-a3f9"
	uid := strconv.Itoa(os.Getuid())
	mnt := filepath.Join(t.TempDir(), "mnt")
	fakeMount(t, mnt)
	ok := []string{
		filepath.Join(e.QuarantineDir(), id),
		filepath.Join(mnt, ".sapu-quarantine", uid, id),
	}
	bad := []string{
		e.QuarantineDir(),
		filepath.Join(e.QuarantineDir(), "20261108-111500-ffff"),
		filepath.Join(e.QuarantineDir(), id, "0001-x"),
		filepath.Join(mnt, ".sapu-quarantine", uid),
		filepath.Join(mnt, ".sapu-quarantine", "0", id),
		filepath.Join(mnt, "other", uid, id),
		filepath.Join(mnt, "sub", ".sapu-quarantine", uid, id),
		filepath.Join(e.Home, ".sapu-quarantine", uid, id),
		e.QuarantineDir() + "/../quarantine/" + id,
		"quarantine/" + id,
	}
	for _, d := range ok {
		if err := checkRunDir(e, id, d); err != nil {
			t.Errorf("checkRunDir(%s) = %v, want ok", d, err)
		}
	}
	for _, d := range bad {
		if err := checkRunDir(e, id, d); !errors.Is(err, ErrRefused) {
			t.Errorf("checkRunDir(%s) = %v, want ErrRefused", d, err)
		}
	}
}

// Invariant 1, other volumes: restore and empty of a per-volume quarantine.
// A folder stands in for the second volume (the real one is in vm_test.go).
func TestVolumeQuarantineRestoreAndEmpty(t *testing.T) {
	e := newHome(t)
	mnt := filepath.Join(realTempDir(t), "mnt")
	writeFile(t, filepath.Join(mnt, "keep.txt"), "keep")
	fakeMount(t, mnt)
	data := filepath.Join(e.Home, "data")
	buildTree(t, data, 5, 50, 3)
	before := snapshot(t, data)
	beforeMount := snapshot(t, mnt)

	items := itemsFor(t, data)
	run := startRun(t, e, items, now)
	vdir := volumeRunDir(mnt, run.ID)
	for _, it := range items {
		must(t, moveItem(run, it, vdir))
	}
	must(t, run.Close(now, true))
	sizes, err := QuarantineSize(e)
	must(t, err)
	if sizes[run.ID] != int64(100*len(items)) {
		t.Fatalf("QuarantineSize = %v", sizes)
	}
	_, err = Restore(e, run.ID)
	must(t, err)
	requireSame(t, before, snapshot(t, data))
	if exists(t, vdir) {
		t.Fatal("empty volume quarantine folder left behind")
	}

	run = startRun(t, e, items, now.Add(time.Minute))
	vdir = volumeRunDir(mnt, run.ID)
	for _, it := range items {
		must(t, moveItem(run, it, vdir))
	}
	must(t, run.Close(now, true))
	freed, err := Empty(e, run.ID)
	must(t, err)
	if freed != int64(100*len(items)) || exists(t, vdir) {
		t.Fatalf("freed = %d, folder left: %v", freed, exists(t, vdir))
	}
	afterMount := snapshot(t, mnt)
	for p := range afterMount {
		if strings.HasPrefix(p, ".sapu-quarantine") {
			delete(afterMount, p)
		}
	}
	requireSame(t, beforeMount, afterMount)
}

// fakeMount makes mountOf report dir as the mount point of everything inside
// it, and isMountPoint report dir as mounted while it exists. The kernel
// reports mount points as real paths, so a stand-in that Empty deletes from
// must be one too (see realTempDir).
func fakeMount(t *testing.T, dir string) {
	t.Helper()
	realMountOf, realIsMountPoint := mountOf, isMountPoint
	mountOf = func(p string) (string, error) {
		if p == dir || within(p, dir) {
			return dir, nil
		}
		return realMountOf(p)
	}
	isMountPoint = func(p string) (bool, error) {
		if p == dir {
			_, err := os.Lstat(dir)
			return err == nil, nil
		}
		return realIsMountPoint(p)
	}
	t.Cleanup(func() { mountOf, isMountPoint = realMountOf, realIsMountPoint })
}

// realTempDir is t.TempDir() with symlinks resolved (/var is /private/var).
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	return dir
}

func TestStateFolderMissingIsRefused(t *testing.T) {
	e := newHome(t)
	writeFile(t, filepath.Join(e.Home, "a.txt"), "a")
	other := env.New(t.TempDir())
	run := startRun(t, other, nil, now)
	defer func() { must(t, run.Close(now, false)) }()
	if err := Quarantine(e, run, itemFor(t, "0001", filepath.Join(e.Home, "a.txt"), 1)); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	if exists(t, e.StateDir) {
		t.Fatal("refusal created the state folder")
	}
}

func TestIsApp(t *testing.T) {
	yes := []string{
		"/Applications/Foo.app",
		"/Applications/Utilities/Foo.app",
		"/Applications/foo.APP",
	}
	no := []string{
		"/Applications",
		"/Applications/",
		"/Applications/Foo",
		"/Applications/Foo.app/Contents",
		"/Applications/Foo.app/Contents/Bar.app",
		"/Applications/A/B/Foo.app",
		"/Applications/Outer.app/Inner.app",
		"/System/Applications/Foo.app",
		"/Applications/../Users/x.app",
		"/applications/Foo.app",
	}
	for _, p := range yes {
		if !isApp(p) {
			t.Errorf("isApp(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if isApp(p) {
			t.Errorf("isApp(%q) = true, want false", p)
		}
	}
}

func TestRemoveOwnLaunchAgent(t *testing.T) {
	e := env.New(t.TempDir())
	dir := filepath.Join(e.Home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ours := filepath.Join(dir, "dev.sapu.agent.plist")
	theirs := filepath.Join(dir, "com.other.agent.plist")
	for _, p := range []string{ours, theirs} {
		if err := os.WriteFile(p, []byte("<plist/>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveOwnLaunchAgent(e, "dev.sapu.agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(ours); !os.IsNotExist(err) {
		t.Errorf("Sapu's agent plist still exists: %v", err)
	}
	if err := RemoveOwnLaunchAgent(e, "dev.sapu.agent"); err != nil {
		t.Errorf("a missing file must not be an error: %v", err)
	}
	for _, label := range []string{"com.other.agent", "dev.sapu.", "dev.sapu./../x", ""} {
		if err := RemoveOwnLaunchAgent(e, label); !errors.Is(err, ErrRefused) {
			t.Errorf("label %q: err %v, want ErrRefused", label, err)
		}
	}
	if _, err := os.Lstat(theirs); err != nil {
		t.Errorf("another app's agent was touched: %v", err)
	}
	// A symlink in place of the plist is refused, and its target survives.
	target := filepath.Join(e.Home, "keep.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, ours); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwnLaunchAgent(e, "dev.sapu.agent"); !errors.Is(err, ErrRefused) {
		t.Errorf("symlinked plist: err %v, want ErrRefused", err)
	}
	if _, err := os.Lstat(target); err != nil {
		t.Errorf("symlink target was touched: %v", err)
	}
}

// A flock that is released a moment later (as when a forked child that
// briefly held a copy of the lock file execs) is taken; one that stays busy
// is reported after the wait. Two opens of one file conflict even inside one
// process, which stands in for the other holder here.
func TestFlockWithinWaitsForABriefHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	holder, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if err := flockWithin(f, 100*time.Millisecond); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("busy lock: err %v, want EWOULDBLOCK", err)
	}
	hfd := int(holder.Fd())
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(200 * time.Millisecond)
		_ = unix.Flock(hfd, unix.LOCK_UN)
	}()
	err = flockWithin(f, 2*time.Second)
	<-done
	if err != nil {
		t.Fatalf("lock released after 200ms was not taken: %v", err)
	}
}
