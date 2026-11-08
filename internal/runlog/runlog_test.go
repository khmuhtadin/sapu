package runlog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
)

var now = time.Date(2026, 11, 8, 11, 15, 0, 0, time.Local)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func start(t *testing.T, e env.Env, kind string, at time.Time) *Run {
	t.Helper()
	p := &plan.Plan{SchemaVersion: plan.SchemaVersion, Kind: kind, CreatedAt: at, Home: e.Home}
	r, err := Start(e, p, at)
	must(t, err)
	return r
}

func types(evs []Event) string {
	var s []string
	for _, ev := range evs {
		s = append(s, ev.Type)
	}
	return strings.Join(s, ",")
}

func appendRaw(t *testing.T, e env.Env, id, raw string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(e.RunsDir(), id, journalFile), os.O_WRONLY|os.O_APPEND, 0)
	must(t, err)
	_, err = f.WriteString(raw)
	must(t, err)
	must(t, f.Close())
}

func TestStartWritesRunFolder(t *testing.T) {
	e := env.New(t.TempDir())
	r := start(t, e, plan.KindPurge, now)
	if !regexp.MustCompile(`^20261108-111500-[0-9a-f]{4}$`).MatchString(r.ID) {
		t.Fatalf("run id %q", r.ID)
	}
	if r.Kind != plan.KindPurge || r.Dir != filepath.Join(e.RunsDir(), r.ID) {
		t.Fatalf("run = %+v", r)
	}
	must(t, r.Log(Event{Time: now, Type: EventMoveBegin, Item: "0001", From: "/a", To: "/q/0001-a", Bytes: 5}))
	must(t, r.Close(now, true))

	for name, want := range map[string]os.FileMode{"": 0o700, planFile: 0o600, journalFile: 0o600} {
		fi, err := os.Stat(filepath.Join(r.Dir, name))
		must(t, err)
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", name, fi.Mode().Perm(), want)
		}
	}
	f, err := os.Open(filepath.Join(r.Dir, planFile))
	must(t, err)
	defer func() { _ = f.Close() }()
	p, err := plan.Read(f)
	must(t, err)
	if p.Kind != plan.KindPurge {
		t.Fatalf("plan kind = %q", p.Kind)
	}

	evs, err := Events(e, r.ID)
	must(t, err)
	if got := types(evs); got != "start,move_begin,end" {
		t.Fatalf("events = %s", got)
	}
	if evs[1].Bytes != 5 || evs[1].To != "/q/0001-a" || !evs[0].Time.Equal(now) {
		t.Fatalf("events = %+v", evs)
	}
	if err := r.Log(Event{Type: EventSkip}); err == nil {
		t.Fatal("Log after Close succeeded")
	}
}

func TestTornLastLineIsIgnored(t *testing.T) {
	e := env.New(t.TempDir())
	r := start(t, e, plan.KindClean, now)
	must(t, r.Log(Event{Time: now, Type: EventMoveBegin, Item: "0001", To: "/q/a"}))
	must(t, r.Close(now, false))
	appendRaw(t, e, r.ID, `{"time":"2026-11-08T11:15:00Z","type":"move_do`)

	evs, err := Events(e, r.ID)
	must(t, err)
	if got := types(evs); got != "start,move_begin" {
		t.Fatalf("events = %s", got)
	}

	// Appending after a torn line keeps the journal readable.
	r, err = Open(e, r.ID)
	must(t, err)
	must(t, r.Log(Event{Time: now, Type: EventRestoreBegin, Item: "0001"}))
	must(t, r.Close(now, false))
	evs, err = Events(e, r.ID)
	must(t, err)
	if got := types(evs); got != "start,move_begin,restore_begin" {
		t.Fatalf("events after reopen = %s", got)
	}
	// A second torn line is handled the same way.
	appendRaw(t, e, r.ID, `{"ti`)
	r, err = Open(e, r.ID)
	must(t, err)
	must(t, r.Log(Event{Time: now, Type: EventRestoreDone, Item: "0001"}))
	must(t, r.Close(now, false))
	evs, err = Events(e, r.ID)
	must(t, err)
	if got := types(evs); got != "start,move_begin,restore_begin,restore_done" {
		t.Fatalf("events after second reopen = %s", got)
	}
}

func TestCorruptMiddleLineIsAnError(t *testing.T) {
	e := env.New(t.TempDir())
	r := start(t, e, plan.KindClean, now)
	must(t, r.Close(now, false))
	appendRaw(t, e, r.ID, "not json\n")
	r, err := Open(e, r.ID)
	must(t, err)
	must(t, r.Close(now, true))

	if _, err := Events(e, r.ID); err == nil {
		t.Fatal("Events accepted a corrupt line in the middle")
	}
	if _, err := List(e); err == nil {
		t.Fatal("List accepted a corrupt journal")
	}
}

func TestStatuses(t *testing.T) {
	e := env.New(t.TempDir())
	ev := func(typ, to string, bytes int64) Event {
		return Event{Time: now, Type: typ, Item: "0001", To: to, From: "/home/a", Bytes: bytes}
	}
	restore := func(from string) Event {
		return Event{Time: now, Type: EventRestoreDone, Item: "0001", From: from, To: "/home/a"}
	}
	cases := []struct {
		name   string
		events []Event
		end    bool
		status string
		items  int
		bytes  int64
	}{
		{"done", []Event{ev(EventMoveBegin, "/q/1", 10), ev(EventMoveDone, "/q/1", 10)}, true, StatusDone, 1, 10},
		{"interrupted", []Event{ev(EventMoveBegin, "/q/1", 10), ev(EventMoveDone, "/q/1", 10), ev(EventMoveBegin, "/q/2", 20)}, false, StatusInterrupted, 1, 10},
		{"failed move", []Event{ev(EventMoveBegin, "/q/1", 10), ev(EventMoveFailed, "/q/1", 0)}, true, StatusDone, 0, 0},
		{"undone", []Event{ev(EventMoveBegin, "/q/1", 10), ev(EventMoveDone, "/q/1", 10), ev(EventMoveBegin, "/q/2", 20), ev(EventMoveDone, "/q/2", 20), restore("/q/2"), restore("/q/1")}, true, StatusUndone, 2, 30},
		{"undone interrupted", []Event{ev(EventMoveBegin, "/q/1", 10), restore("/q/1")}, false, StatusUndone, 0, 0},
		{"partly undone", []Event{ev(EventMoveBegin, "/q/1", 10), ev(EventMoveDone, "/q/1", 10), ev(EventMoveBegin, "/q/2", 20), ev(EventMoveDone, "/q/2", 20), restore("/q/2")}, true, StatusDone, 2, 30},
		{"emptied", []Event{ev(EventMoveBegin, "/q/1", 10), ev(EventMoveDone, "/q/1", 10), {Time: now, Type: EventEmpty, Bytes: 10}}, true, StatusEmptied, 1, 10},
		{"no moves", nil, true, StatusDone, 0, 0},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := start(t, e, plan.KindClean, now.Add(time.Duration(i)*time.Minute))
			for _, ev := range c.events {
				must(t, r.Log(ev))
			}
			must(t, r.Close(now, c.end))
			runs, err := List(e)
			must(t, err)
			for _, s := range runs {
				if s.ID != r.ID {
					continue
				}
				if s.Status != c.status || s.Items != c.items || s.Bytes != c.bytes || s.Kind != plan.KindClean {
					t.Fatalf("summary = %+v, want status %s, %d items, %d bytes", s, c.status, c.items, c.bytes)
				}
				return
			}
			t.Fatalf("run %s not listed", r.ID)
		})
	}
}

func TestListNewestFirst(t *testing.T) {
	e := env.New(t.TempDir())
	if runs, err := List(e); err != nil || len(runs) != 0 {
		t.Fatalf("List without runs = %v, %v", runs, err)
	}
	var ids []string
	for _, at := range []time.Time{now.Add(-48 * time.Hour), now, now.Add(-time.Hour)} {
		r := start(t, e, plan.KindClean, at)
		must(t, r.Close(at, true))
		ids = append(ids, r.ID)
	}
	must(t, os.MkdirAll(filepath.Join(e.RunsDir(), "not-a-run"), 0o700))
	runs, err := List(e)
	must(t, err)
	if len(runs) != 3 || runs[0].ID != ids[1] || runs[1].ID != ids[2] || runs[2].ID != ids[0] {
		t.Fatalf("order = %+v", runs)
	}
	if !runs[0].Started.Equal(now) {
		t.Fatalf("started = %v, want %v", runs[0].Started, now)
	}
}

// A crash inside Start can leave a run folder without plan.json or journal.
func TestListRunCutShortInStart(t *testing.T) {
	e := env.New(t.TempDir())
	must(t, os.MkdirAll(filepath.Join(e.RunsDir(), "20261108-111500-abcd"), 0o700))
	runs, err := List(e)
	must(t, err)
	if len(runs) != 1 || runs[0].Status != StatusInterrupted || runs[0].Started.IsZero() {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestBadRunIDs(t *testing.T) {
	e := env.New(t.TempDir())
	for _, id := range []string{"", ".", "..", "../runs", "20261108-111500", "20261108-111500-ABCD", "20261108-111500-abcd/x"} {
		if _, err := Open(e, id); err == nil {
			t.Errorf("Open(%q) succeeded", id)
		}
		if _, err := Events(e, id); err == nil {
			t.Errorf("Events(%q) succeeded", id)
		}
	}
	if _, err := Open(e, "20261108-111500-abcd"); err == nil {
		t.Error("Open of a missing run succeeded")
	}
}
