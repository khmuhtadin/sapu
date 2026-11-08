package engine

// Regression tests for problems found by an independent review (Nov 2026).
// Each one failed before its fix.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

func readHomeFile(t *testing.T, e env.Env, rel string) string {
	t.Helper()
	data, err := os.ReadFile(homePath(e, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A folder item whose contents changed after the plan was made was moved
// anyway: a folder's identity covers only its own entry, and writing to a
// file deep inside does not change it.
func TestApplySkipsFolderWhoseContentsChanged(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Caches/edited/deep/data.db": "old",
		"Library/Caches/steady/deep/data.db": "old",
	})
	edited := item(t, e, "Library/Caches/edited", rules.RiskSafe)
	p := makePlan(t, e, edited, item(t, e, "Library/Caches/steady", rules.RiskSafe))
	if err := os.WriteFile(homePath(e, "Library/Caches/edited/deep/data.db"), []byte("new personal data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if now, err := plan.IdentityOf(edited.Path); err != nil || now != edited.Identity {
		t.Fatalf("the folder's own identity changed (%v), so this test would not show the problem", err)
	}

	res, err := Apply(t.Context(), e, p, DefaultOptions())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []plan.Skip{{Path: edited.Path, Source: "test.rule", Reason: plan.ReasonChanged, Detail: "something inside changed after the plan was made"}}
	if !slices.Equal(res.Skipped, want) {
		t.Errorf("skipped %+v, want %+v", res.Skipped, want)
	}
	if got := paths(res.Moved); !slices.Equal(got, []string{homePath(e, "Library/Caches/steady")}) {
		t.Errorf("moved %v, want only the steady folder", got)
	}
	if got := readHomeFile(t, e, "Library/Caches/edited/deep/data.db"); got != "new personal data" {
		t.Errorf("edited file holds %q", got)
	}
}

// A folder Apply cannot read all of is skipped as unreadable, not changed: it
// cannot tell whether something inside changed, but nothing is known to have.
func TestApplySkipsFolderItCannotRead(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/locked/inner/data.db": "data"})
	p := makePlan(t, e, item(t, e, "Library/Caches/locked", rules.RiskSafe))
	inner := homePath(e, "Library/Caches/locked/inner")
	if err := os.Chmod(inner, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(inner, 0o755) })

	res, err := Apply(t.Context(), e, p, DefaultOptions())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != plan.ReasonUnreadable || !strings.Contains(res.Skipped[0].Detail, "permission denied") {
		t.Errorf("skipped %+v, want the folder as unreadable with the read error", res.Skipped)
	}
	if len(res.Moved) != 0 {
		t.Errorf("moved %v, want nothing", paths(res.Moved))
	}
}

// Apply checked open files against one snapshot taken before the first move,
// so an app that opened an item later in the plan, after Apply started, was
// not seen. Not parallel: it sets snapshotMaxAge.
func TestApplySeesFilesOpenedWhileApplying(t *testing.T) {
	saved := snapshotMaxAge
	snapshotMaxAge = 0
	t.Cleanup(func() { snapshotMaxAge = saved })
	e := newHome(t, map[string]string{
		"Library/Caches/a/f": "a",
		"Library/Caches/b/f": "b",
	})
	p := makePlan(t, e,
		item(t, e, "Library/Caches/a", rules.RiskSafe),
		item(t, e, "Library/Caches/b", rules.RiskSafe),
	)
	var opened *os.File
	t.Cleanup(func() {
		if opened != nil {
			_ = opened.Close()
		}
	})
	opts := DefaultOptions()
	opts.Progress = func(done, total int, it plan.Item) {
		if done != 1 {
			return
		}
		f, err := os.Open(homePath(e, "Library/Caches/b/f")) // an app opens b while a is being moved
		if err != nil {
			t.Error(err)
			return
		}
		opened = f
	}

	res, err := Apply(t.Context(), e, p, opts)

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := ids(res.Moved); !slices.Equal(got, []string{"0001"}) {
		t.Errorf("moved %v, want only 0001", got)
	}
	pid := fmt.Sprintf("(pid %d)", os.Getpid())
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != plan.ReasonInUse || !strings.Contains(res.Skipped[0].Detail, pid) {
		t.Errorf("skipped %+v, want b as in use by %s", res.Skipped, pid)
	}
	requireExists(t, homePath(e, "Library/Caches/b/f"))
}

// With Permanent, a run with failures is not emptied: a failed move may have
// left something in quarantine (fsop.ErrStranded) that only undo brings back.
func TestApplyPermanentKeepsRunWithFailures(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Caches/a/f": "a",
		"Library/Caches/b/f": "b",
	})
	p := makePlan(t, e,
		item(t, e, "Library/Caches/a", rules.RiskSafe),
		item(t, e, "Library/Caches/b", rules.RiskSafe),
	)
	p.Items[1].Action = "trash"
	opts := DefaultOptions()
	opts.Permanent = true

	res, err := Apply(t.Context(), e, p, opts)

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Failed) != 1 || len(res.Moved) != 1 || res.Freed != 0 {
		t.Errorf("failed %+v, moved %v, freed %d; want one failure, one move, nothing freed", res.Failed, ids(res.Moved), res.Freed)
	}
	if s := runStatus(t, e, res.RunID); s != runlog.StatusDone {
		t.Errorf("run status %q, want %q (not emptied)", s, runlog.StatusDone)
	}
	if _, err := fsop.Restore(e, res.RunID); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if got := readHomeFile(t, e, "Library/Caches/a/f"); got != "a" {
		t.Errorf("after undo a/f holds %q", got)
	}
}

// lockHomeVar passes the test home to TestHelperHoldStateLock.
const lockHomeVar = "SAPU_ENGINE_LOCK_TEST_HOME"

// TestHelperHoldStateLock is not a test on its own: it holds the state
// folder's lock in a child process, as another sapu would, until its stdin is
// closed.
func TestHelperHoldStateLock(t *testing.T) {
	home := os.Getenv(lockHomeVar)
	if home == "" {
		t.Skip("helper process for TestApplyWaitsForOtherProcess")
	}
	unlock, err := fsop.Lock(env.New(home))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := os.Stdout.WriteString("locked\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

// Two sapu processes could work on one state folder at once, so one could
// empty a run the other was still applying. Apply now takes the state
// folder's lock and fails before it moves anything while another process
// holds it. Plan takes no lock and still works.
func TestApplyWaitsForOtherProcess(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/junk/a.db": "junk"})
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldStateLock$")
	cmd.Env = append(os.Environ(), lockHomeVar+"="+e.Home)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	out := bufio.NewReader(stdout)
	if line, err := out.ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("child did not take the lock: %q, %v\n%s", line, err, stderr.String())
	}

	p := makePlan(t, e, item(t, e, "Library/Caches/junk", rules.RiskSafe))
	res, err := Apply(t.Context(), e, p, DefaultOptions())

	if !errors.Is(err, fsop.ErrLocked) || res != nil {
		t.Fatalf("Apply = %+v, %v; want nil and fsop.ErrLocked", res, err)
	}
	requireExists(t, homePath(e, "Library/Caches/junk/a.db"))
	if runs, err := runlog.List(e); err != nil || len(runs) != 0 {
		t.Fatalf("runs %+v (%v), want none", runs, err)
	}

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v\n%s%s", err, rest, stderr.String())
	}
	res, err = Apply(t.Context(), e, p, DefaultOptions())
	if err != nil || len(res.Moved) != 1 {
		t.Fatalf("Apply after the other process finished: %+v, %v", res, err)
	}
}
