// Package fsop is the ONLY code in Sapu that moves or deletes files.
//
// Invariants (each one has a test):
//  1. Quarantine is a rename() on the same volume. Items on the volume of
//     env.StateDir go to <StateDir>/quarantine/<run_id>/<item_id>-<name>;
//     items on another volume go to <mount>/.sapu-quarantine/<uid>/<run_id>/.
//     Nothing is ever copied, so contents, mode, xattrs and hard links are
//     preserved exactly.
//  2. Before moving, Quarantine re-reads the item's identity and returns
//     ErrChanged or ErrGone instead of moving anything else. After the
//     rename it reads the identity of what it moved: if something replaced
//     the item in between, that object is renamed back and Quarantine
//     returns ErrChanged. If its old path is taken again by then, it stays
//     in quarantine, where undo finds it, and Quarantine returns ErrStranded.
//  3. Quarantine refuses paths that are not absolute and clean, that are not
//     strictly inside env.Home, strictly inside /Volumes/<name>/, or an app
//     bundle in /Applications (/Applications/X.app or one folder deeper, for
//     sapu uninstall), or that equal, contain or sit inside env.StateDir or
//     a quarantine folder.
//  4. Every move is journaled: move_begin is fsynced before the rename and
//     move_done after it, so an interrupted run can always be undone.
//  5. Restore never overwrites. If the original path exists again, the item
//     is restored next to it as "<name> (sapu <run_id>)" and reported as a
//     conflict.
//  6. Deleting for good (Empty, Expire) only ever happens inside quarantine
//     folders; any other path is refused. Right before deleting, the run
//     folder and the quarantine folders above it must be real folders, not
//     symlinks, owned by this user, and the run folder's real path must be
//     the one it should have.
//  7. Empty deletes nothing and logs nothing while a volume that, by the
//     journal, still holds items of the run is not mounted. It returns
//     ErrUnavailable, so undo still works when the volume comes back.
//     Expire skips such runs.
//  8. One sapu process at a time changes a state folder: Quarantine,
//     Restore, Empty and Expire hold Lock while they work and fail with
//     ErrLocked when another process holds it.
//  9. QuarantineAndClone replaces a duplicate by an APFS clone of the copy
//     that stays: it moves the duplicate into quarantine as above, then makes
//     the clone inside the run's quarantine folder, gives it the duplicate's
//     mode and mtime, journals its identity (clone_begin) and only then
//     renames it into the duplicate's place, never over anything. If any of
//     that fails, the duplicate simply stays in quarantine (clone_failed).
//  10. Restore deletes a clone only while the file at its path still has
//     exactly the identity journaled for it: it moves that file into the
//     run's quarantine folder, checks the identity again there and only then
//     unlinks it, so a file that took its place in between is put back, not
//     deleted. A clone that was changed or replaced stays, and the duplicate
//     is restored beside it.
//
// Every rename uses renamex_np with RENAME_EXCL, so the kernel itself refuses
// to replace an existing file, even one that appears after Sapu looked.
//
// Implemented in Wave 1 (W1-A).
package fsop

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

// Retention defaults (owner decision, 8 Nov 2026).
const (
	DefaultMaxAge   = 7 * 24 * time.Hour
	DefaultMaxBytes = 20_000_000_000 // 20 GB, as Sapu prints sizes
)

var (
	ErrChanged     = errors.New("item changed since the plan was made")
	ErrGone        = errors.New("item no longer exists")
	ErrRefused     = errors.New("path is outside the folders Sapu may change")
	ErrStranded    = errors.New("something replaced the item while it was being moved and could not be put back; it is in quarantine and undo restores it")
	ErrUnavailable = errors.New("a volume that holds quarantined items is not mounted")
	ErrLocked      = errors.New("another sapu is working on this state folder; try again when it finishes")
	ErrNotCloned   = errors.New("moved to quarantine, clone not placed")
)

type Restored struct {
	From     string `json:"from"` // path inside quarantine
	To       string `json:"to"`   // where it was restored
	Conflict bool   `json:"conflict"`
}

// volumeQuarantine is the quarantine folder at the root of other volumes.
const volumeQuarantine = ".sapu-quarantine"

// afterStep runs after every journal write and every rename. Tests replace
// it to stop a run at any step, as a crash would.
var afterStep = func() error { return nil }

// mountOf returns the mount point of the volume that holds path. Tests
// replace it to stand in for a second volume.
var mountOf = func(path string) (string, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "", fmt.Errorf("statfs %s: %w", path, err)
	}
	return unix.ByteSliceToString(st.Mntonname[:]), nil
}

// isMountPoint reports whether dir is the root of a mounted volume: it is on
// another device than the folder that holds it. A volume that is not mounted
// leaves nothing, or a plain folder, at its mount point. Tests replace it to
// stand in for a second volume.
var isMountPoint = func(dir string) (bool, error) {
	id, err := lstatID(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	parent, err := lstatID(filepath.Dir(dir))
	if err != nil {
		return false, err
	}
	return id.dev != parent.dev, nil
}

// cloneFile makes dst an APFS clone of the file src. It never follows a
// symlink and never replaces dst. Tests replace it to stand in for a volume
// that cannot clone.
var cloneFile = func(src, dst string) error {
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err != nil {
		return &os.LinkError{Op: "clonefile", Old: src, New: dst, Err: err}
	}
	return nil
}

// lockWait is how long Lock retries a busy lock before it reports ErrLocked.
// A child process that another goroutine just forked (lsof, for the open
// files check) holds a copy of every open file until it execs, which keeps a
// just-released flock busy for a moment. A real second sapu still holds the
// lock after lockWait.
var lockWait = 2 * time.Second

// flockWithin takes an exclusive flock on f, retrying while it is busy, for
// at most wait.
func flockWithin(f *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EWOULDBLOCK) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lockFile is the file in the state folder that Lock holds.
const lockFile = "lock"

// locks holds the lock files this process has locked, by path. A flock
// belongs to an open file, so a second flock of the same file in this process
// would fail; counting makes Lock reentrant instead.
var (
	locksMu sync.Mutex
	locks   = map[string]*heldLock{}
)

type heldLock struct {
	f     *os.File
	count int
}

// Lock takes the lock of e's state folder, an exclusive flock on
// <StateDir>/lock, so that only one sapu process at a time changes the state
// folder and the quarantine. It creates the state folder (0700) if needed.
// When another process holds the lock it fails at once with ErrLocked.
//
// Within one process Lock is reentrant: code that holds it may call
// Quarantine, Restore, Empty and Expire, which take it too. The lock is
// released when every unlock has been called, or when the process ends.
func Lock(e env.Env) (unlock func(), err error) {
	if !filepath.IsAbs(e.StateDir) {
		return nil, fmt.Errorf("lock: state folder %q is not an absolute path", e.StateDir)
	}
	path := filepath.Join(e.StateDir, lockFile)
	locksMu.Lock()
	defer locksMu.Unlock()
	l := locks[path]
	if l == nil {
		if err := os.MkdirAll(e.StateDir, 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE|unix.O_NOFOLLOW, 0o600)
		if err != nil {
			return nil, err
		}
		if err := flockWithin(f, lockWait); err != nil {
			_ = f.Close() // the flock error is the one to report
			if errors.Is(err, unix.EWOULDBLOCK) {
				return nil, fmt.Errorf("%w (%s)", ErrLocked, e.StateDir)
			}
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		l = &heldLock{f: f}
		locks[path] = l
	}
	l.count++
	var once sync.Once
	return func() {
		once.Do(func() {
			locksMu.Lock()
			defer locksMu.Unlock()
			l.count--
			if l.count == 0 {
				delete(locks, path)
				_ = l.f.Close() // closing releases the flock, error or not
			}
		})
	}, nil
}

// Quarantine moves one plan item into the run's quarantine. It checks the
// path first, then holds Lock while it moves.
func Quarantine(e env.Env, run *runlog.Run, it plan.Item) error {
	return quarantine(e, run, it, false)
}

// QuarantineAndClone moves a duplicate file into quarantine like Quarantine,
// then puts an APFS clone of its kept copy (it.CloneFrom, which must equal
// it.KeepCopy.Path) at its path (invariant 9). Before moving, the kept copy
// must still have its planned identity (else ErrChanged) and be a regular
// file on the item's volume as long as the item (else ErrRefused); nothing
// is moved then. When the item was moved but no clone took its place, the
// error wraps ErrNotCloned: the item stays in quarantine and undo restores it.
func QuarantineAndClone(e env.Env, run *runlog.Run, it plan.Item) error {
	if it.KeepCopy == nil || it.CloneFrom == "" || it.CloneFrom != it.KeepCopy.Path {
		return fmt.Errorf("%w: %s: clone_from must be the path of the kept copy", ErrRefused, it.Path)
	}
	if !lettersAndDigits(it.ID) {
		return fmt.Errorf("%w: %s: an item replaced by a clone needs an id of letters and digits, not %q", ErrRefused, it.Path, it.ID)
	}
	keep := it.KeepCopy.Path
	if !filepath.IsAbs(keep) || filepath.Clean(keep) != keep || keep == it.Path {
		return fmt.Errorf("%w: %s: the kept copy %q is not another absolute, clean path", ErrRefused, it.Path, keep)
	}
	if it.Identity.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return fmt.Errorf("%w: %s: only a file can be replaced by a clone", ErrRefused, it.Path)
	}
	return quarantine(e, run, it, true)
}

func quarantine(e env.Env, run *runlog.Run, it plan.Item, clone bool) error {
	if it.ID == "" || strings.ContainsAny(it.ID, "/\x00") {
		return fmt.Errorf("%w: bad item id %q", ErrRefused, it.ID)
	}
	parent, err := checkPath(e, it.Path)
	if err != nil {
		return err
	}
	unlock, err := Lock(e)
	if err != nil {
		return err
	}
	defer unlock()
	id, err := plan.IdentityOf(it.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrGone, it.Path)
	}
	if err != nil {
		return err
	}
	dir, err := runDir(e, run.ID, id.Dev, parent)
	if err != nil {
		return err
	}
	if clone {
		if err := checkKeeper(it); err != nil {
			return err
		}
	}
	if err := moveItem(run, it, dir); err != nil {
		return err
	}
	if !clone {
		return nil
	}
	return placeClone(e, run, it, dir)
}

// checkKeeper checks that the kept copy of a duplicate can be the source of
// the clone that takes the duplicate's place: it still has the planned
// identity (else ErrChanged), and it is a regular file on the duplicate's
// volume and as long as the duplicate (else ErrRefused).
func checkKeeper(it plan.Item) error {
	keep := it.KeepCopy
	id, err := plan.IdentityOf(keep.Path)
	if err != nil {
		return fmt.Errorf("%w: the copy that stays: %v", ErrChanged, err)
	}
	switch {
	case id != keep.Identity:
		return fmt.Errorf("%w: the copy that stays (%s) changed", ErrChanged, keep.Path)
	case id.Mode&syscall.S_IFMT != syscall.S_IFREG:
		return fmt.Errorf("%w: the copy that stays (%s) is not a file", ErrRefused, keep.Path)
	case id.Dev != it.Identity.Dev:
		return fmt.Errorf("%w: the copy that stays (%s) is on another volume", ErrRefused, keep.Path)
	case id.Size != it.Identity.Size:
		return fmt.Errorf("%w: the copy that stays (%s) is not as long as %s", ErrRefused, keep.Path, it.Path)
	}
	return nil
}

// lettersAndDigits reports whether id is not empty and only ASCII letters
// and digits (engine.Plan numbers items "0001", "0002", ...).
func lettersAndDigits(id string) bool {
	for _, r := range id {
		isLetterOrDigit := '0' <= r && r <= '9' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z'
		if !isLetterOrDigit {
			return false
		}
	}
	return id != ""
}

// cloneName is where a clone of item id waits inside the run folder dir:
// before it takes the item's place, and when undo moves it aside to delete
// it. Quarantined items are named "<id>-<name>" (or "<id>" when that is too
// long), and the id of an item with a clone has no "." or "-", so only an
// item whose own id ends in ".clone" could have this name.
func cloneName(dir, id string) string {
	return filepath.Join(dir, id+".clone")
}

// placeClone puts a clone of the kept copy at it.Path, which moveItem just
// emptied. The clone is made in dir, the run's quarantine folder on the
// item's volume, and given the item's mode and mtime. Its identity is
// journaled (clone_begin) before it is renamed into place, so undo can
// always tell it from anything else. On failure the item stays in
// quarantine, clone_failed is journaled and the error wraps ErrNotCloned.
func placeClone(e env.Env, run *runlog.Run, it plan.Item, dir string) error {
	keep := it.KeepCopy.Path
	fail := func(why error) error {
		ev := runlog.Event{Time: time.Now(), Type: runlog.EventCloneFailed, Item: it.ID, From: keep, To: it.Path, Reason: why.Error()}
		return errors.Join(fmt.Errorf("%w: %s: %v", ErrNotCloned, it.Path, why), run.Log(ev))
	}
	if err := checkKeeper(it); err != nil {
		return fail(err)
	}
	tmp := cloneName(dir, it.ID)
	if err := cloneFile(keep, tmp); err != nil {
		return fail(err)
	}
	made, err := plan.IdentityOf(tmp)
	if err != nil {
		return fail(err)
	}
	// discard deletes the clone from quarantine again, if it is still the
	// file cloneFile made.
	discard := func() error {
		now, err := plan.IdentityOf(tmp)
		if err != nil {
			return err
		}
		if now.Dev != made.Dev || now.Ino != made.Ino {
			return fmt.Errorf("%w: %s is not the clone Sapu made", ErrRefused, tmp)
		}
		return removeOwnFile(e, run.ID, tmp, now)
	}
	id, err := setModeAndMtime(tmp, it.Identity)
	if err != nil {
		return fail(errors.Join(err, discard()))
	}
	if id.Size != it.Identity.Size || id.Mode != it.Identity.Mode || id.MtimeNs != it.Identity.MtimeNs {
		return fail(errors.Join(fmt.Errorf("the clone did not take the size, mode and mtime of %s", it.Path), discard()))
	}

	ev := runlog.Event{Type: runlog.EventCloneBegin, Item: it.ID, From: keep, To: it.Path, Identity: &id}
	if err := logStep(run, ev); err != nil {
		return err
	}
	if err := renameExcl(tmp, it.Path); err != nil {
		return fail(errors.Join(err, removeOwnFile(e, run.ID, tmp, id)))
	}
	if err := afterStep(); err != nil {
		return err
	}
	ev.Type = runlog.EventCloneDone
	return logStep(run, ev)
}

// setModeAndMtime gives the file at path the permission bits and the
// modification time of want, and returns its identity after that.
func setModeAndMtime(path string, want plan.Identity) (plan.Identity, error) {
	if err := unix.Fchmodat(unix.AT_FDCWD, path, want.Mode&0o7777, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return plan.Identity{}, fmt.Errorf("chmod %s: %w", path, err)
	}
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return plan.Identity{}, fmt.Errorf("stat %s: %w", path, err)
	}
	times := []unix.Timespec{st.Atim, unix.NsecToTimespec(want.MtimeNs)}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, path, times, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return plan.Identity{}, fmt.Errorf("set times of %s: %w", path, err)
	}
	return plan.IdentityOf(path)
}

// removeOwnFile deletes a clone Sapu made, from inside a run's quarantine
// folder. Right before it unlinks, the folder must pass checkRealRunDir and
// the file must still be a regular file with exactly the identity want.
// Nothing else writes in Sapu's quarantine folders, so nothing can take its
// place in between.
func removeOwnFile(e env.Env, runID, path string, want plan.Identity) error {
	if err := checkRealRunDir(e, runID, filepath.Dir(path)); err != nil {
		return err
	}
	got, err := plan.IdentityOf(path)
	if err != nil {
		return err
	}
	if got != want || got.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return fmt.Errorf("%w: %s is not the clone Sapu made", ErrRefused, path)
	}
	if err := unix.Unlink(path); err != nil {
		return fmt.Errorf("unlink %s: %w", path, err)
	}
	return nil
}

// moveItem renames the item into dir, journaling before and after, and
// checks that what it moved is the item.
func moveItem(run *runlog.Run, it plan.Item, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := it.ID + "-" + filepath.Base(it.Path)
	if len(name) > 255 {
		name = it.ID
	}
	to := filepath.Join(dir, name)
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("quarantine %s: %s already exists (item id used twice?)", it.Path, to)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	id, err := plan.IdentityOf(it.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrGone, it.Path)
	}
	if err != nil {
		return err
	}
	if id != it.Identity {
		return fmt.Errorf("%w: %s", ErrChanged, it.Path)
	}

	ev := runlog.Event{Type: runlog.EventMoveBegin, Item: it.ID, From: it.Path, To: to, Bytes: it.Bytes}
	if err := logStep(run, ev); err != nil {
		return err
	}
	if err := renameExcl(it.Path, to); err != nil {
		failed := runlog.Event{Time: time.Now(), Type: runlog.EventMoveFailed, Item: it.ID, From: it.Path, To: to, Reason: err.Error()}
		return errors.Join(fmt.Errorf("quarantine %s: %w", it.Path, err), run.Log(failed))
	}
	if err := afterStep(); err != nil {
		return err
	}
	if err := checkMoved(run, it, to); err != nil {
		return err
	}
	ev.Type = runlog.EventMoveDone
	return logStep(run, ev)
}

// checkMoved reads the identity of what the rename just moved to `to`.
// Something may have replaced the item between the last identity check and
// the rename. That object was never planned, so it goes back to where it came
// from, without replacing anything (RENAME_EXCL), and the move is journaled
// as failed. If its old path is taken again, it stays in quarantine without a
// move_failed event, so undo, which works from the move_begin record, still
// restores it.
func checkMoved(run *runlog.Run, it plan.Item, to string) error {
	id, err := plan.IdentityOf(to)
	if err != nil {
		return fmt.Errorf("check moved item %s: %w", to, err)
	}
	if id == it.Identity {
		return nil
	}
	if err := renameExcl(to, it.Path); err != nil {
		return fmt.Errorf("%w: %s is now %s in run %s (putting it back failed: %v)", ErrStranded, it.Path, to, run.ID, err)
	}
	failed := runlog.Event{Time: time.Now(), Type: runlog.EventMoveFailed, Item: it.ID, From: it.Path, To: to, Reason: "replaced while moving; put back"}
	if err := run.Log(failed); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s was replaced while moving; put back", ErrChanged, it.Path)
}

// logStep stamps and logs one event, then runs the test hook.
func logStep(run *runlog.Run, ev runlog.Event) error {
	ev.Time = time.Now()
	if err := run.Log(ev); err != nil {
		return err
	}
	return afterStep()
}

func renameExcl(from, to string) error {
	if err := unix.RenamexNp(from, to, unix.RENAME_EXCL); err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}

// checkPath applies the path rules of invariant 3 and returns the item's
// parent folder with symlinks resolved.
func checkPath(e env.Env, path string) (string, error) {
	refuse := func(why string) error { return fmt.Errorf("%w: %s: %s", ErrRefused, path, why) }
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", refuse("not an absolute, clean path")
	}
	home := filepath.Clean(e.Home)
	if !filepath.IsAbs(home) || home == "/" || !filepath.IsAbs(e.StateDir) {
		return "", refuse("home or state folder is not an absolute path")
	}
	if !within(path, home) && !onVolume(path) && !isApp(path) {
		return "", refuse("not inside the home folder, a volume or /Applications")
	}
	if inVolumeQuarantine(path) {
		return "", refuse("inside a quarantine folder")
	}

	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %s", ErrGone, path)
	}
	if err != nil {
		return "", refuse(err.Error())
	}
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", refuse(err.Error())
	}
	real := filepath.Join(parent, filepath.Base(path))
	if _, err := os.Lstat(real); errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %s", ErrGone, path)
	}
	if !within(real, realHome) && !onVolume(real) && !isApp(real) {
		return "", refuse("a symlinked folder leads outside the home folder")
	}
	if inVolumeQuarantine(real) {
		return "", refuse("inside a quarantine folder")
	}
	if err := checkNotState(e, real); err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrRefused, path, err)
	}
	return parent, nil
}

// within reports whether p is strictly inside dir. Both must be clean.
func within(p, dir string) bool {
	return p != dir && strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// isApp reports whether p is an app bundle in /Applications:
// /Applications/X.app or /Applications/<folder>/X.app. Nothing else in
// /Applications qualifies, and nothing inside a bundle does.
func isApp(p string) bool {
	rest, ok := strings.CutPrefix(p, "/Applications/")
	if !ok || !strings.HasSuffix(strings.ToLower(p), ".app") {
		return false
	}
	segs := strings.Split(rest, "/")
	if len(segs) > 2 {
		return false
	}
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return len(segs) == 1 || !strings.HasSuffix(strings.ToLower(segs[0]), ".app")
}

// onVolume reports whether p is strictly inside /Volumes/<name>/.
func onVolume(p string) bool {
	rest, ok := strings.CutPrefix(p, "/Volumes/")
	if !ok {
		return false
	}
	name, sub, ok := strings.Cut(rest, "/")
	return ok && name != "" && sub != ""
}

// inVolumeQuarantine reports whether any folder of p is a per-volume
// quarantine folder. APFS ignores case, so the comparison does too.
func inVolumeQuarantine(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.EqualFold(seg, volumeQuarantine) {
			return true
		}
	}
	return false
}

type fileID struct{ dev, ino uint64 }

func lstatID(path string) (fileID, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return fileID{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, fmt.Errorf("no stat data for %s", path)
	}
	return fileID{uint64(st.Dev), st.Ino}, nil
}

// checkNotState fails when real (a path whose folders have no symlinks) is,
// contains or sits inside the state folder or the quarantine folder. It
// compares device and inode numbers, so case differences and symlinks cannot
// hide a match. The state folder must exist: Start created it.
func checkNotState(e env.Env, real string) error {
	item, err := lstatID(real)
	if err != nil {
		return err
	}
	guarded := map[fileID]bool{} // the state and quarantine folders
	above := map[fileID]bool{}   // those folders and every folder above them
	for _, dir := range []string{e.StateDir, e.QuarantineDir()} {
		rdir, err := filepath.EvalSymlinks(dir)
		if errors.Is(err, fs.ErrNotExist) && dir != e.StateDir {
			continue // no quarantine folder yet
		}
		if err != nil {
			return err
		}
		for p := rdir; ; p = filepath.Dir(p) {
			id, err := lstatID(p)
			if err != nil {
				return err
			}
			if p == rdir {
				guarded[id] = true
			}
			above[id] = true
			if p == "/" {
				break
			}
		}
	}
	if above[item] {
		return errors.New("it is or contains Sapu's own folder")
	}
	for p := real; p != "/"; p = filepath.Dir(p) {
		id, err := lstatID(p)
		if err != nil {
			return err
		}
		if guarded[id] {
			return errors.New("it is inside Sapu's own folder")
		}
	}
	return nil
}

// runDir returns the quarantine folder of a run for an item on device dev
// whose parent folder is parent.
func runDir(e env.Env, runID string, dev uint64, parent string) (string, error) {
	q := e.QuarantineDir()
	if err := os.MkdirAll(q, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Stat(q)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("no stat data for %s", q)
	}
	if uint64(st.Dev) == dev {
		return filepath.Join(q, runID), nil
	}
	mount, err := mountOf(parent)
	if err != nil {
		return "", err
	}
	return volumeRunDir(mount, runID), nil
}

func volumeRunDir(mount, runID string) string {
	return filepath.Join(mount, volumeQuarantine, strconv.Itoa(os.Getuid()), runID)
}

// checkRunDir fails unless dir is exactly the quarantine folder of runID:
// <QuarantineDir>/<runID> or <mount>/.sapu-quarantine/<uid>/<runID>.
func checkRunDir(e env.Env, runID, dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || filepath.Base(dir) != runID {
		return fmt.Errorf("%w: %s is not a quarantine folder of run %s", ErrRefused, dir, runID)
	}
	if dir == filepath.Join(e.QuarantineDir(), runID) {
		return nil
	}
	mount, err := mountOf(dir)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}
	if dir != volumeRunDir(mount, runID) {
		return fmt.Errorf("%w: %s is not a quarantine folder of run %s", ErrRefused, dir, runID)
	}
	return nil
}

// checkRealRunDir is checkRunDir plus a look at the disk, made right before
// deleting: the run folder and the quarantine folders above it must be real
// folders, not symlinks, owned by this user, and the run folder must resolve
// to the real path it should have. For the home quarantine that is
// <real StateDir>/quarantine/<runID> (the state folder may be reached through
// a symlink, as /var is); for a volume it is the path itself. Otherwise a
// symlink on the way could lead the deletion outside quarantine.
func checkRealRunDir(e env.Env, runID, dir string) error {
	if err := checkRunDir(e, runID, dir); err != nil {
		return err
	}
	refuse := func(why string) error { return fmt.Errorf("%w: %s: %s", ErrRefused, dir, why) }
	var want string
	var folders []string // must be real folders owned by this user
	if dir == filepath.Join(e.QuarantineDir(), runID) {
		state, err := filepath.EvalSymlinks(e.StateDir)
		if err != nil {
			return refuse(err.Error())
		}
		want = filepath.Join(state, filepath.Base(e.QuarantineDir()), runID)
		folders = []string{e.QuarantineDir(), dir}
	} else {
		want = dir
		folders = []string{filepath.Dir(filepath.Dir(dir)), filepath.Dir(dir), dir}
	}
	for _, f := range folders {
		fi, err := os.Lstat(f)
		if err != nil {
			return refuse(err.Error())
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() {
			return refuse(f + " is not a real folder (a symlink?) owned by this user")
		}
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return refuse(err.Error())
	}
	if real != want {
		return refuse("it leads to " + real)
	}
	return nil
}

// checkAvailable fails with ErrUnavailable when a move of the run that the
// journal does not record as failed or restored went to a volume that is not
// mounted now. Its item may still be there, and recording the run as emptied
// would make undo refuse it when the volume comes back.
func checkAvailable(e env.Env, runID string, moves []move) error {
	local := filepath.Join(e.QuarantineDir(), runID)
	for _, m := range moves {
		dir := filepath.Dir(m.to)
		if m.failed || m.restored || dir == local {
			continue
		}
		mount := filepath.Dir(filepath.Dir(filepath.Dir(dir)))
		if dir != volumeRunDir(mount, runID) {
			return fmt.Errorf("%w: %s is not a quarantine folder of run %s", ErrRefused, dir, runID)
		}
		mounted, err := isMountPoint(mount)
		if err != nil {
			return err
		}
		if !mounted {
			return fmt.Errorf("%w: %s holds items of run %s; connect it and try again", ErrUnavailable, mount, runID)
		}
	}
	return nil
}

// move is one move_begin from the journal and what happened to it later.
type move struct {
	item, from, to string
	bytes          int64
	failed         bool           // move_failed: the item never left from
	restored       bool           // restore_done: the item went back
	clone          *plan.Identity // clone_begin: the clone Sapu made to take the item's place at from
	uncloning      bool           // unclone_begin without unclone_done or unclone_failed: undo moved what was at from to the clone name
}

// movesOf reads the moves of a run in journal order. A quarantine path is
// unique within a run, so it ties the later events to their move. Clone
// events belong to the last move of their item, and only when their path is
// that move's original path.
func movesOf(evs []runlog.Event) (moves []move, emptied bool) {
	byTo := map[string]int{}
	byItem := map[string]int{}
	ofItem := func(item, from string) *move {
		if i, ok := byItem[item]; ok && moves[i].from == from {
			return &moves[i]
		}
		return nil
	}
	for _, ev := range evs {
		switch ev.Type {
		case runlog.EventMoveBegin:
			byTo[ev.To] = len(moves)
			byItem[ev.Item] = len(moves)
			moves = append(moves, move{item: ev.Item, from: ev.From, to: ev.To, bytes: ev.Bytes})
		case runlog.EventCloneBegin:
			if m := ofItem(ev.Item, ev.To); m != nil && ev.Identity != nil {
				id := *ev.Identity
				m.clone = &id
			}
		case runlog.EventUncloneBegin:
			if m := ofItem(ev.Item, ev.From); m != nil {
				m.uncloning = true
			}
		case runlog.EventUncloneDone, runlog.EventUncloneFailed:
			if m := ofItem(ev.Item, ev.From); m != nil {
				m.uncloning = false
			}
		case runlog.EventMoveFailed:
			if i, ok := byTo[ev.To]; ok {
				moves[i].failed = true
			}
		case runlog.EventRestoreDone:
			if i, ok := byTo[ev.From]; ok {
				moves[i].restored = true
			}
		case runlog.EventEmpty:
			emptied = true
		}
	}
	return moves, emptied
}

// held returns the moves whose item is in quarantine right now. It looks at
// the disk, so moves cut short by a crash count only if the rename happened.
func held(moves []move) ([]move, error) {
	var out []move
	for _, m := range moves {
		if m.failed || m.restored {
			continue
		}
		_, err := os.Lstat(m.to)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// Restore moves every quarantined item of a run back, newest move first.
// It also restores items of interrupted runs (move_begin without move_done)
// by checking where each one actually is. It holds Lock while it works.
func Restore(e env.Env, runID string) ([]Restored, error) {
	unlock, err := Lock(e)
	if err != nil {
		return nil, err
	}
	defer unlock()
	evs, err := runlog.Events(e, runID)
	if err != nil {
		return nil, err
	}
	moves, emptied := movesOf(evs)
	if emptied {
		return nil, fmt.Errorf("run %s: its quarantine was emptied, nothing is left to restore", runID)
	}
	run, err := runlog.Open(e, runID)
	if err != nil {
		return nil, err
	}
	out, err := restoreMoves(e, run, moves)
	return out, errors.Join(err, run.Close(time.Now(), false))
}

func restoreMoves(e env.Env, run *runlog.Run, moves []move) ([]Restored, error) {
	var out []Restored
	var errs []error
	for i := len(moves) - 1; i >= 0; i-- {
		m := moves[i]
		if m.failed || m.restored {
			continue
		}
		if err := checkRunDir(e, run.ID, filepath.Dir(m.to)); err != nil {
			errs = append(errs, err)
			continue
		}
		if m.clone != nil {
			fatal, err := unclone(e, run, m)
			if fatal {
				return out, errors.Join(append(errs, err)...)
			}
			if err != nil {
				errs = append(errs, err)
			}
			if errors.Is(err, ErrStranded) {
				continue // the item waits in quarantine until the file in the clone's place can go back
			}
		}
		if _, err := os.Lstat(m.to); errors.Is(err, fs.ErrNotExist) {
			continue // the rename never happened, or the item went back already
		} else if err != nil {
			errs = append(errs, err)
			continue
		}
		target, conflict := m.from, false
		if _, err := os.Lstat(m.from); err == nil {
			target, conflict = m.from+" (sapu "+run.ID+")", true
		} else if !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			errs = append(errs, err)
			continue
		}

		ev := runlog.Event{Type: runlog.EventRestoreBegin, Item: m.item, From: m.to, To: target}
		if err := logStep(run, ev); err != nil {
			return out, errors.Join(append(errs, err)...)
		}
		if err := renameExcl(m.to, target); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", m.from, err))
			continue
		}
		if err := afterStep(); err != nil {
			return out, errors.Join(append(errs, err)...)
		}
		ev.Type = runlog.EventRestoreDone
		if err := logStep(run, ev); err != nil {
			return out, errors.Join(append(errs, err)...)
		}
		out = append(out, Restored{From: m.to, To: target, Conflict: conflict})
	}
	if len(errs) > 0 {
		return out, errors.Join(errs...)
	}
	return out, removeEmptyRunDirs(e, run.ID, moves)
}

// unclone deletes the clone Sapu put where m's item was, so that the item can
// go back to its own place (invariant 10). It first settles what an
// interrupted earlier step left at the clone name. It touches the path only
// while the item is still in quarantine, and deletes only a file with exactly
// the clone's identity. Whatever else is at the path stays, and the item is
// restored beside it. fatal means the journal could not be written (or a
// test stopped the run), and Restore must stop.
func unclone(e env.Env, run *runlog.Run, m move) (fatal bool, err error) {
	hold := cloneName(filepath.Dir(m.to), m.item)
	if fatal, err := settleHold(e, run, m, hold); fatal || err != nil {
		return fatal, err
	}
	if _, err := os.Lstat(m.to); errors.Is(err, fs.ErrNotExist) {
		return false, nil // the item is not there to come back; the clone stays
	} else if err != nil {
		return false, err
	}
	id, err := plan.IdentityOf(m.from)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if id != *m.clone {
		return false, nil // changed or replaced since: it stays
	}

	ev := runlog.Event{Type: runlog.EventUncloneBegin, Item: m.item, From: m.from, To: hold}
	if err := logStep(run, ev); err != nil {
		return true, err
	}
	if err := renameExcl(m.from, hold); err != nil {
		// Nothing moved. The item is restored to its place, or beside
		// whatever is there.
		ev.Type, ev.Reason = runlog.EventUncloneFailed, err.Error()
		err := logStep(run, ev)
		return err != nil, err
	}
	if err := afterStep(); err != nil {
		return true, err
	}
	return settleHold(e, run, move{item: m.item, from: m.from, to: m.to, clone: m.clone, uncloning: true}, hold)
}

// settleHold deals with a file at the clone name hold of move m. The clone
// itself (left by a crash, or just moved aside by unclone) is deleted.
// Anything else was moved there from m.from by unclone, when something took
// the clone's place at the last moment, and it goes back without replacing
// anything. If its place is taken again, it stays in quarantine, and so
// does m's item, so that the next undo tries again; the error wraps
// ErrStranded. A file there that unclone did not move is left alone.
func settleHold(e env.Env, run *runlog.Run, m move, hold string) (fatal bool, err error) {
	id, err := plan.IdentityOf(hold)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ev := runlog.Event{Item: m.item, From: m.from, To: hold}
	if id == *m.clone {
		if err := removeOwnFile(e, run.ID, hold, id); err != nil {
			return false, err
		}
		if !m.uncloning {
			return false, nil
		}
		ev.Type = runlog.EventUncloneDone
		err := logStep(run, ev)
		return err != nil, err
	}
	if !m.uncloning {
		return false, nil
	}
	if err := renameExcl(hold, m.from); err != nil {
		return false, fmt.Errorf("%w: %s is now %s in run %s (putting it back failed: %v)", ErrStranded, m.from, hold, run.ID, err)
	}
	if err := afterStep(); err != nil {
		return true, err
	}
	ev.Type, ev.Reason = runlog.EventUncloneFailed, "it was not the clone; put back"
	err = logStep(run, ev)
	return err != nil, err
}

// removeEmptyRunDirs removes the run's quarantine folders once they are
// empty. rmdir cannot remove a folder that still holds anything.
func removeEmptyRunDirs(e env.Env, runID string, moves []move) error {
	for _, dir := range runDirs(e, runID, moves) {
		if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := checkRealRunDir(e, runID, dir); err != nil {
			return err
		}
		err := unix.Rmdir(dir)
		if err != nil && !errors.Is(err, unix.ENOTEMPTY) && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("rmdir %s: %w", dir, err)
		}
	}
	return nil
}

// runDirs lists every quarantine folder a run may have used.
func runDirs(e env.Env, runID string, moves []move) []string {
	dirs := []string{filepath.Join(e.QuarantineDir(), runID)}
	seen := map[string]bool{dirs[0]: true}
	for _, m := range moves {
		d := filepath.Dir(m.to)
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// Empty deletes the quarantine of one run for good and logs an "empty" event.
// It holds Lock while it works. It deletes nothing and logs nothing when a
// volume that still holds items of the run is not mounted (ErrUnavailable),
// or when any run folder fails checkRealRunDir (ErrRefused).
func Empty(e env.Env, runID string) (freed int64, err error) {
	unlock, err := Lock(e)
	if err != nil {
		return 0, err
	}
	defer unlock()
	evs, err := runlog.Events(e, runID)
	if err != nil {
		return 0, err
	}
	moves, emptied := movesOf(evs)
	if emptied {
		return 0, nil
	}
	if err := checkAvailable(e, runID, moves); err != nil {
		return 0, err
	}
	// Check every folder before deleting any. A folder that is gone (deleted
	// already, or on a mounted volume where it was removed) has nothing to
	// delete.
	dirs := runDirs(e, runID, moves)
	for _, dir := range dirs {
		if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := checkRealRunDir(e, runID, dir); err != nil {
			return 0, err
		}
	}
	inside, err := held(moves)
	if err != nil {
		return 0, err
	}
	for _, m := range inside {
		freed += m.bytes
	}

	run, err := runlog.Open(e, runID)
	if err != nil {
		return 0, err
	}
	for _, dir := range dirs {
		if err := removeRunDir(e, runID, dir); err != nil {
			return 0, errors.Join(err, run.Close(time.Now(), false))
		}
	}
	logErr := run.Log(runlog.Event{Time: time.Now(), Type: runlog.EventEmpty, Bytes: freed})
	if err := errors.Join(logErr, run.Close(time.Now(), false)); err != nil {
		return 0, err
	}
	return freed, nil
}

// removeRunDir deletes one quarantine folder of a run, checking right before
// that it is one (checkRealRunDir).
func removeRunDir(e env.Env, runID, dir string) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: %s is not a folder", ErrRefused, dir)
	}
	if err := checkRealRunDir(e, runID, dir); err != nil {
		return err
	}
	// Quarantined caches often hold read-only folders (the Go module cache),
	// which RemoveAll cannot empty. WalkDir visits a folder before reading it.
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir && !within(p, dir) {
			return fmt.Errorf("%w: %s", ErrRefused, p)
		}
		if !d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if perm := info.Mode().Perm(); perm&0o700 != 0o700 {
			return os.Chmod(p, perm|0o700)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := checkRealRunDir(e, runID, dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Expire empties runs older than maxAge, then the oldest runs until the total
// quarantine size is at most maxBytes. It holds Lock while it works. A run on
// a volume that is not mounted (ErrUnavailable from Empty) is skipped without
// an error, so a missing drive does not block cleaning. Without a runs folder
// there is nothing to expire, and Expire returns without creating anything.
func Expire(e env.Env, now time.Time, maxAge time.Duration, maxBytes int64) (freed int64, err error) {
	if _, err := os.Lstat(e.RunsDir()); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	unlock, err := Lock(e)
	if err != nil {
		return 0, err
	}
	defer unlock()
	runs, err := runlog.List(e)
	if err != nil {
		return 0, err
	}
	sizes, err := QuarantineSize(e)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, b := range sizes {
		total += b
	}
	cutoff := now.Add(-maxAge)
	var errs []error
	for i := len(runs) - 1; i >= 0; i-- { // oldest first
		r := runs[i]
		size, ok := sizes[r.ID]
		if !ok {
			continue
		}
		if !r.Started.Before(cutoff) && total <= maxBytes {
			continue
		}
		f, err := Empty(e, r.ID)
		if errors.Is(err, ErrUnavailable) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		freed += f
		total -= size
	}
	return freed, errors.Join(errs...)
}

// QuarantineSize returns the bytes held in quarantine, per run id.
func QuarantineSize(e env.Env) (map[string]int64, error) {
	runs, err := runlog.List(e)
	if err != nil {
		return nil, err
	}
	sizes := map[string]int64{}
	for _, r := range runs {
		if r.Status == runlog.StatusEmptied {
			continue
		}
		evs, err := runlog.Events(e, r.ID)
		if errors.Is(err, fs.ErrNotExist) {
			continue // a run cut short before its journal existed moved nothing
		}
		if err != nil {
			return nil, err
		}
		moves, _ := movesOf(evs)
		inside, err := held(moves)
		if err != nil {
			return nil, err
		}
		if len(inside) == 0 {
			continue
		}
		var b int64
		for _, m := range inside {
			b += m.bytes
		}
		sizes[r.ID] = b
	}
	return sizes, nil
}

// AgentLabelPrefix starts the label of every LaunchAgent Sapu installs.
const AgentLabelPrefix = "dev.sapu."

// RemoveOwnLaunchAgent deletes the LaunchAgent plist Sapu installed for
// label: exactly <Home>/Library/LaunchAgents/<label>.plist, where label
// starts with AgentLabelPrefix and has no slash. It must be a regular file
// owned by this user. A missing file is not an error. This is the only
// deletion outside quarantine folders, and it only ever removes Sapu's own
// file (sapu schedule off).
func RemoveOwnLaunchAgent(e env.Env, label string) error {
	if !strings.HasPrefix(label, AgentLabelPrefix) || strings.ContainsAny(label, "/\x00") || label == AgentLabelPrefix {
		return fmt.Errorf("%w: %q is not a Sapu agent label", ErrRefused, label)
	}
	path := filepath.Join(e.Home, "Library", "LaunchAgents", label+".plist")
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%w: %s is not a regular file owned by you", ErrRefused, path)
	}
	return os.Remove(path)
}
