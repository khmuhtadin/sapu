package fsop

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

// killHomeVar passes the test home to the helper process.
const killHomeVar = "SAPU_FSOP_KILL_TEST_HOME"

// TestHelperQuarantineUntilKilled is not a test on its own: TestKill9 runs
// it in a child process and kills it with SIGKILL in the middle of the run.
func TestHelperQuarantineUntilKilled(t *testing.T) {
	home := os.Getenv(killHomeVar)
	if home == "" {
		t.Skip("helper process for TestKill9DuringQuarantineIsUndone")
	}
	e := env.New(home)
	items := itemsFor(t, filepath.Join(home, "data"))
	run := startRun(t, e, items, time.Now())
	for _, it := range items {
		must(t, Quarantine(e, run, it))
	}
	must(t, run.Close(time.Now(), true))
}

// Invariant 4 with a real crash: SIGKILL in the middle of a 2,000 item run.
func TestKill9DuringQuarantineIsUndone(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: quarantines hundreds of items in a child process")
	}
	e := newHome(t)
	data := filepath.Join(e.Home, "data")
	for i := 0; i < 2000; i++ {
		p := filepath.Join(data, fmt.Sprintf("item%04d", i))
		if i%2 == 0 {
			writeFile(t, p, fmt.Sprintf("file %d", i))
			continue
		}
		writeFile(t, filepath.Join(p, "a.txt"), fmt.Sprintf("a %d", i))
		writeFile(t, filepath.Join(p, "sub", "b.txt"), fmt.Sprintf("b %d", i))
	}
	before := snapshot(t, data)

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperQuarantineUntilKilled$", "-test.v")
	cmd.Env = append(os.Environ(), killHomeVar+"="+e.Home)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	must(t, cmd.Start())

	// Kill once a few hundred items have moved.
	deadline := time.Now().Add(2 * time.Minute)
	for moved := 0; moved < 300; moved = countMoves(t, e) {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("child moved only %d items in time; output:\n%s", moved, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	must(t, cmd.Process.Kill())
	if err := cmd.Wait(); err == nil {
		t.Fatalf("child finished before it was killed; output:\n%s", out.String())
	}

	runs, err := runlog.List(e)
	must(t, err)
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	r := runs[0]
	if r.Status != runlog.StatusInterrupted || r.Items < 300 || r.Items >= 2000 {
		t.Fatalf("summary after kill = %+v, want interrupted with 300..1999 items", r)
	}
	t.Logf("killed after %d of 2000 items", r.Items)

	_, err = Restore(e, r.ID)
	must(t, err)
	requireSame(t, before, snapshot(t, data))
	if got := summary(t, e, r.ID).Status; got != runlog.StatusUndone {
		t.Fatalf("status after undo = %q, want undone", got)
	}
}

// countMoves counts move_done lines in the only run's journal.
func countMoves(t *testing.T, e env.Env) int {
	t.Helper()
	runs, err := os.ReadDir(e.RunsDir())
	if os.IsNotExist(err) || len(runs) == 0 {
		return 0
	}
	must(t, err)
	data, err := os.ReadFile(filepath.Join(e.RunsDir(), runs[0].Name(), "journal.jsonl"))
	if os.IsNotExist(err) {
		return 0
	}
	must(t, err)
	return bytes.Count(data, []byte(`"type":"move_done"`))
}
