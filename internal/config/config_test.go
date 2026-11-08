package config

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// home returns an Env for a temp home.
func home(t *testing.T) env.Env {
	t.Helper()
	return env.New(t.TempDir())
}

func writeConfig(t *testing.T, e env.Env, text string) string {
	t.Helper()
	path := filepath.Join(e.Home, ".config", "sapu", "sapu.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingFileGivesDefaults(t *testing.T) {
	e := home(t)
	c, err := Load(e)
	if err != nil {
		t.Fatal(err)
	}
	if c.Found || c.FromEnv {
		t.Errorf("Found=%v FromEnv=%v, want both false", c.Found, c.FromEnv)
	}
	if want := filepath.Join(e.Home, ".config", "sapu", "sapu.toml"); c.File != want {
		t.Errorf("File = %s, want %s", c.File, want)
	}
	if c.Profile != Standard || c.MaxAge != fsop.DefaultMaxAge || c.MaxBytes != fsop.DefaultMaxBytes {
		t.Errorf("defaults: %+v", c)
	}
	if c.PurgeOlderThan != 0 || c.CleanExclude != nil || c.PurgeRoots != nil || c.Whitelist != nil || len(c.Set) != 0 {
		t.Errorf("defaults: %+v", c)
	}
}

func TestEverySetting(t *testing.T) {
	e := home(t)
	writeConfig(t, e, `
profile = "aggressive"

[quarantine]
max_age = "30d"
max_size = "50GB"

[clean]
exclude = ["chrome.cache", "browsers"]

[purge]
roots = ["~/Projects", "/Volumes/Work/code"]
older_than = "2w"

[whitelist]
paths = ["~/Projects/keep", "/Volumes/Work/keep/*"]
`)
	c, err := Load(e)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Found || c.Profile != Aggressive || c.MaxAge != 30*24*time.Hour || c.MaxBytes != 50_000_000_000 {
		t.Errorf("got %+v", c)
	}
	if !slices.Equal(c.CleanExclude, []string{"chrome.cache", "browsers"}) ||
		!slices.Equal(c.PurgeRoots, []string{"~/Projects", "/Volumes/Work/code"}) ||
		!slices.Equal(c.Whitelist, []string{"~/Projects/keep", "/Volumes/Work/keep/*"}) ||
		c.PurgeOlderThan != 14*24*time.Hour {
		t.Errorf("got %+v", c)
	}
	for _, k := range Keys {
		if !c.Set[k] {
			t.Errorf("%s not marked as set", k)
		}
	}
}

func TestSapuConfigChoosesTheFile(t *testing.T) {
	e := home(t)
	file := filepath.Join(t.TempDir(), "dotfiles", "sapu.toml")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`profile = "conservative"`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.ConfigFile, e.ConfigFromEnv = file, true
	c, err := Load(e)
	if err != nil {
		t.Fatal(err)
	}
	if c.File != file || !c.FromEnv || !c.Found || c.Profile != Conservative {
		t.Errorf("got %+v", c)
	}

	e.ConfigFile = "dotfiles/sapu.toml"
	if _, err := Load(e); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("relative SAPU_CONFIG: err = %v, want a refusal", err)
	}
}

// Every mistake names the file and the line it is on.
func TestErrorsNameTheLine(t *testing.T) {
	cases := []struct {
		name, text, line, want string
	}{
		{"unknown key", "profile = \"standard\"\n\n[quarantine]\nmax_agee = \"7d\"\n", "line 4", `"quarantine.max_agee"`},
		{"unknown top-level key", "# settings\ncolor = true\n", "line 2", `"color"`},
		{"unknown table", "[purge]\nolder_than = \"1d\"\n[extras]\nx = 1\n", "line 3", `"extras"`},
		{"bad profile", "\n\nprofile = \"yolo\"\n", "line 3", "profile"},
		{"age without unit", "[quarantine]\nmax_age = \"7\"\n", "line 2", "max_age"},
		{"age as a number", "[quarantine]\nmax_age = 7\n", "line 2", "max_age"},
		{"bad size unit", "[quarantine]\nmax_size = \"20GiB\"\n", "line 2", "max_size"},
		{"string instead of list", "[clean]\nexclude = \"chrome.cache\"\n", "line 2", "exclude"},
		{"blank exclude name", "[clean]\nexclude = [\"\"]\n", "line 2", "exclude"},
		{"relative purge root", "[purge]\nroots = [\"code\"]\n", "line 2", "roots"},
		{"relative whitelist path", "[whitelist]\npaths = [\"Documents\"]\n", "line 2", "paths"},
		{"whitelist with **", "[whitelist]\npaths = [\"~/x/**\"]\n", "line 2", "paths"},
		{"syntax error", "profile = \"standard\n", "line 1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse("/x/sapu.toml", []byte(c.text))
			if err == nil {
				t.Fatal("no error")
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "/x/sapu.toml: ") || !strings.Contains(msg, c.line) || !strings.Contains(msg, c.want) {
				t.Errorf("error %q: want the file, %q and %q", msg, c.line, c.want)
			}
		})
	}
}

func TestLimits(t *testing.T) {
	ok := []string{
		"[quarantine]\nmax_age = \"1d\"", "[quarantine]\nmax_age = \"24h\"", "[quarantine]\nmax_age = \"52w\"",
		"[quarantine]\nmax_size = \"1GB\"", "[quarantine]\nmax_size = \"1000MB\"", "[quarantine]\nmax_size = \"2tb\"",
		"[purge]\nolder_than = \"0d\"",
	}
	for _, text := range ok {
		if _, err := Parse("sapu.toml", []byte(text)); err != nil {
			t.Errorf("%q: %v", text, err)
		}
	}
	bad := []string{
		"[quarantine]\nmax_age = \"23h\"", "[quarantine]\nmax_age = \"0d\"", "[quarantine]\nmax_age = \"-1d\"",
		"[quarantine]\nmax_size = \"999MB\"", "[quarantine]\nmax_size = \"0GB\"",
		"[quarantine]\nmax_size = \"99999999999TB\"", "[quarantine]\nmax_age = \"9999999999999w\"",
	}
	for _, text := range bad {
		if _, err := Parse("sapu.toml", []byte(text)); err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("%q: err = %v, want a refusal on line 2", text, err)
		}
	}
}

// No key can turn protection off, add an allow list or open a protected
// place: every attempt is an unknown key.
func TestProtectionCannotBeChanged(t *testing.T) {
	attempts := []string{
		"protect = false",
		"protection = \"off\"",
		"[protect]\nenabled = false",
		"[protect]\nallow_sources = [\"clean\"]",
		"[protect.claude]\nallow_sources = [\"clean\"]",
		"[whitelist]\nallow = [\"~/.claude\"]",
		"[whitelist]\nallow_sources = [\"clean\"]",
		"[whitelist]\nremove = [\"~/Documents\"]",
		"[clean]\nallow = [\"~/.claude\"]",
	}
	for _, text := range attempts {
		_, err := Parse("sapu.toml", []byte(text))
		if err == nil || !strings.Contains(err.Error(), "unknown setting") {
			t.Errorf("%q: err = %v, want an unknown setting", text, err)
		}
	}
}

func TestProfileRisk(t *testing.T) {
	want := map[string]rules.Risk{Conservative: rules.RiskSafe, Standard: rules.RiskModerate, Aggressive: rules.RiskRisky}
	for p, r := range want {
		if got, ok := ProfileRisk(p); !ok || got != r {
			t.Errorf("ProfileRisk(%q) = %q, %v; want %q", p, got, ok, r)
		}
	}
	if _, ok := ProfileRisk("yolo"); ok {
		t.Error("ProfileRisk(yolo) is ok")
	}
}

func TestCheckExclude(t *testing.T) {
	rs := []rules.Rule{{ID: "chrome.cache", Category: rules.CategoryBrowsers}, {ID: "npm.cache", Category: rules.CategoryDev}}
	c := Defaults()
	c.File = "sapu.toml"
	c.CleanExclude = []string{"chrome.cache", "dev"}
	if err := c.CheckExclude(rs); err != nil {
		t.Errorf("known names: %v", err)
	}
	if !c.Excluded(rs[0]) || !c.Excluded(rs[1]) {
		t.Error("an excluded rule is not excluded")
	}
	c.CleanExclude = []string{"chrome.caches"}
	if err := c.CheckExclude(rs); err == nil || !strings.Contains(err.Error(), `"chrome.caches"`) {
		t.Errorf("unknown name: err = %v", err)
	}
	if c.Excluded(rs[0]) {
		t.Error("chrome.cache excluded by chrome.caches")
	}
}

// The example parses as it is (every setting at its default), and removing
// "# " in front of every setting line gives a valid file that sets them all.
func TestExample(t *testing.T) {
	c, err := Parse("example", []byte(Example))
	if err != nil {
		t.Fatalf("Example: %v", err)
	}
	if len(c.Set) != 0 {
		t.Errorf("Example sets %v; every setting should be commented out", c.Set)
	}

	setting := regexp.MustCompile(`(?m)^# ([a-z_]+ = .*)$`)
	on := setting.ReplaceAllString(Example, "$1")
	c, err = Parse("example", []byte(on))
	if err != nil {
		t.Fatalf("Example with every setting on: %v\n%s", err, on)
	}
	for _, k := range Keys {
		if !c.Set[k] {
			t.Errorf("the example has no line for %s", k)
		}
	}
	d := Defaults()
	if c.Profile != d.Profile || c.MaxAge != d.MaxAge || c.MaxBytes != d.MaxBytes {
		t.Errorf("the example's values are not the defaults: %+v", c)
	}
}

func TestInit(t *testing.T) {
	e := home(t)
	path, err := Init(e)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != Example {
		t.Fatalf("Init wrote %q, %v", data, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode: %v, %v", info.Mode(), err)
	}

	if err := os.WriteFile(path, []byte("profile = \"aggressive\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(e); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second Init: err = %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "profile = \"aggressive\"\n" {
		t.Errorf("second Init changed the file: %q", data)
	}
}

// A dotfiles symlink that points nowhere yet is something that exists: Init
// does not write through it.
func TestInitRefusesASymlink(t *testing.T) {
	e := home(t)
	target := filepath.Join(t.TempDir(), "dotfiles-sapu.toml")
	link := filepath.Join(e.Home, ".config", "sapu", "sapu.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(e); err == nil {
		t.Error("Init wrote through a symlink")
	}
	if _, err := os.Lstat(target); err == nil {
		t.Error("the symlink target was created")
	}
}

// A symlinked settings file (dotfiles) is read where it points.
func TestLoadFollowsASymlink(t *testing.T) {
	e := home(t)
	target := filepath.Join(t.TempDir(), "sapu.toml")
	if err := os.WriteFile(target, []byte(`profile = "conservative"`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(e.Home, ".config", "sapu", "sapu.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	c, err := Load(e)
	if err != nil || c.Profile != Conservative {
		t.Errorf("Load = %+v, %v", c, err)
	}
}

func TestFormat(t *testing.T) {
	for d, want := range map[time.Duration]string{7 * 24 * time.Hour: "7d", 36 * time.Hour: "36h", 90 * time.Minute: "90m"} {
		if got := FormatAge(d); got != want {
			t.Errorf("FormatAge(%v) = %s, want %s", d, got, want)
		}
	}
	for n, want := range map[int64]string{20_000_000_000: "20GB", 2_000_000_000_000: "2TB", 1_500_000_000: "1500MB"} {
		if got := FormatSize(n); got != want {
			t.Errorf("FormatSize(%d) = %s, want %s", n, got, want)
		}
	}
}

// config init never replaces anything: not a file, and not a symlink, even
// one that points nowhere.
func TestInitRefusesSymlinks(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		e := home(t)
		dir := filepath.Dir(e.ConfigFile)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(e.Home, "precious.toml")
		if !dangling {
			if err := os.WriteFile(target, []byte("profile = \"conservative\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(target, e.ConfigFile); err != nil {
			t.Fatal(err)
		}

		if _, err := Init(e); err == nil {
			t.Errorf("dangling=%v: Init wrote through a symlink", dangling)
		}
		b, err := os.ReadFile(target)
		switch {
		case dangling && err == nil:
			t.Errorf("Init created the file behind a dangling symlink: %q", b)
		case !dangling && string(b) != "profile = \"conservative\"\n":
			t.Errorf("the file behind the symlink changed to %q", b)
		}
	}
}
