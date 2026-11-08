package explain

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "explain", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeRun answers tmutil and sysctl with canned output. Any other command fails the test.
func fakeRun(t *testing.T, tmutil, swap []byte, tmutilErr, swapErr error) {
	t.Helper()
	old := run
	t.Cleanup(func() { run = old })
	run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := name + " " + strings.Join(args, " ")
		switch cmd {
		case "tmutil listlocalsnapshots /":
			return tmutil, tmutilErr
		case "sysctl -n vm.swapusage":
			return swap, swapErr
		}
		t.Errorf("unexpected command: %s", cmd)
		return nil, errors.New("unexpected command")
	}
}

// pointSystemPaths sends the absolute system paths into dir.
func pointSystemPaths(t *testing.T, dir string) {
	t.Helper()
	oldS, oldL, oldA := spotlightDir, sleepImage, systemAssets
	t.Cleanup(func() { spotlightDir, sleepImage, systemAssets = oldS, oldL, oldA })
	spotlightDir = filepath.Join(dir, "spotlight")
	sleepImage = filepath.Join(dir, "sleepimage")
	systemAssets = filepath.Join(dir, "assets")
}

func setup(t *testing.T) env.Env {
	t.Helper()
	fakeRun(t, fixture(t, "tmutil_list.txt"), fixture(t, "swapusage.txt"), nil, nil)
	pointSystemPaths(t, t.TempDir())
	return env.New(t.TempDir())
}

func byID(t *testing.T, rows []Row) map[string]Row {
	t.Helper()
	m := map[string]Row{}
	for _, r := range rows {
		m[r.ID] = r
	}
	return m
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeSparse makes a file with a big apparent size but only a little data.
func writeSparse(t *testing.T, path string, apparent int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(apparent, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExplainOrderAndFields(t *testing.T) {
	e := setup(t)
	rows, err := Explain(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tm_snapshots", "purgeable", "spotlight", "ios_backups", "messages", "vm_images",
		"xcode", "cloud", "system_assets", "swap", "user_caches"}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(rows), len(want))
	}
	for i, r := range rows {
		if r.ID != want[i] {
			t.Errorf("row %d: got %s, want %s", i, r.ID, want[i])
		}
		if r.Title == "" || r.Explain == "" || r.Action == "" {
			t.Errorf("%s: title, explain and action must be filled: %+v", r.ID, r)
		}
		if r.Bytes < 0 && r.Unmeasured == "" {
			t.Errorf("%s: Bytes is -1 without a reason", r.ID)
		}
		if r.Bytes >= 0 && r.Unmeasured != "" {
			t.Errorf("%s: has a size and a reason", r.ID)
		}
		if r.NeedsAdmin && len(r.Command) == 0 {
			t.Errorf("%s: NeedsAdmin without a command", r.ID)
		}
	}
}

func TestEmptyHomeMeasuresZero(t *testing.T) {
	e := setup(t)
	rows, err := Explain(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	m := byID(t, rows)
	for _, id := range []string{"ios_backups", "messages", "vm_images", "xcode", "cloud", "user_caches", "spotlight", "system_assets"} {
		if m[id].Bytes != 0 {
			t.Errorf("%s: got %d, want 0 for a missing folder", id, m[id].Bytes)
		}
	}
}

func TestSparseFileCountsAllocatedBytes(t *testing.T) {
	e := setup(t)
	const apparent = 8 << 30
	writeSparse(t, filepath.Join(e.Home, "Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw"), apparent)
	writeFile(t, filepath.Join(e.Home, ".orbstack/data/a"), 4096)
	writeFile(t, filepath.Join(e.Home, "Library/Group Containers/HUAQ24HBR6.dev.orbstack/b"), 4096)
	writeFile(t, filepath.Join(e.Home, "Parallels/vm/c"), 4096)
	writeFile(t, filepath.Join(e.Home, "Library/Containers/com.utmapp.UTM/Data/Documents/d"), 4096)

	rows, err := Explain(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	r := byID(t, rows)["vm_images"]
	if r.Bytes < 4*4096 || r.Bytes > 4<<20 {
		t.Errorf("vm_images = %d, want about 16 KiB plus the sparse file's few blocks, far below %d", r.Bytes, int64(apparent))
	}
}

func TestSourcesAddUpTheirPaths(t *testing.T) {
	e := setup(t)
	writeFile(t, filepath.Join(e.Home, "Library/Developer/Xcode/DerivedData/a"), 8192)
	writeFile(t, filepath.Join(e.Home, "Library/Developer/CoreSimulator/Devices/b"), 8192)
	writeFile(t, filepath.Join(e.Home, "Library/Developer/Xcode/iOS DeviceSupport/c"), 8192)
	writeFile(t, filepath.Join(e.Home, "Library/Developer/Xcode/Archives/d"), 8192)
	writeFile(t, filepath.Join(e.Home, "Library/CloudStorage/GoogleDrive-x/f"), 8192)
	writeFile(t, filepath.Join(e.Home, "Library/Mobile Documents/g"), 8192)
	writeFile(t, filepath.Join(e.Home, "Library/Application Support/Google/DriveFS/h"), 8192)
	writeFile(t, filepath.Join(e.Home, "Library/Caches/i"), 8192)
	writeFile(t, filepath.Join(e.Home, "Library/Logs/j"), 8192)
	writeFile(t, filepath.Join(e.Home, "Library/Application Support/MobileSync/Backup/k"), 8192)
	writeFile(t, filepath.Join(e.Home, "Library/Messages/Attachments/l"), 8192)
	writeFile(t, filepath.Join(spotlightDir, "m"), 8192)
	writeFile(t, filepath.Join(systemAssets, "n"), 8192)

	rows, err := Explain(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	m := byID(t, rows)
	for id, n := range map[string]int64{"xcode": 4, "cloud": 3, "user_caches": 2, "ios_backups": 1, "messages": 1, "spotlight": 1, "system_assets": 1} {
		if m[id].Bytes < n*8192 {
			t.Errorf("%s: got %d, want at least %d", id, m[id].Bytes, n*8192)
		}
	}
}

func TestPermissionDeniedBecomesUnmeasured(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	e := setup(t)
	backup := filepath.Join(e.Home, "Library/Application Support/MobileSync/Backup")
	writeFile(t, filepath.Join(backup, "x"), 4096)
	if err := os.Chmod(backup, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(backup, 0o755); err != nil {
			t.Error(err)
		}
	})
	if err := os.MkdirAll(spotlightDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(spotlightDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(spotlightDir, 0o755); err != nil {
			t.Error(err)
		}
	})

	rows, err := Explain(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	m := byID(t, rows)
	if r := m["ios_backups"]; r.Bytes != -1 || !strings.Contains(r.Unmeasured, "Full Disk Access") {
		t.Errorf("ios_backups: %+v", r)
	}
	if r := m["spotlight"]; r.Bytes != -1 || !strings.Contains(r.Unmeasured, "admin") {
		t.Errorf("spotlight: %+v", r)
	}
	if m["messages"].Bytes != 0 {
		t.Errorf("one failing source must not affect others: %+v", m["messages"])
	}
}

func TestTimeMachine(t *testing.T) {
	cases := []struct {
		name      string
		out       []byte
		err       error
		wantBytes int64
		wantTitle string
		wantWhy   string
		wantCmd   bool
	}{
		{"two snapshots", fixture(t, "tmutil_list.txt"), nil, -1, "(2)", "does not report", true},
		{"only os update snapshots", fixture(t, "tmutil_list_none.txt"), nil, 0, "snapshots", "", false},
		{"empty output", nil, nil, 0, "snapshots", "", false},
		{"command fails", nil, errors.New("exit status 1"), -1, "snapshots", "could not list", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeRun(t, c.out, fixture(t, "swapusage.txt"), c.err, nil)
			pointSystemPaths(t, t.TempDir())
			rows, err := Explain(context.Background(), env.New(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			r := rows[0]
			if r.ID != "tm_snapshots" || r.Bytes != c.wantBytes {
				t.Fatalf("got %+v", r)
			}
			if !strings.Contains(r.Title, c.wantTitle) || !strings.Contains(r.Unmeasured, c.wantWhy) {
				t.Errorf("title %q, unmeasured %q", r.Title, r.Unmeasured)
			}
			if (len(r.Command) > 0) != c.wantCmd || r.NeedsAdmin != c.wantCmd {
				t.Errorf("command %v, admin %v", r.Command, r.NeedsAdmin)
			}
		})
	}
}

func TestSwapAndSleepImage(t *testing.T) {
	cases := []struct {
		name    string
		out     []byte
		err     error
		sleep   int
		wantMin int64
		wantMax int64 // -1 means unmeasured
	}{
		{"swap only", fixture(t, "swapusage.txt"), nil, 0, 1280 << 20, 1280 << 20},
		{"swap and sleep image", fixture(t, "swapusage.txt"), nil, 8192, 1280<<20 + 8192, 1280<<20 + 8192},
		{"gigabytes", []byte("total = 2.00G  used = 1.00G  free = 1.00G\n"), nil, 0, 2 << 30, 2 << 30},
		{"command fails", nil, errors.New("boom"), 0, -1, -1},
		{"empty output", nil, nil, 0, -1, -1},
		{"garbage", []byte("hello"), nil, 0, -1, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeRun(t, nil, c.out, nil, c.err)
			dir := t.TempDir()
			pointSystemPaths(t, dir)
			if c.sleep > 0 {
				writeFile(t, sleepImage, c.sleep)
			}
			rows, err := Explain(context.Background(), env.New(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			r := byID(t, rows)["swap"]
			if c.wantMax == -1 {
				if r.Bytes != -1 || r.Unmeasured == "" {
					t.Errorf("got %+v, want unmeasured", r)
				}
				return
			}
			if r.Bytes < c.wantMin || r.Bytes > c.wantMax+4096 {
				t.Errorf("got %d, want about %d", r.Bytes, c.wantMin)
			}
		})
	}
}

func TestPurgeableIsNeverGuessed(t *testing.T) {
	e := setup(t)
	rows, err := Explain(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if r := byID(t, rows)["purgeable"]; r.Bytes != -1 || r.Unmeasured == "" {
		t.Errorf("got %+v", r)
	}
}

func TestOnlyReadOnlyCommandsRun(t *testing.T) {
	e := setup(t)
	var mu sync.Mutex // Explain runs its sources at the same time
	var seen []string
	inner := run
	run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		seen = append(seen, name+" "+strings.Join(args, " "))
		mu.Unlock()
		return inner(ctx, name, args...)
	}
	if _, err := Explain(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	for _, c := range seen {
		if c != "tmutil listlocalsnapshots /" && c != "sysctl -n vm.swapusage" {
			t.Errorf("Explain ran %q", c)
		}
	}
	if len(seen) != 2 {
		t.Errorf("ran %v", seen)
	}
}

func TestCancel(t *testing.T) {
	e := setup(t)
	for i := 0; i < 200; i++ {
		writeFile(t, filepath.Join(e.Home, "Library/Caches", fmt.Sprint(i), "f"), 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, err := Explain(ctx, e)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(rows) != 0 {
		t.Errorf("cancelled before the first source, got %d rows", len(rows))
	}
}

// snapshot records every path with its mode, size, modification time and content.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		sum := ""
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum = fmt.Sprintf("%x", sha256.Sum256(b))
		}
		m[p] = fmt.Sprintf("%v %d %d %s", fi.Mode(), fi.Size(), fi.ModTime().UnixNano(), sum)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestExplainLeavesHomeUntouched(t *testing.T) {
	e := setup(t)
	writeFile(t, filepath.Join(e.Home, "Library/Caches/a/b"), 5000)
	writeFile(t, filepath.Join(e.Home, "Library/Messages/Attachments/c"), 100)
	writeFile(t, filepath.Join(e.Home, "Library/Developer/Xcode/DerivedData/d"), 100)
	writeSparse(t, filepath.Join(e.Home, "Parallels/disk"), 1<<30)
	before := snapshot(t, e.Home)
	if _, err := Explain(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, e.Home)
	if len(before) != len(after) {
		t.Fatalf("entries before %d, after %d", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed: %q -> %q", p, v, after[p])
		}
	}
}

func TestSlowFolderBecomesUnmeasured(t *testing.T) {
	e := setup(t)
	writeFile(t, filepath.Join(e.Home, "Library/Caches/a"), 100)
	old := measureTimeout
	t.Cleanup(func() { measureTimeout = old })
	measureTimeout = time.Nanosecond
	rows, err := Explain(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	r := byID(t, rows)["user_caches"]
	if r.Bytes != -1 || !strings.Contains(r.Unmeasured, "took too long") {
		t.Errorf("got %+v", r)
	}
}

// A row that measured zero has nothing to reclaim, so its action says so and
// it shows no command. This is the Time Machine row with no snapshots, and
// every other row of an empty Mac.
func TestRowsWithNothingToReclaimHaveNoCommand(t *testing.T) {
	fakeRun(t, fixture(t, "tmutil_list_none.txt"), []byte("total = 0.00M  used = 0.00M  free = 0.00M\n"), nil, nil)
	pointSystemPaths(t, t.TempDir())
	rows, err := Explain(context.Background(), env.New(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Bytes != 0 {
			continue
		}
		if r.Action != "Nothing to do." || len(r.Command) != 0 || r.NeedsAdmin {
			t.Errorf("%s: measured 0 but action %q, command %v, admin %v", r.ID, r.Action, r.Command, r.NeedsAdmin)
		}
	}
	tm := byID(t, rows)["tm_snapshots"]
	if tm.Bytes != 0 || tm.Action != "Nothing to do." {
		t.Errorf("tm_snapshots: %+v", tm)
	}
}

func TestRowsWithDataKeepTheirAction(t *testing.T) {
	e := setup(t)
	writeFile(t, filepath.Join(e.Home, "Library/Developer/Xcode/DerivedData/x"), 4096)
	rows, err := Explain(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	m := byID(t, rows)
	if x := m["xcode"]; x.Action == "Nothing to do." || len(x.Command) == 0 {
		t.Errorf("xcode with data lost its action: %+v", x)
	}
	if tm := m["tm_snapshots"]; !strings.Contains(tm.Action, "thin them") || len(tm.Command) == 0 {
		t.Errorf("tm_snapshots with snapshots lost its action: %+v", tm)
	}
}
