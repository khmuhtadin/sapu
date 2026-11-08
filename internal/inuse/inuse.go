// Package inuse finds items that must be skipped because something is using
// them right now (blueprint S5).
//
// Snapshot runs `lsof` once and `ps` once and keeps the results, so checking
// thousands of items costs no extra processes. A path is busy when any open
// file, working directory or executable of a process is the path itself or
// inside it.
//
// The snapshot shows what was open when lsof ran, nothing later. Callers that
// work through many items take a new one as it ages (engine.Apply does).
//
// ChangedSince walks a folder and reports whether anything in it was
// modified after a given time. engine.Apply uses it to skip a folder whose
// contents changed after the plan was made, and CheckConditions uses it for
// modified_within.
//
// Implemented in Wave 1 (W1-C).
package inuse

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/khmuhtadin/sapu/internal/rules"
)

// Open is a snapshot of the open files and running processes of one user.
type Open struct {
	files []openFile // sorted by path
	procs []process
}

// openFile is one absolute path held by a process: an open file, its working
// directory (lsof fd "cwd") or its executable (fd "txt").
type openFile struct {
	path    string
	pid     int
	command string
}

// process is one line of `ps -axo pid=,comm=`. comm is the executable's path.
type process struct {
	pid  int
	comm string
}

// Snapshot records the open files and running processes of the current user.
func Snapshot(ctx context.Context) (*Open, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	lsof := exec.CommandContext(ctx, "lsof", "-n", "-P", "-w", "-F", "pcfn", "-u", strconv.Itoa(os.Getuid()))
	out, err := lsof.Output()
	// lsof exits 1 when some files vanish while it runs. That is fine as long
	// as it printed something.
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || len(out) == 0) {
		return nil, fmt.Errorf("lsof: %w", err)
	}
	files := parseLsof(string(out))
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	psOut, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return &Open{files: files, procs: parsePs(string(psOut))}, nil
}

// parseLsof reads `lsof -F pcfn` output: "p" starts a process, "c" is its
// command, "f" starts a file and "n" is that file's name. Every file counts,
// cwd and txt included, so the "f" line itself is not needed. Only absolute
// names are kept; sockets and pipes have other kinds of names.
func parseLsof(out string) []openFile {
	var files []openFile
	var pid int
	var command string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		value := line[1:]
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(value)
			command = ""
		case 'c':
			command = value
		case 'n':
			if strings.HasPrefix(value, "/") {
				files = append(files, openFile{path: filepath.Clean(value), pid: pid, command: command})
			}
		}
	}
	return files
}

// parsePs reads lines like "  123 /Applications/Google Chrome.app/Contents/MacOS/Google Chrome".
func parsePs(out string) []process {
	var procs []process
	for _, line := range strings.Split(out, "\n") {
		pidText, comm, found := strings.Cut(strings.TrimSpace(line), " ")
		pid, err := strconv.Atoi(pidText)
		if !found || err != nil {
			continue
		}
		procs = append(procs, process{pid: pid, comm: strings.TrimSpace(comm)})
	}
	return procs
}

// Check reports whether a process holds path or something inside it.
// reason names the process, e.g. "open in node (pid 4242)".
func (o *Open) Check(path string) (busy bool, reason string) {
	path = filepath.Clean(path)
	// lsof prints real paths, so look up the real path when there is one.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	i := o.firstAtLeast(path)
	if i < len(o.files) && o.files[i].path == path {
		return true, openReason(o.files[i])
	}
	prefix := path
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	// Everything inside path starts with prefix, so it sits together in the
	// sorted slice, starting at the first path >= prefix.
	i = o.firstAtLeast(prefix)
	if i < len(o.files) && strings.HasPrefix(o.files[i].path, prefix) {
		return true, openReason(o.files[i])
	}
	return false, ""
}

func (o *Open) firstAtLeast(path string) int {
	return sort.Search(len(o.files), func(i int) bool { return o.files[i].path >= path })
}

func openReason(f openFile) string {
	return fmt.Sprintf("open in %s (pid %d)", f.command, f.pid)
}

// CheckConditions evaluates a rule's protect_if conditions for path:
// process_running matches a running process name (case-insensitive), and
// modified_within holds when anything under path changed after now minus
// the age.
func (o *Open) CheckConditions(path string, conds []rules.Condition, now time.Time) (busy bool, reason string) {
	for _, c := range conds {
		if c.ProcessRunning == "" && c.ModifiedWithin == "" {
			// A rule bug. Skipping the item is the safe answer.
			return true, "empty protect_if condition"
		}
		if c.ProcessRunning != "" && o.processRunning(c.ProcessRunning) {
			return true, c.ProcessRunning + " is running"
		}
		if c.ModifiedWithin == "" {
			continue
		}
		age, err := rules.ParseAge(c.ModifiedWithin)
		if err != nil {
			return true, err.Error()
		}
		changed, err := ChangedSince(path, now.Add(-age))
		if err != nil {
			return true, fmt.Sprintf("cannot check %s: %v", path, err)
		}
		if changed {
			return true, "changed within " + c.ModifiedWithin
		}
	}
	return false, ""
}

// processRunning matches the base name of a process's executable, or an
// executable inside "<name>.app", ignoring case.
func (o *Open) processRunning(name string) bool {
	name = strings.ToLower(name)
	for _, p := range o.procs {
		comm := strings.ToLower(p.comm)
		if filepath.Base(comm) == name || strings.Contains(comm, "/"+name+".app/") {
			return true
		}
	}
	return false
}

// ChangedSince reports whether path or anything under it has an mtime after
// t (not at t). It walks with Lstat, never follows symlinks, and stops at the
// first hit. A path that does not exist, and files that vanish while walking,
// are ignored; any other error (a folder it cannot read) is returned.
func ChangedSince(path string, t time.Time) (bool, error) {
	found := false
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.ModTime().After(t) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}
