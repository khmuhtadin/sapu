package fsop

// Regression tests for problems found by an independent review (Nov 2026).
// Each one failed before its fix.

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
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

// Empty deleted whatever a symlinked quarantine folder led to. Each case
// quarantines one item, then puts a symlink somewhere on the way to the run
// folder. The link leads to a folder whose <run_id>/0001-cache is the only
// copy of someone's data. Empty and Expire must refuse and delete nothing.
func TestEmptyRefusesSymlinkedQuarantineFolders(t *testing.T) {
	uid := strconv.Itoa(os.Getuid())
	cases := []struct {
		name   string
		volume bool                                      // the item goes to the volume's quarantine
		swap   func(e env.Env, mnt, runID string) string // the folder replaced by a symlink
	}{
		{"quarantine folder", false, func(e env.Env, _, _ string) string { return e.QuarantineDir() }},
		{"run folder", false, func(e env.Env, _, runID string) string { return filepath.Join(e.QuarantineDir(), runID) }},
		{"volume quarantine folder", true, func(_ env.Env, mnt, _ string) string { return filepath.Join(mnt, ".sapu-quarantine") }},
		{"volume user folder", true, func(_ env.Env, mnt, _ string) string { return filepath.Join(mnt, ".sapu-quarantine", uid) }},
		{"volume run folder", true, func(_ env.Env, mnt, runID string) string {
			return filepath.Join(mnt, ".sapu-quarantine", uid, runID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHome(t)
			mnt := filepath.Join(realTempDir(t), "mnt")
			must(t, os.MkdirAll(mnt, 0o755))
			fakeMount(t, mnt)
			p := filepath.Join(e.Home, "cache")
			writeFile(t, p, "cache")
			it := itemFor(t, "0001", p, 5)
			run := startRun(t, e, []plan.Item{it}, now)
			runDir := filepath.Join(e.QuarantineDir(), run.ID)
			if tc.volume {
				runDir = volumeRunDir(mnt, run.ID)
				must(t, moveItem(run, it, runDir))
			} else {
				must(t, Quarantine(e, run, it))
			}
			must(t, run.Close(now, true))

			// The link target mirrors the real layout below the swapped folder.
			swapped := tc.swap(e, mnt, run.ID)
			rest, err := filepath.Rel(swapped, filepath.Join(runDir, "0001-cache"))
			must(t, err)
			outside := t.TempDir()
			personal := filepath.Join(outside, rest)
			writeFile(t, personal, "only copy")
			must(t, os.Rename(swapped, swapped+"-saved"))
			must(t, os.Symlink(outside, swapped))

			if _, err := Empty(e, run.ID); !errors.Is(err, ErrRefused) {
				t.Fatalf("Empty = %v, want ErrRefused", err)
			}
			if _, err := Expire(e, now.Add(30*24*time.Hour), time.Hour, 1); !errors.Is(err, ErrRefused) {
				t.Fatalf("Expire = %v, want ErrRefused", err)
			}
			if readFile(t, personal) != "only copy" {
				t.Fatal("a file behind the symlink was deleted")
			}
			if s := summary(t, e, run.ID); s.Status == runlog.StatusEmptied {
				t.Fatal("the run was recorded as emptied")
			}
		})
	}
}

// A state folder reached through a symlink (like /var -> /private/var) is
// still Sapu's own folder, and Empty works there.
func TestEmptyWorksThroughSymlinkedStateFolder(t *testing.T) {
	e := newHome(t)
	real := filepath.Join(t.TempDir(), "state")
	must(t, os.MkdirAll(real, 0o700))
	e.StateDir = filepath.Join(e.Home, "state-link")
	must(t, os.Symlink(real, e.StateDir))
	p := filepath.Join(e.Home, "cache")
	writeFile(t, p, "cache")
	id := quarantineAll(t, e, []plan.Item{itemFor(t, "0001", p, 5)}, now)

	freed, err := Empty(e, id)
	must(t, err)
	if freed != 5 || exists(t, filepath.Join(real, "quarantine", id)) {
		t.Fatalf("freed = %d, quarantine left: %v", freed, exists(t, filepath.Join(real, "quarantine", id)))
	}
}

// onStep runs steps[k] at the k-th afterStep call of this test. For one
// move, step 1 follows the move_begin record, step 2 the rename and step 3
// the move_done record.
func onStep(t *testing.T, steps map[int]func() error) {
	t.Helper()
	n := 0
	afterStep = func() error {
		n++
		if fn := steps[n]; fn != nil {
			return fn()
		}
		return nil
	}
	t.Cleanup(func() { afterStep = func() error { return nil } })
}

// replaceFile moves the file at p aside and writes a new one in its place.
func replaceFile(p, content string) error {
	if err := os.Rename(p, p+"-old"); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// Something replaced the item between Quarantine's last identity check and
// the rename, and Sapu moved the replacement, which was never planned. Now
// the replacement goes back and the item is reported as changed.
func TestQuarantinePutsBackItemReplacedWhileMoving(t *testing.T) {
	e := newHome(t)
	p := filepath.Join(e.Home, "cache")
	writeFile(t, p, "planned")
	it := itemFor(t, "0001", p, 7)
	run := startRun(t, e, []plan.Item{it}, now)
	onStep(t, map[int]func() error{1: func() error { return replaceFile(p, "unplanned personal data") }})

	err := Quarantine(e, run, it)
	must(t, run.Close(now, true))

	if !errors.Is(err, ErrChanged) {
		t.Fatalf("Quarantine = %v, want ErrChanged", err)
	}
	if readFile(t, p) != "unplanned personal data" || readFile(t, p+"-old") != "planned" {
		t.Fatal("the replacement is not back where it was")
	}
	if exists(t, filepath.Join(e.QuarantineDir(), run.ID, "0001-cache")) {
		t.Fatal("something was left in quarantine")
	}
	if got := strings.Join(journalTypes(t, e, run.ID), ","); got != "start,move_begin,move_failed,end" {
		t.Fatalf("journal = %s", got)
	}
	restored, err := Restore(e, run.ID)
	must(t, err)
	if len(restored) != 0 || readFile(t, p) != "unplanned personal data" {
		t.Fatalf("undo restored %+v; want nothing", restored)
	}
}

// When the replacement cannot go back because its old path is taken again,
// it stays in quarantine with its move_begin record, Quarantine returns
// ErrStranded, and undo restores it beside whatever is there now.
func TestQuarantineStrandsReplacementWhenPathIsTakenAgain(t *testing.T) {
	e := newHome(t)
	p := filepath.Join(e.Home, "cache")
	writeFile(t, p, "planned")
	it := itemFor(t, "0001", p, 7)
	run := startRun(t, e, []plan.Item{it}, now)
	onStep(t, map[int]func() error{
		1: func() error { return replaceFile(p, "unplanned personal data") },
		2: func() error { return os.WriteFile(p, []byte("newest"), 0o644) },
	})

	err := Quarantine(e, run, it)
	must(t, run.Close(now, true))

	if !errors.Is(err, ErrStranded) || errors.Is(err, ErrChanged) {
		t.Fatalf("Quarantine = %v, want ErrStranded only", err)
	}
	if got := strings.Join(journalTypes(t, e, run.ID), ","); got != "start,move_begin,end" {
		t.Fatalf("journal = %s", got)
	}
	restored, err := Restore(e, run.ID)
	must(t, err)
	if len(restored) != 1 || !restored[0].Conflict {
		t.Fatalf("restored = %+v, want one conflict restore", restored)
	}
	if readFile(t, p) != "newest" || readFile(t, p+" (sapu "+run.ID+")") != "unplanned personal data" || readFile(t, p+"-old") != "planned" {
		t.Fatal("undo did not bring the replacement back beside the newest file")
	}
}

// lockHomeVar passes the test home to TestHelperApplyingProcess.
const lockHomeVar = "SAPU_FSOP_LOCK_TEST_HOME"

// TestHelperApplyingProcess is not a test on its own: the lock test runs it in
// a child process as a second sapu in the middle of applying a run. It moves
// ~/a, prints the run id, waits until its stdin is closed, then moves ~/b.
func TestHelperApplyingProcess(t *testing.T) {
	home := os.Getenv(lockHomeVar)
	if home == "" {
		t.Skip("helper process for TestEmptyWaitsForOtherProcessApplyingTheRun")
	}
	e := env.New(home)
	unlock, err := Lock(e)
	must(t, err)
	defer unlock()
	items := []plan.Item{itemFor(t, "0001", filepath.Join(home, "a"), 1), itemFor(t, "0002", filepath.Join(home, "b"), 1)}
	run := startRun(t, e, items, time.Now())
	must(t, Quarantine(e, run, items[0]))
	if _, err := os.Stdout.WriteString("run " + run.ID + "\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	must(t, Quarantine(e, run, items[1]))
	must(t, run.Close(time.Now(), true))
}

// Another sapu process emptied a run while this one was still applying it,
// and the items moved after that could no longer be undone. While a process
// holds the state folder's lock, every other process is kept out.
func TestEmptyWaitsForOtherProcessApplyingTheRun(t *testing.T) {
	e := newHome(t)
	a, b, c := filepath.Join(e.Home, "a"), filepath.Join(e.Home, "b"), filepath.Join(e.Home, "c")
	writeFile(t, a, "cache a")
	writeFile(t, b, "only copy of b")
	writeFile(t, c, "only copy of c")

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperApplyingProcess$")
	cmd.Env = append(os.Environ(), lockHomeVar+"="+e.Home)
	stdin, err := cmd.StdinPipe()
	must(t, err)
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	must(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	out := bufio.NewReader(stdout)
	var id string
	for id == "" {
		line, err := out.ReadString('\n')
		if err != nil {
			t.Fatalf("child stopped before it printed its run id: %v\n%s%s", err, line, stderr.String())
		}
		id, _ = strings.CutPrefix(strings.TrimSpace(line), "run ")
	}

	// The child holds the lock with its run open.
	if _, err := Empty(e, id); !errors.Is(err, ErrLocked) {
		t.Fatalf("Empty = %v, want ErrLocked", err)
	}
	if _, err := Restore(e, id); !errors.Is(err, ErrLocked) {
		t.Fatalf("Restore = %v, want ErrLocked", err)
	}
	if _, err := Expire(e, now.Add(30*24*time.Hour), time.Hour, 1); !errors.Is(err, ErrLocked) {
		t.Fatalf("Expire = %v, want ErrLocked", err)
	}
	other := startRun(t, e, nil, now)
	if err := Quarantine(e, other, itemFor(t, "0001", c, 1)); !errors.Is(err, ErrLocked) {
		t.Fatalf("Quarantine = %v, want ErrLocked", err)
	}
	must(t, other.Close(now, true))
	if _, err := Lock(e); !errors.Is(err, ErrLocked) {
		t.Fatalf("Lock = %v, want ErrLocked", err)
	}

	must(t, stdin.Close())
	rest, err := io.ReadAll(out)
	must(t, err)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v\n%s%s", err, rest, stderr.String())
	}
	if _, err := Restore(e, id); err != nil {
		t.Fatalf("undo after the child finished: %v", err)
	}
	if readFile(t, a) != "cache a" || readFile(t, b) != "only copy of b" || readFile(t, c) != "only copy of c" {
		t.Fatal("undo did not bring every item back")
	}
}

// Lock is reentrant within one process: code that holds it can still call
// Empty, and the lock stays held until every unlock has been called once.
func TestLockIsReentrant(t *testing.T) {
	e := newHome(t)
	p := filepath.Join(e.Home, "cache")
	writeFile(t, p, "cache")
	id := quarantineAll(t, e, []plan.Item{itemFor(t, "0001", p, 5)}, now)
	if lockedElsewhere(t, e) {
		t.Fatal("lock still held after Quarantine returned")
	}

	unlock1, err := Lock(e)
	must(t, err)
	unlock2, err := Lock(e)
	must(t, err)
	if _, err := Empty(e, id); err != nil {
		t.Fatalf("Empty while this process holds the lock: %v", err)
	}
	unlock2()
	unlock2() // a second call does nothing
	if !lockedElsewhere(t, e) {
		t.Fatal("lock released while still held once")
	}
	unlock1()
	if lockedElsewhere(t, e) {
		t.Fatal("lock still held after every unlock")
	}
}

// lockedElsewhere tries the lock through a second open file, as another
// process would.
func lockedElsewhere(t *testing.T, e env.Env) bool {
	t.Helper()
	f, err := os.Open(filepath.Join(e.StateDir, lockFile))
	must(t, err)
	defer func() { _ = f.Close() }()
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return true
	}
	must(t, err)
	must(t, unix.Flock(int(f.Fd()), unix.LOCK_UN))
	return false
}

// A run with items on a volume that is unplugged was recorded as emptied,
// and undo then refused the items when the volume came back. Now Empty
// deletes nothing and logs nothing, and Expire skips the run quietly.
func TestEmptyWaitsForUnpluggedVolume(t *testing.T) {
	e := newHome(t)
	mnt := filepath.Join(realTempDir(t), "mnt")
	must(t, os.MkdirAll(mnt, 0o755))
	fakeMount(t, mnt)
	local := filepath.Join(e.Home, "cache")
	other := filepath.Join(e.Home, "photos-export")
	writeFile(t, local, "local cache")
	writeFile(t, other, "only copy")
	items := []plan.Item{itemFor(t, "0001", local, 11), itemFor(t, "0002", other, 9)}
	run := startRun(t, e, items, now)
	must(t, Quarantine(e, run, items[0]))
	must(t, moveItem(run, items[1], volumeRunDir(mnt, run.ID)))
	must(t, run.Close(now, true))
	must(t, os.Rename(mnt, mnt+"-unplugged"))

	_, err := Empty(e, run.ID)
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), mnt) {
		t.Fatalf("Empty = %v, want ErrUnavailable naming %s", err, mnt)
	}
	if !exists(t, filepath.Join(e.QuarantineDir(), run.ID, "0001-cache")) {
		t.Fatal("Empty deleted the local quarantine")
	}
	if got := strings.Join(journalTypes(t, e, run.ID), ","); strings.Contains(got, runlog.EventEmpty) {
		t.Fatalf("journal = %s, want no empty event", got)
	}
	freed, err := Expire(e, now.Add(30*24*time.Hour), DefaultMaxAge, DefaultMaxBytes)
	if err != nil || freed != 0 {
		t.Fatalf("Expire = %d, %v; want 0, nil", freed, err)
	}
	if s := summary(t, e, run.ID); s.Status == runlog.StatusEmptied {
		t.Fatal("the run was recorded as emptied")
	}

	must(t, os.Rename(mnt+"-unplugged", mnt))
	restored, err := Restore(e, run.ID)
	must(t, err)
	if len(restored) != 2 || readFile(t, local) != "local cache" || readFile(t, other) != "only copy" {
		t.Fatalf("restored %+v; want both items back", restored)
	}
}

// A volume counts as mounted only when its mount point is on another device
// than the folder that holds it. The positive case needs a real volume and
// runs in vm_test.go (TestSecondVolumeQuarantine empties one).
func TestIsMountPointRefusesPlainFolders(t *testing.T) {
	dir := t.TempDir()
	must(t, os.Symlink("/", filepath.Join(dir, "link-to-root")))
	for _, p := range []string{dir, filepath.Join(dir, "missing"), filepath.Join(dir, "link-to-root")} {
		mounted, err := isMountPoint(p)
		must(t, err)
		if mounted {
			t.Errorf("isMountPoint(%s) = true, want false", p)
		}
	}
}
