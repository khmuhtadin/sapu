package inuse

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/rules"
)

func snapshot(t *testing.T) *Open {
	t.Helper()
	o, err := Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestOpenFileMakesFileAndParentBusy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "data.db")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	other := t.TempDir()

	o := snapshot(t)
	for _, path := range []string{file, dir} {
		busy, reason := o.Check(path)
		if !busy {
			t.Errorf("Check(%s) not busy", path)
		}
		if !strings.HasPrefix(reason, "open in ") || !strings.Contains(reason, "(pid "+strconv.Itoa(os.Getpid())+")") {
			t.Errorf("Check(%s) reason = %q", path, reason)
		}
	}
	if busy, reason := o.Check(other); busy {
		t.Errorf("unrelated folder is busy: %s", reason)
	}
	if busy, reason := o.Check(filepath.Join(dir, "other.db")); busy {
		t.Errorf("sibling file is busy: %s", reason)
	}
}

func TestCheckResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data.db")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if busy, _ := snapshot(t).Check(link); !busy {
		t.Error("a symlink to a busy folder is not busy")
	}
}

// Mole #1870: a process whose working directory is inside the folder.
func TestChildWorkingDirectoryMakesFolderBusy(t *testing.T) {
	home := env.New(t.TempDir()).Home
	npx := filepath.Join(home, ".npm", "_npx")
	cwd := filepath.Join(npx, "abc")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(npx, "def"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	cmd.Dir = cwd
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	o := snapshot(t)
	for _, path := range []string{cwd, npx} {
		busy, reason := o.Check(path)
		want := "open in sleep (pid " + strconv.Itoa(cmd.Process.Pid) + ")"
		if !busy || reason != want {
			t.Errorf("Check(%s) = %v, %q; want %q", path, busy, reason, want)
		}
	}
	if busy, reason := o.Check(filepath.Join(npx, "def")); busy {
		t.Errorf("folder next to the working directory is busy: %s", reason)
	}
}

func TestCheckUsesPathBoundaries(t *testing.T) {
	files := parseLsof("p10\ncnode\nfcwd\nn/data/abc/x\nftxt\nn/data/abcd\nf3\nnTCP *:80\n")
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	o := &Open{files: files}
	for path, want := range map[string]bool{
		"/data/abc":     true,
		"/data":         true,
		"/":             true,
		"/data/abc/x":   true,
		"/data/abcd":    true,
		"/data/ab":      false, // a prefix of the name, not a parent folder
		"/data/abc/xy":  false,
		"/data/abc/x/y": false, // inside the file, not a parent of it
		"/other":        false,
	} {
		if busy, _ := o.Check(path); busy != want {
			t.Errorf("Check(%s) = %v, want %v", path, busy, want)
		}
	}
}

func TestParseLsof(t *testing.T) {
	out := "p1\ncmds\nfcwd\nn/\nftxt\nn/usr/sbin/mds\nf4\nn*:5000\nf5\nn/var/run/x.sock\n" +
		"p42\ncGoogle Chrome\nf12\nn/home/x/Library/Caches/a b\nf13\nnpipe\n\n"
	got := parseLsof(out)
	want := []openFile{
		{"/", 1, "mds"},
		{"/usr/sbin/mds", 1, "mds"},
		{"/var/run/x.sock", 1, "mds"},
		{"/home/x/Library/Caches/a b", 42, "Google Chrome"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestParsePs(t *testing.T) {
	out := "    1 /sbin/launchd\n  501 /Applications/Google Chrome.app/Contents/MacOS/Google Chrome\n\n   77 (kernel)\nbad line\n"
	got := parsePs(out)
	want := []process{
		{1, "/sbin/launchd"},
		{501, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},
		{77, "(kernel)"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("process %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestProcessRunningMatchesCurrentProcess(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(exe)
	o := snapshot(t)
	now := time.Now()
	for _, n := range []string{name, strings.ToUpper(name)} {
		busy, reason := o.CheckConditions(t.TempDir(), []rules.Condition{{ProcessRunning: n}}, now)
		if !busy || reason != n+" is running" {
			t.Errorf("process_running %q = %v, %q", n, busy, reason)
		}
	}
	busy, reason := o.CheckConditions(t.TempDir(), []rules.Condition{{ProcessRunning: "no-such-process-sapu"}}, now)
	if busy {
		t.Errorf("unknown process matched: %s", reason)
	}
}

func TestProcessRunningMatchesAppBundle(t *testing.T) {
	o := &Open{procs: []process{
		{1, "/Applications/CapCut.app/Contents/Frameworks/Helper.app/Contents/MacOS/Helper"},
		{2, "/usr/bin/capcutter"},
	}}
	for name, want := range map[string]bool{
		"CapCut": true,
		"capcut": true,
		"Helper": true,
		"Cap":    false,
		"Cut":    false,
	} {
		busy, reason := o.CheckConditions("/x", []rules.Condition{{ProcessRunning: name}}, time.Now())
		if busy != want {
			t.Errorf("process_running %q = %v (%s), want %v", name, busy, reason, want)
		}
	}
	if _, reason := o.CheckConditions("/x", []rules.Condition{{ProcessRunning: "capcut"}}, time.Now()); reason != "capcut is running" {
		t.Errorf("reason = %q", reason)
	}
}

func TestModifiedWithin(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh.txt")
	old := filepath.Join(dir, "old.txt")
	for _, p := range []string{fresh, old} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	setMtime(t, old, now.Add(-10*24*time.Hour))
	o := &Open{}
	cond := []rules.Condition{{ModifiedWithin: "3d"}}

	busy, reason := o.CheckConditions(fresh, cond, now)
	if !busy || reason != "changed within 3d" {
		t.Errorf("fresh file = %v, %q", busy, reason)
	}
	if busy, reason := o.CheckConditions(old, cond, now); busy {
		t.Errorf("old file is busy: %s", reason)
	}
	// "3d" is measured from now: a file 4 days old is fine, 2 days old is not.
	setMtime(t, old, now.Add(-4*24*time.Hour))
	if busy, _ := o.CheckConditions(old, cond, now); busy {
		t.Error("4 day old file is busy")
	}
	setMtime(t, old, now.Add(-2*24*time.Hour))
	if busy, _ := o.CheckConditions(old, cond, now); !busy {
		t.Error("2 day old file is not busy")
	}
}

func TestModifiedWithinLooksInsideFolders(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(deep, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-30 * 24 * time.Hour)
	// Make every entry old, files and folders before their parents.
	for _, p := range []string{file, deep, filepath.Join(root, "a"), root} {
		setMtime(t, p, old)
	}
	o := &Open{}
	cond := []rules.Condition{{ModifiedWithin: "1w"}}
	if busy, reason := o.CheckConditions(root, cond, now); busy {
		t.Errorf("old tree is busy: %s", reason)
	}
	setMtime(t, file, now)
	if busy, _ := o.CheckConditions(root, cond, now); !busy {
		t.Error("a fresh file deep inside did not make the folder busy")
	}
}

func TestModifiedWithinMissingPathIsNotBusy(t *testing.T) {
	o := &Open{}
	busy, reason := o.CheckConditions(filepath.Join(t.TempDir(), "gone"), []rules.Condition{{ModifiedWithin: "1d"}}, time.Now())
	if busy {
		t.Errorf("missing path is busy: %s", reason)
	}
}

func TestBadConditionsAreBusy(t *testing.T) {
	o := &Open{}
	for _, c := range []rules.Condition{{}, {ModifiedWithin: "soon"}, {ModifiedWithin: "3x"}} {
		if busy, _ := o.CheckConditions(t.TempDir(), []rules.Condition{c}, time.Now()); !busy {
			t.Errorf("condition %+v is not busy", c)
		}
	}
}

func TestNoConditionsIsNotBusy(t *testing.T) {
	o := &Open{}
	if busy, _ := o.CheckConditions(t.TempDir(), nil, time.Now()); busy {
		t.Error("no conditions is busy")
	}
}

func setMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// ChangedSince walks the whole tree, counts only mtimes after t (not at t),
// does not follow symlinks, and reports folders it cannot read.
func TestChangedSince(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	deep := filepath.Join(root, "a", "b")
	file := filepath.Join(deep, "f.db")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(t.TempDir(), "fresh")
	if err := os.WriteFile(fresh, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fresh, filepath.Join(deep, "link")); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Hour)
	old := since.Add(-24 * time.Hour)
	for _, p := range []string{file, deep, filepath.Join(root, "a"), root} {
		setMtime(t, p, old)
	}
	tv := unix.NsecToTimeval(old.UnixNano())
	if err := unix.Lutimes(filepath.Join(deep, "link"), []unix.Timeval{tv, tv}); err != nil {
		t.Fatal(err)
	}
	changedSince := func(path string) bool {
		t.Helper()
		changed, err := ChangedSince(path, since)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}

	if changedSince(root) {
		t.Error("an old tree (with a symlink to a fresh file) counts as changed")
	}
	if changedSince(filepath.Join(root, "missing")) {
		t.Error("a missing path counts as changed")
	}
	setMtime(t, file, since)
	if changedSince(root) {
		t.Error("an mtime equal to t counts as changed")
	}
	setMtime(t, file, since.Add(time.Microsecond))
	if !changedSince(root) {
		t.Error("a file deep inside modified after t does not count")
	}

	setMtime(t, file, old)
	if err := os.Chmod(deep, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(deep, 0o755) })
	if _, err := ChangedSince(root, since); err == nil {
		t.Error("an unreadable folder gave no error")
	}
}
