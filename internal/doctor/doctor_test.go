package doctor

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

func fakes(t *testing.T, term string, version string, versionErr error, lsofErr error) {
	t.Helper()
	oldT, oldV, oldL := termProgram, macOSVersion, lookPath
	t.Cleanup(func() { termProgram, macOSVersion, lookPath = oldT, oldV, oldL })
	termProgram = func() string { return term }
	macOSVersion = func() (string, error) { return version, versionErr }
	lookPath = func(string) (string, error) {
		if lsofErr != nil {
			return "", lsofErr
		}
		return "/usr/sbin/lsof", nil
	}
}

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
}

func TestRunOrder(t *testing.T) {
	fakes(t, "ghostty", "14.5", nil, nil)
	checks := Run(context.Background(), env.New(t.TempDir()))
	want := []string{"full_disk_access", "state_dir", "same_volume", "macos_version", "lsof", "free_space"}
	if len(checks) != len(want) {
		t.Fatalf("got %d checks", len(checks))
	}
	for i, c := range checks {
		if c.ID != want[i] {
			t.Errorf("check %d: got %s, want %s", i, c.ID, want[i])
		}
		if c.Title == "" || c.Detail == "" {
			t.Errorf("%s: title and detail must be filled", c.ID)
		}
		if !c.OK && c.Fix == "" && c.ID != "full_disk_access" {
			t.Errorf("%s: failed without a fix", c.ID)
		}
	}
}

func TestHasFullDiskAccess(t *testing.T) {
	skipIfRoot(t)
	e := env.New(t.TempDir())
	if HasFullDiskAccess(e) {
		t.Error("no probe exists: access cannot be confirmed")
	}

	tcc := filepath.Join(e.Home, tccProbes[0])
	if err := os.MkdirAll(filepath.Dir(tcc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tcc, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasFullDiskAccess(e) {
		t.Error("readable probe: want access")
	}

	if err := os.Chmod(tcc, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(tcc, 0o644); err != nil {
			t.Error(err)
		}
	})
	if HasFullDiskAccess(e) {
		t.Error("unreadable probe: want no access")
	}
}

func TestHasFullDiskAccessSkipsMissingProbes(t *testing.T) {
	e := env.New(t.TempDir())
	old := tccProbes
	t.Cleanup(func() { tccProbes = old })
	tccProbes = []string{"missing", "present"}
	if err := os.WriteFile(filepath.Join(e.Home, "present"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasFullDiskAccess(e) {
		t.Error("a missing probe must be skipped, then the next one decides")
	}
}

func TestFullDiskAccessCheckNamesTerminal(t *testing.T) {
	cases := map[string]string{
		"ghostty":        "Ghostty",
		"iTerm.app":      "iTerm.app",
		"Apple_Terminal": "Terminal",
		"":               "your terminal app",
	}
	for term, want := range cases {
		fakes(t, term, "14.0", nil, nil)
		c := checkFullDiskAccess(env.New(t.TempDir())) // empty home: no access
		if c.OK {
			t.Fatal("empty home must report no access")
		}
		if !strings.Contains(c.Detail, "terminal app") || !strings.Contains(c.Detail, want) {
			t.Errorf("TERM_PROGRAM=%q: detail %q does not name %q", term, c.Detail, want)
		}
		if !strings.Contains(c.Fix, want) || !strings.Contains(c.Fix, "sapu doctor --open") {
			t.Errorf("TERM_PROGRAM=%q: fix %q", term, c.Fix)
		}
	}
}

func TestStateDir(t *testing.T) {
	skipIfRoot(t)

	t.Run("exists and writable", func(t *testing.T) {
		e := env.New(t.TempDir())
		if err := os.Mkdir(e.StateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if c := checkStateDir(e); !c.OK {
			t.Errorf("%+v", c)
		}
	})

	t.Run("exists but not writable", func(t *testing.T) {
		e := env.New(t.TempDir())
		if err := os.Mkdir(e.StateDir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(e.StateDir, 0o755); err != nil {
				t.Error(err)
			}
		})
		c := checkStateDir(e)
		if c.OK || c.Fix == "" {
			t.Errorf("%+v", c)
		}
	})

	t.Run("missing but creatable", func(t *testing.T) {
		e := env.New(t.TempDir())
		c := checkStateDir(e)
		if !c.OK {
			t.Errorf("%+v", c)
		}
		if _, err := os.Stat(e.StateDir); !errors.Is(err, fs.ErrNotExist) {
			t.Error("the check must not create the folder")
		}
	})

	t.Run("missing and parent not writable", func(t *testing.T) {
		e := env.New(t.TempDir())
		if err := os.Chmod(e.Home, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(e.Home, 0o755); err != nil {
				t.Error(err)
			}
		})
		c := checkStateDir(e)
		if c.OK || c.Fix == "" {
			t.Errorf("%+v", c)
		}
	})

	t.Run("is a file", func(t *testing.T) {
		e := env.New(t.TempDir())
		if err := os.WriteFile(e.StateDir, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if c := checkStateDir(e); c.OK {
			t.Errorf("%+v", c)
		}
	})
}

func TestSameVolume(t *testing.T) {
	e := env.New(t.TempDir())
	if c := checkSameVolume(e); !c.OK {
		t.Errorf("state folder inside home: %+v", c)
	}
	// /dev is a separate filesystem on macOS and Linux.
	e.StateDir = "/dev/sapu"
	if c := checkSameVolume(e); c.OK || c.Fix == "" {
		t.Errorf("state folder on another volume: %+v", c)
	}
}

func TestMacOSVersion(t *testing.T) {
	cases := []struct {
		version string
		err     error
		ok      bool
	}{
		{"13.0", nil, true},
		{"14.5.1", nil, true},
		{"26", nil, true},
		{"12.7.6", nil, false},
		{"10.15", nil, false},
		{"", errors.New("boom"), false},
		{"abc", nil, false},
	}
	for _, c := range cases {
		fakes(t, "", c.version, c.err, nil)
		if got := checkMacOSVersion(); got.OK != c.ok {
			t.Errorf("version %q: ok=%v, want %v (%+v)", c.version, got.OK, c.ok, got)
		}
	}
}

func TestLsof(t *testing.T) {
	fakes(t, "", "14.0", nil, nil)
	if c := checkLsof(); !c.OK {
		t.Errorf("%+v", c)
	}
	fakes(t, "", "14.0", nil, errors.New("not found"))
	if c := checkLsof(); c.OK || c.Fix == "" {
		t.Errorf("%+v", c)
	}
}

func TestFreeSpace(t *testing.T) {
	c := checkFreeSpace(env.New(t.TempDir()))
	if !strings.HasSuffix(c.Detail, "GB free") {
		t.Errorf("detail %q", c.Detail)
	}
	// The real disk decides OK; either way a failing check says what to do.
	if !c.OK && c.Fix == "" {
		t.Errorf("%+v", c)
	}
	c = checkFreeSpace(env.New(filepath.Join(t.TempDir(), "missing")))
	if c.OK {
		t.Errorf("unreadable home: %+v", c)
	}

	old := FreeBytes
	t.Cleanup(func() { FreeBytes = old })
	FreeBytes = func(string) (uint64, error) { return 4_200_000_000, nil }
	if c = checkFreeSpace(env.New(t.TempDir())); c.OK || c.Detail != "4.2 GB free" || c.Fix == "" {
		t.Errorf("low space: %+v", c)
	}
	FreeBytes = func(string) (uint64, error) { return 50_000_000_000, nil }
	if c = checkFreeSpace(env.New(t.TempDir())); !c.OK || c.Fix != "" {
		t.Errorf("enough space: %+v", c)
	}
}

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

func TestRunLeavesHomeUntouched(t *testing.T) {
	fakes(t, "ghostty", "14.0", nil, nil)
	e := env.New(t.TempDir())
	tcc := filepath.Join(e.Home, tccProbes[0])
	if err := os.MkdirAll(filepath.Dir(tcc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tcc, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The state folder does not exist; Run must not create it.
	before := snapshot(t, e.Home)
	Run(context.Background(), e)
	HasFullDiskAccess(e)
	after := snapshot(t, e.Home)
	if len(before) != len(after) {
		t.Fatalf("entries before %d, after %d", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed", p)
		}
	}
}

// With no TERM_PROGRAM the text names "your terminal app" once, not
// "the terminal app (your terminal app)".
func TestFullDiskAccessCheckWithUnknownTerminal(t *testing.T) {
	fakes(t, "", "14.0", nil, nil)
	c := checkFullDiskAccess(env.New(t.TempDir()))
	if strings.Contains(c.Detail, "(") || strings.Contains(c.Fix, "(") {
		t.Errorf("parentheses around the app name:\n%s\n%s", c.Detail, c.Fix)
	}
	if !strings.HasPrefix(c.Detail, "The permission belongs to your terminal app, not to sapu.") {
		t.Errorf("detail %q", c.Detail)
	}
	if !strings.Contains(c.Fix, "turn on your terminal app, then restart it.") {
		t.Errorf("fix %q", c.Fix)
	}
}
