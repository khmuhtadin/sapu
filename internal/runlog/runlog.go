// Package runlog records every run in ~/.sapu/runs/<run_id>/:
//
//	plan.json      the plan that was applied
//	journal.jsonl  one Event per line, appended and fsynced BEFORE the action
//	               it describes, so a crash always leaves a readable record
//
// A run without an "end" event was interrupted. A torn last line (crash in
// the middle of a write) is ignored when reading.
//
// A duplicate replaced by an APFS clone (fsop.QuarantineAndClone) is
// journaled as a move, then clone_begin (with the clone's identity, before
// the clone takes the item's place) and clone_done, or clone_failed. Undo
// journals unclone_begin before it moves the clone aside to delete it, and
// unclone_done after, or unclone_failed when what it moved aside was not the
// clone and went back.
//
// Official commands (engine.Options.RunCommands) are journaled too:
// command_begin before the program starts and command after it ends, with
// the item, the command and the exit code. They are not undoable; undo only
// restores what was moved into quarantine.
//
// Implemented in Wave 1 (W1-A).
package runlog

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/safefile"
)

// Event types.
const (
	EventStart         = "start"
	EventMoveBegin     = "move_begin"
	EventMoveDone      = "move_done"
	EventMoveFailed    = "move_failed"
	EventSkip          = "skip"
	EventRestoreBegin  = "restore_begin"
	EventRestoreDone   = "restore_done"
	EventEmpty         = "empty"          // quarantine of this run deleted for good
	EventCommandBegin  = "command_begin"  // an official command is about to start
	EventCommand       = "command"        // it ended (ExitCode), or was refused (Reason)
	EventCloneBegin    = "clone_begin"    // a clone of the kept copy is about to take the moved item's place
	EventCloneDone     = "clone_done"     // it did
	EventCloneFailed   = "clone_failed"   // it did not; the item stays in quarantine (Reason)
	EventUncloneBegin  = "unclone_begin"  // undo: the clone is about to be moved aside and deleted
	EventUncloneDone   = "unclone_done"   // undo deleted it
	EventUncloneFailed = "unclone_failed" // what was moved aside was not the clone, or could not be; it stays (Reason)
	EventEnd           = "end"
)

// Run statuses, derived from the events.
const (
	StatusDone        = "done"
	StatusInterrupted = "interrupted"
	StatusUndone      = "undone"
	StatusEmptied     = "emptied"
)

// Event is one journal line. For move_* events From is the original path and
// To the path inside quarantine; for restore_* events From is the path inside
// quarantine and To where the item went back. The quarantine path ties the
// events of one item together. Bytes is set on move_begin, move_done and empty.
// For command_* events From is the item's path, Command the program and its
// arguments, and ExitCode (command only) the program's exit code, or -1 when
// it did not exit by itself.
//
// For clone_* events From is the kept copy and To the moved item's original
// path, where the clone goes; Identity (clone_begin and clone_done) is the
// identity of the clone. For unclone_* events From is that path and To the
// name inside quarantine the clone is moved to before it is deleted.
type Event struct {
	Time     time.Time      `json:"time"`
	Type     string         `json:"type"`
	Item     string         `json:"item,omitempty"` // plan item id
	From     string         `json:"from,omitempty"`
	To       string         `json:"to,omitempty"`
	Bytes    int64          `json:"bytes,omitempty"`
	Reason   string         `json:"reason,omitempty"`
	Command  []string       `json:"command,omitempty"`
	ExitCode *int           `json:"exit_code,omitempty"`
	Identity *plan.Identity `json:"identity,omitempty"`
}

// Run is an open journal.
type Run struct {
	ID   string // sortable by time: "20261108-1115-a3f9"
	Kind string // plan kind
	Dir  string // ~/.sapu/runs/<ID>

	mu     sync.Mutex
	f      *os.File
	broken error // set after a failed write; the journal then takes no more events
}

const (
	planFile    = "plan.json"
	journalFile = "journal.jsonl"
	idLayout    = "20060102-150405"
)

var idPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{4}$`)

// validID reports whether id has the form of a run id. Run ids become folder
// names, so anything else (for example "..") is refused.
func validID(id string) bool {
	return idPattern.MatchString(id)
}

// Start creates a new run folder, writes plan.json and the "start" event.
func Start(e env.Env, p *plan.Plan, now time.Time) (*Run, error) {
	var rnd [2]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, err
	}
	id := now.Format(idLayout) + "-" + hex.EncodeToString(rnd[:])
	if err := os.MkdirAll(e.RunsDir(), 0o700); err != nil {
		return nil, err
	}
	dir := filepath.Join(e.RunsDir(), id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	if err := writePlan(e, filepath.Join(dir, planFile), p); err != nil {
		return nil, err
	}
	f, err := safefile.Open(e.StateDir, filepath.Join(dir, journalFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	r := &Run{ID: id, Kind: p.Kind, Dir: dir, f: f}
	if err := r.Log(Event{Time: now, Type: EventStart}); err != nil {
		_ = f.Close()
		return nil, err
	}
	return r, nil
}

func writePlan(e env.Env, path string, p *plan.Plan) error {
	f, err := safefile.Open(e.StateDir, path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := plan.Write(f, p); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Open reopens an existing run to append events (used by undo and empty).
//
// If the journal ends in a torn line, Open ends that line and adds an empty
// line after it. Events reads a line followed by an empty line as torn, so the
// journal stays readable and nothing is ever cut off it.
//
// The journal is opened through safefile: a journal that is a symlink, has
// other names (hard links), is not a regular file, belongs to another user
// or lies outside the state folder is refused before anything is appended,
// the torn-line repair included.
func Open(e env.Env, id string) (*Run, error) {
	if !validID(id) {
		return nil, fmt.Errorf("invalid run id %q", id)
	}
	dir := filepath.Join(e.RunsDir(), id)
	kind, err := readKind(dir)
	if err != nil {
		return nil, err
	}
	f, err := safefile.Open(e.StateDir, filepath.Join(dir, journalFile), os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return nil, err
	}
	torn, err := endsTorn(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	r := &Run{ID: id, Kind: kind, Dir: dir, f: f}
	if torn {
		if err := r.write([]byte("\n\n")); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return r, nil
}

// endsTorn reports whether the file is not empty and does not end in a newline.
func endsTorn(f *os.File) (bool, error) {
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	if fi.Size() == 0 {
		return false, nil
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, fi.Size()-1); err != nil {
		return false, err
	}
	return last[0] != '\n', nil
}

// Log appends one event and fsyncs the journal.
func (r *Run) Log(ev Event) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return r.write(append(line, '\n'))
}

// write appends b in one write and fsyncs. After any failure the journal may
// end in a torn line, so the run refuses further events.
func (r *Run) write(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.broken != nil {
		return r.broken
	}
	if r.f == nil {
		return fmt.Errorf("run %s: journal is closed", r.ID)
	}
	if _, err := r.f.Write(b); err != nil {
		r.broken = fmt.Errorf("run %s: journal write failed: %w", r.ID, err)
		return r.broken
	}
	if err := r.f.Sync(); err != nil {
		r.broken = fmt.Errorf("run %s: journal sync failed: %w", r.ID, err)
		return r.broken
	}
	return nil
}

// Close writes the "end" event (when end is true) and closes the journal.
func (r *Run) Close(now time.Time, end bool) error {
	var logErr error
	if end {
		logErr = r.Log(Event{Time: now, Type: EventEnd})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return logErr
	}
	closeErr := r.f.Close()
	r.f = nil
	return errors.Join(logErr, closeErr)
}

type Summary struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Status  string    `json:"status"`
	Started time.Time `json:"started"`
	Items   int       `json:"items"` // items moved into quarantine
	Bytes   int64     `json:"bytes"` // bytes moved into quarantine
}

// List returns all runs, newest first.
//
// A run folder left by a crash inside Start may lack plan.json or the
// journal; it is listed as interrupted with what is there.
func List(e env.Env) ([]Summary, error) {
	entries, err := os.ReadDir(e.RunsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, de := range entries {
		if !de.IsDir() || !validID(de.Name()) {
			continue
		}
		s, err := summarize(e, de.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Started.Equal(out[j].Started) {
			return out[i].Started.After(out[j].Started)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func summarize(e env.Env, id string) (Summary, error) {
	dir := filepath.Join(e.RunsDir(), id)
	kind, err := readKind(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Summary{}, err
	}
	evs, err := Events(e, id)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Summary{}, err
	}
	s := Summary{ID: id, Kind: kind}
	started, err := time.ParseInLocation(idLayout, id[:len(idLayout)], time.Local)
	if err != nil {
		return Summary{}, err
	}
	s.Started = started

	begun := map[string]int64{} // quarantine path -> bytes
	moved := map[string]bool{}  // quarantine path -> move_done seen
	restored := map[string]bool{}
	var hasEnd, hasEmpty bool
	for _, ev := range evs {
		switch ev.Type {
		case EventStart:
			s.Started = ev.Time
		case EventMoveBegin:
			begun[ev.To] = ev.Bytes
		case EventMoveDone:
			if !moved[ev.To] {
				moved[ev.To] = true
				s.Items++
				s.Bytes += begun[ev.To]
			}
		case EventRestoreDone:
			restored[ev.From] = true
		case EventEnd:
			hasEnd = true
		case EventEmpty:
			hasEmpty = true
		}
	}
	allRestored := len(restored) > 0
	for to := range moved {
		if !restored[to] {
			allRestored = false
		}
	}
	switch {
	case hasEmpty:
		s.Status = StatusEmptied
	case allRestored:
		s.Status = StatusUndone
	case hasEnd:
		s.Status = StatusDone
	default:
		s.Status = StatusInterrupted
	}
	return s, nil
}

func readKind(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, planFile))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }() // read only: a failed close loses nothing
	p, err := plan.Read(f)
	if err != nil {
		return "", fmt.Errorf("%s: %w", dir, err)
	}
	return p.Kind, nil
}

// Events returns the journal of one run.
//
// The last line is ignored when it has no newline (a write torn by a crash),
// and so is a line followed by an empty line (a torn line ended by Open). Any
// other line that does not parse is an error.
func Events(e env.Env, id string) ([]Event, error) {
	if !validID(id) {
		return nil, fmt.Errorf("invalid run id %q", id)
	}
	f, err := os.Open(filepath.Join(e.RunsDir(), id, journalFile))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read only: a failed close loses nothing
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(data, []byte("\n"))
	lines = lines[:len(lines)-1] // empty after the final newline, or a torn line
	var evs []Event
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		if i+1 < len(lines) && len(lines[i+1]) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("run %s: journal line %d: %w", id, i+1, err)
		}
		evs = append(evs, ev)
	}
	return evs, nil
}
