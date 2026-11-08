package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

// fakeSystem stands in for launchd, osascript and the disk's free space.
// Every test that may reach Install, Uninstall, Inspect or Run uses it, so no test ever
// talks to the real launchd or shows a notification.
type fakeSystem struct {
	launchctl [][]string // every launchctl call
	loaded    bool       // what launchctl print answers
	scripts   []string   // every osascript script
	free      int64      // what FreeBytes returns
	program   string     // what Executable returns
}

func fake(t *testing.T) (*fakeSystem, env.Env) {
	t.Helper()
	e := env.New(t.TempDir())
	f := &fakeSystem{free: 100_000_000_000, program: filepath.Join(e.Home, "bin", "sapu")}
	oldL, oldO, oldF, oldE := Launchctl, Osascript, FreeBytes, Executable
	Launchctl = func(args ...string) error {
		f.launchctl = append(f.launchctl, args)
		switch args[0] {
		case "print":
			if !f.loaded {
				return errors.New("launchctl print: exit status 113")
			}
		case "bootstrap":
			f.loaded = true
		case "bootout":
			f.loaded = false
		}
		return nil
	}
	Osascript = func(script string) error {
		f.scripts = append(f.scripts, script)
		return nil
	}
	FreeBytes = func(env.Env) (int64, error) { return f.free, nil }
	Executable = func() (string, error) { return f.program, nil }
	t.Cleanup(func() { Launchctl, Osascript, FreeBytes, Executable = oldL, oldO, oldF, oldE })
	return f, e
}

func (f *fakeSystem) calls() []string {
	var out []string
	for _, c := range f.launchctl {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func put(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestPlist(t *testing.T) {
	e := env.New(t.TempDir())
	program := filepath.Join(e.Home, "Homebrew & Co", "bin", `sapu"<1>`)
	text := Plist(e, program)
	for _, want := range []string{
		"<key>Label</key>\n\t<string>dev.sapu.agent</string>",
		"<key>ProgramArguments</key>\n\t<array>\n\t\t<string>" + filepath.Join(e.Home, "Homebrew &amp; Co", "bin", "sapu&#34;&lt;1&gt;") +
			"</string>\n\t\t<string>agent</string>\n\t\t<string>run</string>\n\t</array>",
		"<key>StartInterval</key>\n\t<integer>3600</integer>",
		"<key>RunAtLoad</key>\n\t<false/>",
		"<key>ProcessType</key>\n\t<string>Background</string>",
		"<key>SAPU_HOME</key>\n\t\t<string>" + e.StateDir + "</string>",
		"<key>StandardOutPath</key>\n\t<string>" + filepath.Join(e.StateDir, "agent.log") + "</string>",
		"<key>StandardErrorPath</key>\n\t<string>" + filepath.Join(e.StateDir, "agent.log") + "</string>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plist lacks %q:\n%s", want, text)
		}
	}
	if got := programOf([]byte(text)); got != program {
		t.Errorf("programOf = %q, want %q", got, program)
	}

	// macOS's own checker accepts it. plutil -lint only reads the file.
	path := filepath.Join(e.Home, "agent.plist")
	put(t, path, text)
	if out, err := exec.Command("/usr/bin/plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Errorf("plutil -lint: %v\n%s", err, out)
	}
}

// The agent reads the same sapu.toml as the user's shell: the plist passes
// SAPU_CONFIG when the user set it, and only then.
func TestPlistPassesSapuConfig(t *testing.T) {
	e := env.New(t.TempDir())
	if text := Plist(e, "/bin/sapu"); strings.Contains(text, "SAPU_CONFIG") {
		t.Errorf("SAPU_CONFIG not set, but the plist has it:\n%s", text)
	}

	e.ConfigFile, e.ConfigFromEnv = "/dotfiles/a&b/sapu.toml", true
	text := Plist(e, "/bin/sapu")
	for _, want := range []string{
		"<key>SAPU_HOME</key>\n\t\t<string>" + e.StateDir + "</string>\n\t\t<key>SAPU_CONFIG</key>\n\t\t<string>/dotfiles/a&amp;b/sapu.toml</string>\n\t</dict>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plist lacks %q:\n%s", want, text)
		}
	}
	path := filepath.Join(e.Home, "agent.plist")
	put(t, path, text)
	if out, err := exec.Command("/usr/bin/plutil", "-lint", path).CombinedOutput(); err != nil {
		t.Errorf("plutil -lint: %v\n%s", err, out)
	}
}

func TestInstallUpdateUninstall(t *testing.T) {
	f, e := fake(t)
	uid := os.Getuid()
	domain := fmt.Sprintf("gui/%d", uid)
	service := domain + "/dev.sapu.agent"
	plist := filepath.Join(e.Home, "Library", "LaunchAgents", "dev.sapu.agent.plist")

	if err := Install(e); err != nil {
		t.Fatal(err)
	}
	want := []string{"print " + service, "bootstrap " + domain + " " + plist}
	if got := f.calls(); !slices.Equal(got, want) {
		t.Errorf("first install: launchctl %q, want %q", got, want)
	}
	b, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != Plist(e, f.program) {
		t.Errorf("plist on disk:\n%s\nwant:\n%s", b, Plist(e, f.program))
	}

	// An update (a new sapu after an upgrade) boots the old one out first.
	f.launchctl = nil
	f.program = filepath.Join(e.Home, "new", "sapu")
	if err := Install(e); err != nil {
		t.Fatal(err)
	}
	want = []string{"print " + service, "bootout " + service, "bootstrap " + domain + " " + plist}
	if got := f.calls(); !slices.Equal(got, want) {
		t.Errorf("update: launchctl %q, want %q", got, want)
	}
	if b, _ := os.ReadFile(plist); programOf(b) != f.program {
		t.Errorf("updated plist runs %q, want %q", programOf(b), f.program)
	}

	// Uninstall: bootout, then fsop removes the plist.
	f.launchctl = nil
	if err := Uninstall(e); err != nil {
		t.Fatal(err)
	}
	want = []string{"print " + service, "bootout " + service}
	if got := f.calls(); !slices.Equal(got, want) {
		t.Errorf("off: launchctl %q, want %q", got, want)
	}
	if exists(plist) {
		t.Error("plist still there when both are off")
	}

	// Again: nothing loaded, nothing to remove.
	f.launchctl = nil
	if err := Uninstall(e); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(); !slices.Equal(got, []string{"print " + service}) {
		t.Errorf("off again: launchctl %q", got)
	}
}

// Sapu never writes its plist through a symlink, and never removes anything
// but a regular file it owns (fsop.RemoveOwnLaunchAgent).
func TestInstallRefusesSymlinkedPlist(t *testing.T) {
	_, e := fake(t)
	target := filepath.Join(e.Home, "precious.plist")
	put(t, target, "precious")
	plist := PlistPath(e)
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, plist); err != nil {
		t.Fatal(err)
	}
	if err := Install(e); err == nil {
		t.Error("Install wrote through a symlink")
	}
	if err := Uninstall(e); err == nil {
		t.Error("Uninstall removed a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "precious" || !exists(plist) {
		t.Errorf("symlink or its target changed: %q", b)
	}
}

func TestInspect(t *testing.T) {
	f, e := fake(t)
	c := DefaultConfig()

	info, err := Inspect(e, c)
	if err != nil {
		t.Fatal(err)
	}
	if info.Installed || info.Loaded || len(info.Warnings) != 0 {
		t.Errorf("nothing installed: %+v", info)
	}

	c.Schedule.Enabled = true
	put(t, f.program, "binary")
	if err := Install(e); err != nil {
		t.Fatal(err)
	}
	f.launchctl = nil
	info, err = Inspect(e, c)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Installed || !info.Loaded || info.Program != f.program || info.ProgramMissing || len(info.Warnings) != 0 {
		t.Errorf("installed: %+v", info)
	}

	// After an upgrade the plist points at a binary that is gone.
	put(t, PlistPath(e), Plist(e, filepath.Join(e.Home, "Cellar", "sapu", "1.0", "bin", "sapu")))
	info, err = Inspect(e, c)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ProgramMissing || len(info.Warnings) != 1 || !strings.Contains(info.Warnings[0], "Run sapu schedule on again") {
		t.Errorf("missing program: %+v", info)
	}

	// Only alerts on: the advice does not turn the schedule on.
	c.Schedule.Enabled, c.Alerts.Enabled = false, true
	info, _ = Inspect(e, c)
	if len(info.Warnings) != 1 || !strings.Contains(info.Warnings[0], "Run sapu alerts on again") {
		t.Errorf("missing program, alerts only: %+v", info.Warnings)
	}

	// On, but launchd does not have it.
	put(t, PlistPath(e), Plist(e, f.program))
	f.loaded = false
	info, _ = Inspect(e, c)
	if len(info.Warnings) != 1 || !strings.Contains(info.Warnings[0], "not running") {
		t.Errorf("not loaded: %+v", info.Warnings)
	}

	// Inspect only reads: launchctl print and nothing else.
	for _, call := range f.launchctl {
		if call[0] != "print" {
			t.Errorf("Inspect ran launchctl %v", call)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	e := env.New(t.TempDir())
	c, err := LoadConfig(e)
	if err != nil {
		t.Fatal(err)
	}
	if c != DefaultConfig() {
		t.Errorf("missing file: %+v", c)
	}
	c.Schedule = Schedule{Enabled: true, Every: EveryDaily}
	c.Alerts = Alerts{Enabled: true, MinFreeBytes: 3, DropBytesPerDay: 4}
	if err := SaveConfig(e, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(e)
	if err != nil || got != c {
		t.Errorf("round trip: %+v, %v", got, err)
	}
	b, _ := os.ReadFile(ConfigPath(e))
	for _, key := range []string{`"schedule"`, `"enabled"`, `"every": "daily"`, `"alerts"`, `"min_free_bytes": 3`, `"drop_bytes_per_day": 4`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("agent.json lacks %s:\n%s", key, b)
		}
	}
	c.Schedule.Every = "hourly"
	if err := SaveConfig(e, c); err == nil {
		t.Error("saved every=hourly")
	}
	put(t, ConfigPath(e), `{"schedule": {"enabled": true, "every": "monthly"}}`)
	if _, err := LoadConfig(e); err == nil {
		t.Error("loaded every=monthly")
	}
}
