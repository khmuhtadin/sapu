// Package safefile writes Sapu's own small files (snapshots, settings,
// whitelist, plans, journals, logs) without ever writing into something
// else: never through a symlink, never through a second hard link, never
// into a file another user owns, and never outside the folder the file
// belongs to.
//
// Every write names a root: the folder the file belongs to (the state
// folder, the LaunchAgents folder, the folder of a plan file the user
// named). The root itself may be reached through a symlink: /var is
// /private/var on macOS, and people keep ~/.sapu or ~/.config in a dotfiles
// folder. Below the root:
//   - every folder down to the file is opened one at a time, relative to
//     the one above it, and none may be a symlink. A folder that is open
//     stays the same folder even if its name is swapped for a symlink
//     afterwards, so the write cannot be led out of the root;
//   - the file is opened inside the last folder without following a symlink
//     and without truncating, and the opened file is checked (a regular file
//     with one link, owned by this user) before anything is truncated or
//     written.
package safefile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// ErrRefused means the path is not a plain file Sapu may write.
var ErrRefused = errors.New("refusing to write: not a plain file of yours")

// Write replaces the contents of path with data, creating it with perm if
// it does not exist. root is the folder path belongs to; see the package
// comment for what is refused.
func Write(root, path string, data []byte, perm os.FileMode) error {
	f, err := Open(root, path, os.O_WRONLY|os.O_CREATE, perm)
	if err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return errors.Join(err, f.Close())
	}
	if _, err := f.Write(data); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}

// Open opens path with flag (os.O_WRONLY|os.O_APPEND, ...) and perm, after
// the checks of the package comment, and checks the opened file before it
// returns it. flag must not hold os.O_TRUNC: truncate after Open instead, so
// nothing is cut before the checks pass.
func Open(root, path string, flag int, perm os.FileMode) (*os.File, error) {
	dir, err := openDir(root, filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	// O_NONBLOCK: opening a FIFO to write would otherwise wait for a reader
	// before check could refuse it. A regular file gets blocking I/O back.
	fd, err := openat(dir, filepath.Base(path), flag|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, uint32(perm.Perm()))
	if cerr := unix.Close(dir); err == nil && cerr != nil {
		err = errors.Join(cerr, unix.Close(fd))
	}
	if err == nil {
		if err = unix.SetNonblock(fd, false); err != nil {
			err = errors.Join(err, unix.Close(fd))
		}
	}
	switch {
	case errors.Is(err, unix.ELOOP):
		return nil, fmt.Errorf("%w: %s is a symlink", ErrRefused, path)
	case errors.Is(err, unix.ENXIO):
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrRefused, path)
	case err != nil:
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path) //nolint:gosec // G115: an fd from a successful open is never negative
	if err := check(f, path); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

// openDir opens dir, which must be root or inside it, and returns its file
// descriptor. root is opened by name (it may be a symlink); every folder
// below it is opened relative to the one above, and a symlink is refused.
func openDir(root, dir string) (int, error) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(dir))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return -1, fmt.Errorf("%w: %s is not inside %s", ErrRefused, dir, root)
	}
	fd, err := openat(unix.AT_FDCWD, root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &fs.PathError{Op: "open", Path: root, Err: err}
	}
	if rel == "." {
		return fd, nil
	}
	at := root
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		at = filepath.Join(at, name)
		next, err := openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if cerr := unix.Close(fd); err == nil && cerr != nil {
			err = errors.Join(cerr, unix.Close(next))
		}
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return -1, fmt.Errorf("%w: %s is not a real folder", ErrRefused, at)
		}
		if err != nil {
			return -1, &fs.PathError{Op: "open", Path: at, Err: err}
		}
		fd = next
	}
	return fd, nil
}

// openat is unix.Openat, retried when a signal interrupts it.
func openat(dir int, name string, flag int, perm uint32) (int, error) {
	for {
		fd, err := unix.Openat(dir, name, flag, perm)
		if err != unix.EINTR {
			return fd, err
		}
	}
}

// check refuses an opened file that is not a regular file with one link,
// owned by this user.
func check(f *os.File, path string) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !ok || !fi.Mode().IsRegular():
		return fmt.Errorf("%w: %s is not a regular file", ErrRefused, path)
	case st.Nlink != 1:
		return fmt.Errorf("%w: %s has other names (hard links)", ErrRefused, path)
	case int(st.Uid) != os.Getuid():
		return fmt.Errorf("%w: %s belongs to another user", ErrRefused, path)
	}
	return nil
}

// Exists reports whether path exists, without following a symlink.
func Exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
