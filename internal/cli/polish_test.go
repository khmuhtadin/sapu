package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

// errorOf reads the error envelope of a failed --json run.
func errorOf(t *testing.T, out string) (command, code, message string) {
	t.Helper()
	var doc struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if doc.OK {
		t.Fatalf("envelope says ok:\n%s", out)
	}
	return doc.Command, doc.Error.Code, doc.Error.Message
}

// lockHomeVar passes the test home to TestHelperHoldingLock.
const lockHomeVar = "SAPU_CLI_LOCK_TEST_HOME"

// TestHelperHoldingLock is not a test on its own: the locked test runs it in a
// child process as another sapu that is working on the state folder. It holds
// the lock until its stdin is closed.
func TestHelperHoldingLock(t *testing.T) {
	home := os.Getenv(lockHomeVar)
	if home == "" {
		t.Skip("helper process for TestLockedExplainsAndUsesCodeLocked")
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

func TestLockedExplainsAndUsesCodeLocked(t *testing.T) {
	e := newHome(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldingLock$")
	cmd.Env = append(os.Environ(), lockHomeVar+"="+e.Home)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var childErr bytes.Buffer
	cmd.Stderr = &childErr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("child did not take the lock: %q, %v\n%s", line, err, childErr.String())
	}
	before := snapshot(t, e)

	_, errOut, code := sapu(t, e, "", "clean", "--yes")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if strings.TrimSpace(errOut) != "sapu: "+lockedText {
		t.Errorf("stderr = %q", errOut)
	}

	out, _, code := sapu(t, e, "", "clean", "--yes", "--json")
	if code != 1 {
		t.Errorf("--json: exit %d, want 1", code)
	}
	validate(t, "clean", out)
	command, errCode, message := errorOf(t, out)
	if command != "clean" || errCode != "locked" || message != lockedText {
		t.Errorf("envelope: command %q, code %q, message %q", command, errCode, message)
	}
	sameSnapshot(t, before, snapshot(t, e))
	assertDecoys(t, e)

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v\n%s", err, childErr.String())
	}
	if _, _, code := sapu(t, e, "", "clean", "--yes"); code != 0 {
		t.Errorf("after the other sapu finished: exit %d", code)
	}
}

// A run with an item on a volume that is not mounted: emptying it must say
// which volume to connect, and say that undo still works.
func TestEmptyNamesTheUnavailableVolume(t *testing.T) {
	e := env.New(t.TempDir())
	mnt := filepath.Join(t.TempDir(), "unplugged")
	p := &plan.Plan{SchemaVersion: plan.SchemaVersion, Kind: plan.KindClean, CreatedAt: time.Now(), Home: e.Home}
	run, err := runlog.Start(e, p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(mnt, ".sapu-quarantine", strconv.Itoa(os.Getuid()), run.ID, "0001-photos")
	writeFile(t, to, "only copy")
	begin := runlog.Event{Time: time.Now(), Type: runlog.EventMoveBegin, Item: "0001", From: filepath.Join(e.Home, "photos"), To: to, Bytes: 9}
	done := runlog.Event{Time: time.Now(), Type: runlog.EventMoveDone, Item: "0001", From: begin.From, To: to}
	for _, ev := range []runlog.Event{begin, done} {
		if err := run.Log(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.Close(time.Now(), true); err != nil {
		t.Fatal(err)
	}

	_, errOut, code := sapu(t, e, "", "quarantine", "empty", run.ID, "--yes")
	want := "The volume " + mnt + " that holds quarantined items is not connected. " +
		"Connect it and try again; its items can still be restored with sapu undo."
	if code != 1 || strings.TrimSpace(errOut) != "sapu: "+want {
		t.Errorf("exit %d, stderr %q\nwant      %q", code, errOut, want)
	}

	out, _, code := sapu(t, e, "", "quarantine", "empty", run.ID, "--yes", "--json")
	if code != 1 {
		t.Errorf("--json: exit %d, want 1", code)
	}
	validate(t, "quarantine-empty", out)
	command, errCode, message := errorOf(t, out)
	if command != "quarantine-empty" || errCode != "unavailable" || message != want {
		t.Errorf("envelope: command %q, code %q, message %q", command, errCode, message)
	}
	if got, _ := os.ReadFile(to); string(got) != "only copy" {
		t.Errorf("the item on the volume was touched: %q", got)
	}
}

func TestFriendlyErrWithoutVolumeName(t *testing.T) {
	text, code := friendlyErr(fmt.Errorf("run x: %w", fsop.ErrUnavailable))
	if code != "unavailable" || !strings.HasPrefix(text, "A volume that holds quarantined items") {
		t.Errorf("got %q, %q", text, code)
	}
	if text, code := friendlyErr(errors.New("plain")); text != "plain" || code != "" {
		t.Errorf("a plain error changed: %q, %q", text, code)
	}
}

func TestStrandedFailureTellsHowToUndo(t *testing.T) {
	home := t.TempDir()
	stranded := fmt.Errorf("%w: %s/cache is now %s/q/0001-cache in run 20261108-1115-a3f9 (putting it back failed: x)",
		fsop.ErrStranded, home, home)
	res := &engine.Result{
		RunID: "20261108-1115-a3f9",
		Failed: []engine.Failure{
			{Item: plan.Item{Path: home + "/cache"}, Error: stranded.Error()},
			{Item: plan.Item{Path: home + "/other"}, Error: "unknown action \"burn\""},
		},
	}
	var buf bytes.Buffer
	writeResult(&buf, home, res, config.Defaults(), time.Now(), false, false)
	out := buf.String()
	for _, want := range []string{
		"~/cache (something replaced this item while it moved. It is in quarantine; `sapu undo 20261108-1115-a3f9` puts it back)",
		"~/other (unknown action \"burn\")",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "RENAME") || strings.Contains(out, "putting it back failed") {
		t.Errorf("the raw error leaked into the text:\n%s", out)
	}
}

func TestCleanPlanFileReplacesARegularFile(t *testing.T) {
	e := newHome(t)
	file := filepath.Join(t.TempDir(), "plan.json")
	writeFile(t, file, strings.Repeat("old plan, longer than the new one ", 5000))
	out, errOut, code := sapu(t, e, "", "clean", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if got := selectedPaths(readPlanFile(t, file)); len(got) != len(junkItems) {
		t.Errorf("the file does not hold the new plan: %v", got)
	}
}

func TestCleanPlanFileRefusesFolderAndSymlink(t *testing.T) {
	e := newHome(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	writeFile(t, target, "precious")
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, "folder")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ file, want string }{
		{folder, "is a folder; give --plan a file name"},
		{link, "is a symlink; Sapu will not write through it"},
	} {
		_, errOut, code := sapu(t, e, "", "clean", "--plan", c.file)
		if code != 1 || !strings.Contains(errOut, c.file+" "+c.want) {
			t.Errorf("--plan %s: exit %d, stderr %q", c.file, code, errOut)
		}
	}
	if got, _ := os.ReadFile(target); string(got) != "precious" {
		t.Errorf("the symlink target was written: %q", got)
	}
	noState(t, e)
}

// Every command's one-line description must fit in 80 columns in the help of
// the command that lists it, indentation included.
func TestHelpDescriptionsFit80Columns(t *testing.T) {
	e := env.New(t.TempDir())
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if len(c.Commands()) == 0 {
			return
		}
		args := append(strings.Fields(strings.TrimPrefix(c.CommandPath(), "sapu")), "--help")
		out, _, code := sapu(t, e, "", args...)
		if code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			found := false
			for _, line := range strings.Split(out, "\n") {
				if !strings.HasSuffix(line, sub.Short) {
					continue
				}
				found = true
				if n := utf8.RuneCountInString(line); n > 80 {
					t.Errorf("%v: line is %d columns: %q", args, n, line)
				}
			}
			if !found {
				t.Errorf("%v: no line ends with the description of %s", args, sub.Name())
			}
			walk(sub)
		}
	}
	walk(newRootCmd())
}

// A usage error of a subcommand names the command the way its success does.
func TestErrorEnvelopeNamesSubcommandWithHyphen(t *testing.T) {
	out, _, code := sapu(t, env.New(t.TempDir()), "", "quarantine", "list", "--json", "--bogus")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if command, errCode, _ := errorOf(t, out); command != "quarantine-list" || errCode != "usage" {
		t.Errorf("command %q, code %q", command, errCode)
	}
}
