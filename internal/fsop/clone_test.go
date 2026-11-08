package fsop

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

// Tests of QuarantineAndClone and of undoing it (invariants 9 and 10). The
// test homes are temp folders, which are on APFS on a Mac.

var cloneData = bytes.Repeat([]byte("the same report, twice "), 15000)

// cloneHome has a file and an extra copy of it with another mode, mtime and
// xattr, plus a file that has nothing to do with it.
func cloneHome(t *testing.T) (e env.Env, keep, extra string) {
	t.Helper()
	e = newHome(t)
	keep = filepath.Join(e.Home, "Downloads", "report.pdf")
	extra = filepath.Join(e.Home, "Documents", "work", "report copy.pdf")
	writeFile(t, keep, string(cloneData))
	writeFile(t, extra, string(cloneData))
	writeFile(t, filepath.Join(e.Home, "Documents", "notes.txt"), "notes")
	must(t, os.Chmod(extra, 0o600))
	must(t, unix.Setxattr(extra, "com.sapu.test", []byte("extra's own"), 0))
	must(t, os.Chtimes(keep, time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC), time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)))
	mt := time.Date(2025, 6, 15, 10, 30, 0, 123456789, time.UTC)
	must(t, os.Chtimes(extra, mt, mt))
	return e, keep, extra
}

// cloneItem is what sapu dupes --replace-with-clone plans for extra.
func cloneItem(t *testing.T, id, extra, keep string) plan.Item {
	t.Helper()
	it := itemFor(t, id, extra, int64(len(cloneData)))
	kid, err := plan.IdentityOf(keep)
	must(t, err)
	it.KeepCopy = &plan.KeepCopy{Path: keep, Identity: kid}
	it.CloneFrom = keep
	return it
}

// homeSnap is the whole test home but Sapu's state folder. The mtimes of the
// folders given (relative to the home) are left out: a rename changes them.
func homeSnap(t *testing.T, e env.Env, parents ...string) map[string]entry {
	t.Helper()
	s := snapshotWithout(t, e.Home, ".sapu")
	for _, p := range parents {
		en, ok := s[p]
		if !ok {
			t.Fatalf("homeSnap: %s is not in the home", p)
		}
		en.MtimeNs = 0
		s[p] = en
	}
	return s
}

func quarantined(e env.Env, runID string, it plan.Item) string {
	return filepath.Join(e.QuarantineDir(), runID, it.ID+"-"+filepath.Base(it.Path))
}

func identity(t *testing.T, p string) plan.Identity {
	t.Helper()
	id, err := plan.IdentityOf(p)
	must(t, err)
	return id
}

func events(t *testing.T, e env.Env, runID, typ string) []runlog.Event {
	t.Helper()
	evs, err := runlog.Events(e, runID)
	must(t, err)
	var out []runlog.Event
	for _, ev := range evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

// applyClone runs QuarantineAndClone on one item and closes the run.
func applyClone(t *testing.T, e env.Env, it plan.Item) string {
	t.Helper()
	run := startRun(t, e, []plan.Item{it}, now)
	must(t, QuarantineAndClone(e, run, it))
	must(t, run.Close(now, true))
	return run.ID
}

func TestQuarantineAndCloneThenUndo(t *testing.T) {
	e, keep, extra := cloneHome(t)
	before := homeSnap(t, e, "Documents/work")
	orig := identity(t, extra)
	it := cloneItem(t, "0001", extra, keep)

	id := applyClone(t, e, it)

	// The clone is in place, with the content of the kept copy and the mode
	// and mtime of the copy that was there.
	clone := identity(t, extra)
	if clone.Ino == orig.Ino || clone.Dev != orig.Dev {
		t.Fatalf("clone identity %+v; want a new file on the same volume as %+v", clone, orig)
	}
	if clone.Mode != orig.Mode || clone.MtimeNs != orig.MtimeNs || clone.Size != orig.Size {
		t.Fatalf("clone %+v does not look like the original %+v", clone, orig)
	}
	if !bytes.Equal([]byte(readFile(t, extra)), cloneData) {
		t.Fatal("the clone's content differs")
	}
	// The original is in quarantine, unchanged, xattr and all.
	q := quarantined(e, id, it)
	if identity(t, q) != orig || xattrs(t, q)["com.sapu.test"] != "extra's own" {
		t.Fatalf("quarantined original %+v, want %+v with its xattr", identity(t, q), orig)
	}
	if identity(t, keep) != it.KeepCopy.Identity {
		t.Fatal("the kept copy changed")
	}
	if exists(t, cloneName(filepath.Dir(q), it.ID)) {
		t.Fatal("the clone's temporary name is left in quarantine")
	}
	if got := strings.Join(journalTypes(t, e, id), ","); got != "start,move_begin,move_done,clone_begin,clone_done,end" {
		t.Fatalf("journal = %s", got)
	}
	done := events(t, e, id, runlog.EventCloneDone)
	if len(done) != 1 || done[0].Identity == nil || *done[0].Identity != clone || done[0].From != keep || done[0].To != extra {
		t.Fatalf("clone_done = %+v, want the clone's identity %+v", done, clone)
	}
	if s := summary(t, e, id); s.Items != 1 || s.Status != runlog.StatusDone {
		t.Fatalf("summary = %+v", s)
	}

	restored, err := Restore(e, id)
	must(t, err)
	if len(restored) != 1 || restored[0].Conflict || restored[0].To != extra {
		t.Fatalf("restored = %+v, want the original back at its place", restored)
	}
	requireSame(t, before, homeSnap(t, e, "Documents/work"))
	if exists(t, filepath.Join(e.QuarantineDir(), id)) {
		t.Fatal("the run's quarantine folder is left behind")
	}
	if got := strings.Join(journalTypes(t, e, id), ","); !strings.HasSuffix(got, "end,unclone_begin,unclone_done,restore_begin,restore_done") {
		t.Fatalf("journal = %s", got)
	}
	if s := summary(t, e, id); s.Status != runlog.StatusUndone {
		t.Fatalf("status after undo = %q", s.Status)
	}
}

// A clone that was changed or replaced after apply is the user's file now.
// Undo never deletes it; the original comes back beside it.
func TestUndoKeepsChangedClone(t *testing.T) {
	cases := map[string]func(t *testing.T, p string){
		"edited": func(t *testing.T, p string) {
			must(t, os.WriteFile(p, []byte("edited after apply"), 0o600))
		},
		"replaced by a file with the same bytes and times": func(t *testing.T, p string) {
			id := identity(t, p)
			must(t, os.Remove(p))
			must(t, os.WriteFile(p, cloneData, 0o600))
			mt := time.Unix(0, id.MtimeNs)
			must(t, os.Chtimes(p, mt, mt))
		},
		"mode changed": func(t *testing.T, p string) {
			must(t, os.Chmod(p, 0o644))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			e, keep, extra := cloneHome(t)
			orig := identity(t, extra)
			it := cloneItem(t, "0001", extra, keep)
			id := applyClone(t, e, it)

			change(t, extra)
			changed := identity(t, extra)
			content := readFile(t, extra)

			restored, err := Restore(e, id)
			must(t, err)
			beside := extra + " (sapu " + id + ")"
			if len(restored) != 1 || !restored[0].Conflict || restored[0].To != beside {
				t.Fatalf("restored = %+v, want the original beside the clone", restored)
			}
			if identity(t, extra) != changed || readFile(t, extra) != content {
				t.Fatal("the changed clone was touched")
			}
			if identity(t, beside) != orig || !bytes.Equal([]byte(readFile(t, beside)), cloneData) {
				t.Fatal("the original did not come back beside the clone")
			}
			if !bytes.Equal([]byte(readFile(t, keep)), cloneData) || identity(t, keep) != it.KeepCopy.Identity {
				t.Fatal("changing the clone changed the kept copy")
			}
			if n := len(events(t, e, id, runlog.EventUncloneBegin)); n != 0 {
				t.Fatalf("undo tried to remove the changed clone (%d unclone_begin)", n)
			}
		})
	}
}

// The kept copy changed after the plan was made: nothing is moved.
func TestCloneKeeperChangedBeforeMoveMovesNothing(t *testing.T) {
	for name, change := range map[string]func(p string) error{
		"edited":  func(p string) error { return os.WriteFile(p, []byte("edited"), 0o644) },
		"deleted": os.Remove,
	} {
		t.Run(name, func(t *testing.T) {
			e, keep, extra := cloneHome(t)
			it := cloneItem(t, "0001", extra, keep)
			must(t, change(keep))
			before := homeSnap(t, e)
			run := startRun(t, e, []plan.Item{it}, now)
			err := QuarantineAndClone(e, run, it)
			must(t, run.Close(now, true))
			if !errors.Is(err, ErrChanged) {
				t.Fatalf("err = %v, want ErrChanged", err)
			}
			requireSame(t, before, homeSnap(t, e))
			if got := strings.Join(journalTypes(t, e, run.ID), ","); got != "start,end" {
				t.Fatalf("journal = %s", got)
			}
		})
	}
}

// The kept copy changes in the moment after the move: no clone is made, and
// the item stays in quarantine, where undo finds it.
func TestCloneKeeperChangedAfterMoveLeavesItemInQuarantine(t *testing.T) {
	e, keep, extra := cloneHome(t)
	it := cloneItem(t, "0001", extra, keep)
	before := homeSnap(t, e, "Documents/work")
	run := startRun(t, e, []plan.Item{it}, now)
	onStep(t, map[int]func() error{3: func() error { return os.WriteFile(keep, []byte("edited"), 0o644) }})

	err := QuarantineAndClone(e, run, it)
	must(t, run.Close(now, true))
	afterStep = func() error { return nil }
	if !errors.Is(err, ErrNotCloned) || errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrNotCloned only", err)
	}
	if exists(t, extra) || !exists(t, quarantined(e, run.ID, it)) {
		t.Fatal("want the item in quarantine and nothing at its path")
	}
	if got := strings.Join(journalTypes(t, e, run.ID), ","); got != "start,move_begin,move_done,clone_failed,end" {
		t.Fatalf("journal = %s", got)
	}
	_, err = Restore(e, run.ID)
	must(t, err)
	want := before
	want["Downloads/report.pdf"] = homeSnap(t, e)["Downloads/report.pdf"] // the user's edit
	requireSame(t, want, homeSnap(t, e, "Documents/work"))
}

// A file appears at the path after the move: the clone never replaces it.
func TestFileAppearingAtPathIsNotReplacedByClone(t *testing.T) {
	e, keep, extra := cloneHome(t)
	it := cloneItem(t, "0001", extra, keep)
	run := startRun(t, e, []plan.Item{it}, now)
	onStep(t, map[int]func() error{4: func() error { return os.WriteFile(extra, []byte("new file"), 0o644) }}) // after clone_begin

	err := QuarantineAndClone(e, run, it)
	must(t, run.Close(now, true))
	afterStep = func() error { return nil }
	if !errors.Is(err, ErrNotCloned) {
		t.Fatalf("err = %v, want ErrNotCloned", err)
	}
	if readFile(t, extra) != "new file" {
		t.Fatal("the new file was replaced")
	}
	if exists(t, cloneName(filepath.Join(e.QuarantineDir(), run.ID), it.ID)) {
		t.Fatal("the clone that was not placed is left in quarantine")
	}
	if got := strings.Join(journalTypes(t, e, run.ID), ","); got != "start,move_begin,move_done,clone_begin,clone_failed,end" {
		t.Fatalf("journal = %s", got)
	}

	restored, err := Restore(e, run.ID)
	must(t, err)
	beside := extra + " (sapu " + run.ID + ")"
	if len(restored) != 1 || !restored[0].Conflict || !bytes.Equal([]byte(readFile(t, beside)), cloneData) {
		t.Fatalf("restored = %+v, want the original beside the new file", restored)
	}
	if readFile(t, extra) != "new file" {
		t.Fatal("undo touched the new file")
	}
}

// A volume that cannot clone (not APFS): the item stays a quarantined move.
func TestCloneFailureLeavesAQuarantinedMove(t *testing.T) {
	e, keep, extra := cloneHome(t)
	it := cloneItem(t, "0001", extra, keep)
	before := homeSnap(t, e, "Documents/work")
	real := cloneFile
	cloneFile = func(src, dst string) error {
		return &os.LinkError{Op: "clonefile", Old: src, New: dst, Err: unix.ENOTSUP}
	}
	t.Cleanup(func() { cloneFile = real })

	run := startRun(t, e, []plan.Item{it}, now)
	err := QuarantineAndClone(e, run, it)
	must(t, run.Close(now, true))
	if !errors.Is(err, ErrNotCloned) || !strings.Contains(err.Error(), "moved to quarantine, clone not placed") {
		t.Fatalf("err = %v, want ErrNotCloned", err)
	}
	if exists(t, extra) || identity(t, quarantined(e, run.ID, it)) != it.Identity {
		t.Fatal("want the item in quarantine and nothing at its path")
	}
	failed := events(t, e, run.ID, runlog.EventCloneFailed)
	if len(failed) != 1 || !strings.Contains(failed[0].Reason, "not supported") {
		t.Fatalf("clone_failed = %+v", failed)
	}
	if s := summary(t, e, run.ID); s.Items != 1 {
		t.Fatalf("summary = %+v, want the move counted", s)
	}
	_, err = Restore(e, run.ID)
	must(t, err)
	requireSame(t, before, homeSnap(t, e, "Documents/work"))
}

// Invariant 4 for clones: a crash after any journal write or rename, while
// cloning or while undoing, can be undone, and undo deletes only the clone.
func TestCrashAtEveryCloneStepIsUndone(t *testing.T) {
	// move_begin, rename, move_done, clone_begin, clone rename, clone_done
	for k := 1; k <= 6; k++ {
		t.Run("apply/step"+strconv.Itoa(k), func(t *testing.T) {
			e, keep, extra := cloneHome(t)
			it := cloneItem(t, "0001", extra, keep)
			before := homeSnap(t, e, "Documents/work")
			run := startRun(t, e, []plan.Item{it}, now)
			setHook(t, k)
			if err := QuarantineAndClone(e, run, it); !errors.Is(err, errCrash) {
				t.Fatalf("err = %v, want the simulated crash", err)
			}
			must(t, run.Close(now, false))
			afterStep = func() error { return nil }

			_, err := Restore(e, run.ID)
			must(t, err)
			requireSame(t, before, homeSnap(t, e, "Documents/work"))
			if exists(t, filepath.Join(e.QuarantineDir(), run.ID)) {
				t.Fatal("something is left in the run's quarantine folder")
			}
		})
	}
	// unclone_begin, rename, unclone_done, restore_begin, rename, restore_done
	for k := 1; k <= 6; k++ {
		t.Run("undo/step"+strconv.Itoa(k), func(t *testing.T) {
			e, keep, extra := cloneHome(t)
			before := homeSnap(t, e, "Documents/work")
			id := applyClone(t, e, cloneItem(t, "0001", extra, keep))
			setHook(t, k)
			if _, err := Restore(e, id); !errors.Is(err, errCrash) {
				t.Fatalf("first undo: err = %v, want the simulated crash", err)
			}
			afterStep = func() error { return nil }
			_, err := Restore(e, id)
			must(t, err)
			requireSame(t, before, homeSnap(t, e, "Documents/work"))
			if exists(t, filepath.Join(e.QuarantineDir(), id)) {
				t.Fatal("something is left in the run's quarantine folder")
			}
		})
	}
}

// A crash after clone_begin, then a file the user puts at the path, with the
// same content as the kept copy. Sapu cannot prove it made that file, so it
// stays; the clone still in quarantine is deleted, and the original comes
// back beside the user's file.
func TestCrashThenUserFileAtPathIsKept(t *testing.T) {
	e, keep, extra := cloneHome(t)
	it := cloneItem(t, "0001", extra, keep)
	run := startRun(t, e, []plan.Item{it}, now)
	setHook(t, 4) // after clone_begin, before the clone is renamed into place
	if err := QuarantineAndClone(e, run, it); !errors.Is(err, errCrash) {
		t.Fatalf("err = %v, want the simulated crash", err)
	}
	must(t, run.Close(now, false))
	afterStep = func() error { return nil }
	tmp := cloneName(filepath.Join(e.QuarantineDir(), run.ID), it.ID)
	if !exists(t, tmp) {
		t.Fatal("test setup: the clone should wait in quarantine")
	}
	writeFile(t, extra, string(cloneData))
	mine := identity(t, extra)

	restored, err := Restore(e, run.ID)
	must(t, err)
	if len(restored) != 1 || !restored[0].Conflict {
		t.Fatalf("restored = %+v, want a conflict restore", restored)
	}
	if identity(t, extra) != mine {
		t.Fatal("the user's file at the path was touched")
	}
	if identity(t, extra+" (sapu "+run.ID+")") != it.Identity {
		t.Fatal("the original is not beside the user's file")
	}
	if exists(t, tmp) {
		t.Fatal("the clone in quarantine was not deleted")
	}
}

// Something takes the clone's place in the moment between undo's check and
// its move. That file goes back, and the original comes back beside it.
func TestUndoPutsBackFileThatReplacedTheClone(t *testing.T) {
	e, keep, extra := cloneHome(t)
	id := applyClone(t, e, cloneItem(t, "0001", extra, keep))
	onStep(t, map[int]func() error{1: func() error { return replaceFile(extra, "new personal data") }}) // after unclone_begin

	restored, err := Restore(e, id)
	must(t, err)
	afterStep = func() error { return nil }
	if readFile(t, extra) != "new personal data" {
		t.Fatal("the file that replaced the clone is not back")
	}
	if len(restored) != 1 || !restored[0].Conflict || !bytes.Equal([]byte(readFile(t, extra+" (sapu "+id+")")), cloneData) {
		t.Fatalf("restored = %+v, want the original beside", restored)
	}
	if !bytes.Equal([]byte(readFile(t, extra+"-old")), cloneData) {
		t.Fatal("the clone the test moved aside changed")
	}
	if n := len(events(t, e, id, runlog.EventUncloneFailed)); n != 1 {
		t.Fatalf("%d unclone_failed events, want 1", n)
	}
}

// As above, and the path is taken again before that file can go back. It
// waits in quarantine with the original, and the next undo puts both back.
func TestUndoStrandedReplacementComesBackLater(t *testing.T) {
	e, keep, extra := cloneHome(t)
	it := cloneItem(t, "0001", extra, keep)
	id := applyClone(t, e, it)
	onStep(t, map[int]func() error{
		1: func() error { return replaceFile(extra, "new personal data") },
		2: func() error { return os.WriteFile(extra, []byte("newest"), 0o644) }, // after the move aside
	})

	_, err := Restore(e, id)
	afterStep = func() error { return nil }
	if !errors.Is(err, ErrStranded) {
		t.Fatalf("undo = %v, want ErrStranded", err)
	}
	hold := cloneName(filepath.Join(e.QuarantineDir(), id), it.ID)
	if readFile(t, hold) != "new personal data" || identity(t, quarantined(e, id, it)) != it.Identity {
		t.Fatal("want the replacement and the original both in quarantine")
	}
	if readFile(t, extra) != "newest" {
		t.Fatal("the newest file was touched")
	}

	must(t, os.Remove(extra)) // the user moves the newest file away
	restored, err := Restore(e, id)
	must(t, err)
	if readFile(t, extra) != "new personal data" {
		t.Fatal("the replacement did not come back")
	}
	if len(restored) != 1 || !restored[0].Conflict || identity(t, extra+" (sapu "+id+")") != it.Identity {
		t.Fatalf("restored = %+v, want the original beside", restored)
	}
	if exists(t, filepath.Join(e.QuarantineDir(), id)) {
		t.Fatal("something is left in quarantine")
	}
}

// Emptying deletes only the quarantined originals; the clones stay.
func TestEmptyKeepsClones(t *testing.T) {
	e, keep, extra := cloneHome(t)
	id := applyClone(t, e, cloneItem(t, "0001", extra, keep))
	clone := identity(t, extra)

	freed, err := Empty(e, id)
	must(t, err)
	if freed != int64(len(cloneData)) || exists(t, filepath.Join(e.QuarantineDir(), id)) {
		t.Fatalf("freed = %d, quarantine left: %v", freed, exists(t, filepath.Join(e.QuarantineDir(), id)))
	}
	if identity(t, extra) != clone || !bytes.Equal([]byte(readFile(t, extra)), cloneData) {
		t.Fatal("the clone changed")
	}
	if !bytes.Equal([]byte(readFile(t, keep)), cloneData) {
		t.Fatal("the kept copy changed")
	}
}

// Items that are not a well-formed clone request are refused before
// anything moves.
func TestQuarantineAndCloneRefusesBadItems(t *testing.T) {
	e, keep, extra := cloneHome(t)
	other := filepath.Join(e.Home, "Documents", "notes.txt")
	short := filepath.Join(e.Home, "Documents", "short.pdf")
	writeFile(t, short, "shorter")
	folder := filepath.Join(e.Home, "Documents", "work")
	cases := map[string]func(it *plan.Item){
		"no kept copy":           func(it *plan.Item) { it.KeepCopy = nil },
		"source is not the kept": func(it *plan.Item) { it.CloneFrom = other },
		"no source":              func(it *plan.Item) { it.CloneFrom = "" },
		"relative kept copy":     func(it *plan.Item) { it.KeepCopy.Path, it.CloneFrom = "Downloads/report.pdf", "Downloads/report.pdf" },
		"kept copy is the item":  func(it *plan.Item) { it.KeepCopy.Path, it.CloneFrom = extra, extra },
		"id with a dot":          func(it *plan.Item) { it.ID = "00.1" },
		"folder item": func(it *plan.Item) {
			*it = cloneItem(t, "0001", folder, keep)
		},
		"kept copy is a folder": func(it *plan.Item) {
			it.KeepCopy = &plan.KeepCopy{Path: folder, Identity: identity(t, folder)}
			it.CloneFrom = folder
		},
		"kept copy is shorter": func(it *plan.Item) {
			it.KeepCopy = &plan.KeepCopy{Path: short, Identity: identity(t, short)}
			it.CloneFrom = short
		},
	}
	before := homeSnap(t, e)
	run := startRun(t, e, nil, now)
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			it := cloneItem(t, "0001", extra, keep)
			edit(&it)
			if err := QuarantineAndClone(e, run, it); !errors.Is(err, ErrRefused) {
				t.Fatalf("err = %v, want ErrRefused", err)
			}
		})
	}
	must(t, run.Close(now, true))
	requireSame(t, before, homeSnap(t, e))
	if got := strings.Join(journalTypes(t, e, run.ID), ","); got != "start,end" {
		t.Fatalf("journal = %s", got)
	}
}
