package pkgs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Tests here replace the package variables, so none of them is parallel.

// fake makes LookPath find exactly the programs given (name -> path) and
// Output answer exactly the command lines given ("path arg arg" -> stdout).
// Anything else fails, so no real tool is ever run. It returns the command
// lines Output was asked, with the folder they ran in.
func fake(t *testing.T, programs, outputs map[string]string) *[]string {
	t.Helper()
	oldLook, oldOut := LookPath, Output
	t.Cleanup(func() { LookPath, Output = oldLook, oldOut })
	var asked []string
	LookPath = func(name string) (string, error) {
		if p, ok := programs[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	Output = func(_ context.Context, dir, program string, args ...string) (string, error) {
		line := strings.Join(append([]string{program}, args...), " ")
		asked = append(asked, dir+": "+line)
		if out, ok := outputs[line]; ok {
			return out, nil
		}
		return "", errors.New("exit status 1")
	}
	return &asked
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func find(t *testing.T, e env.Env) ([]plan.Item, []string) {
	t.Helper()
	items, notes, err := Find(t.Context(), e)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	return items, notes
}

func byCommand(items []plan.Item) map[string]plan.Item {
	out := map[string]plan.Item{}
	for _, it := range items {
		if it.Action == rules.ActionOfficialCommand {
			out[strings.Join(it.Command, " ")] = it
		}
	}
	return out
}

func quarantined(items []plan.Item) []string {
	var out []string
	for _, it := range items {
		if it.Action == rules.ActionQuarantine {
			out = append(out, filepath.Base(it.Path))
		}
	}
	slices.Sort(out)
	return out
}

func TestToolsBecomeCommandItems(t *testing.T) {
	e := env.New(t.TempDir())
	h := func(rel string) string { return filepath.Join(e.Home, rel) }
	write(t, h("Library/Caches/Homebrew/downloads/x.tar.gz"), "bottle")
	write(t, h(".npm/_cacache/content-v2/blob"), "npm")
	write(t, h("Library/Caches/go-build/aa/bb"), "build")
	write(t, h("go/pkg/mod/example.com/m@v1/m.go"), "module")
	mkdir(t, h(".cache/uv")) // empty: nothing to clean
	write(t, h("Library/Caches/CocoaPods/Pods/x"), "pod")
	docker := filepath.Join(t.TempDir(), "docker") // its item's path is the program, so it must exist
	write(t, docker, "#!/bin/sh\n")
	asked := fake(t,
		map[string]string{
			"brew": "/fake/brew", "npm": "/fake/npm", "go": "/fake/go", "uv": "/fake/uv",
			"pnpm": "/fake/pnpm", "docker": docker,
		},
		map[string]string{
			"/fake/brew --cache":      h("Library/Caches/Homebrew"),
			"/fake/go env GOCACHE":    h("Library/Caches/go-build"),
			"/fake/go env GOMODCACHE": "warning: something\n" + h("go/pkg/mod"),
			"/fake/uv cache dir":      h(".cache/uv"),
			// pnpm store path fails
		},
	)
	items, notes := find(t, e)

	got := byCommand(items)
	want := map[string]string{ // command -> path
		"/fake/brew cleanup --prune=all -s": h("Library/Caches/Homebrew"),
		"/fake/npm cache clean --force":     h(".npm/_cacache"),
		"/fake/go clean -cache":             h("Library/Caches/go-build"),
		"/fake/go clean -modcache":          h("go/pkg/mod"),
	}
	for cmd, path := range want {
		it, ok := got[cmd]
		if !ok {
			t.Errorf("no item runs %q; items %v", cmd, keys(got))
			continue
		}
		if it.Path != path || it.Bytes <= 0 || it.Source != "packages" || it.Risk != rules.RiskModerate || it.Explain == "" || it.Identity.Ino == 0 {
			t.Errorf("%s: %+v", cmd, it)
		}
	}
	if _, ok := got["/fake/uv cache clean"]; ok {
		t.Error("uv offered with an empty cache")
	}
	if _, ok := got["/fake/pod cache clean --all"]; ok {
		t.Error("pod offered though it is not installed")
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "pnpm store path") }) {
		t.Errorf("no note says pnpm could not be asked: %q", notes)
	}
	// docker is offered, risky, and never selected.
	d, ok := got[docker+" system prune"]
	if !ok || d.Path != docker || d.Risk != rules.RiskRisky || !NeverSelected(d) || d.Bytes != 0 || !strings.Contains(d.Explain, "stopped container") {
		t.Errorf("docker item %+v", d)
	}
	if NeverSelected(got["/fake/brew cleanup --prune=all -s"]) {
		t.Error("brew is never selected")
	}
	for _, line := range *asked {
		if !strings.HasPrefix(line, e.Home+": ") {
			t.Errorf("discovery ran outside the home folder: %s", line)
		}
	}
}

// /usr/bin/python3 is a stub that opens an install dialog without the
// developer tools, so it is not asked then.
func TestPipNeedsDeveloperTools(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, filepath.Join(e.Home, "Library/Caches/pip/http/x"), "wheel")
	asked := fake(t,
		map[string]string{"python3": "/usr/bin/python3", "xcode-select": "/fake/xcode-select"},
		map[string]string{"/usr/bin/python3 -m pip cache dir": filepath.Join(e.Home, "Library/Caches/pip")},
	)

	items, notes := find(t, e)

	if len(items) != 0 {
		t.Errorf("items %+v", items)
	}
	for _, line := range *asked {
		if strings.Contains(line, "python3") {
			t.Errorf("asked python3 without the developer tools: %s", line)
		}
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "command line tools") }) {
		t.Errorf("notes %q", notes)
	}

	// With the tools, pip is asked and offered.
	fake(t,
		map[string]string{"python3": "/usr/bin/python3", "xcode-select": "/fake/xcode-select"},
		map[string]string{
			"/fake/xcode-select -p":             "/Library/Developer/CommandLineTools",
			"/usr/bin/python3 -m pip cache dir": filepath.Join(e.Home, "Library/Caches/pip"),
		},
	)
	items, _ = find(t, e)
	if _, ok := byCommand(items)["/usr/bin/python3 -m pip cache purge"]; !ok {
		t.Errorf("pip not offered: %+v", items)
	}
}

func TestUnavailableSimulators(t *testing.T) {
	e := env.New(t.TempDir())
	devices := filepath.Join(e.Home, "Library/Developer/CoreSimulator/Devices")
	write(t, filepath.Join(devices, "AAAA-1111/data/app.db"), "old simulator data")
	write(t, filepath.Join(devices, "BBBB-2222/data/app.db"), "a simulator that still works, and is bigger")
	fake(t,
		map[string]string{"xcrun": "/fake/xcrun"},
		map[string]string{
			"/fake/xcrun simctl list devices unavailable -j": `{"devices":{"com.apple.CoreSimulator.SimRuntime.iOS-15-0":[{"udid":"AAAA-1111","name":"iPhone 12"},{"udid":"../escape"}]}}`,
		},
	)

	items, _ := find(t, e)

	it, ok := byCommand(items)["/fake/xcrun simctl delete unavailable"]
	if !ok {
		t.Fatalf("no simulator item: %+v", items)
	}
	if it.Path != devices || it.Bytes <= 0 || it.Risk != rules.RiskModerate || !strings.HasPrefix(it.Explain, "1 simulators") {
		t.Errorf("item %+v", it)
	}
}

func TestNvmKeepsTheDefaultVersion(t *testing.T) {
	installs := []string{"v18.20.1", "v20.10.0", "v20.11.1", "v22.1.0"}
	cases := []struct {
		name    string
		aliases map[string]string // file under ~/.nvm/alias -> contents
		offered []string
		note    string
	}{
		{"major version", map[string]string{"default": "20\n"}, []string{"v18.20.1", "v22.1.0"}, ""},
		{"exact version", map[string]string{"default": "v18.20.1"}, []string{"v20.10.0", "v20.11.1", "v22.1.0"}, ""},
		{"alias chain", map[string]string{"default": "lts/*", "lts/*": "lts/iron", "lts/iron": "v20.11.1"}, []string{"v18.20.1", "v20.10.0", "v22.1.0"}, ""},
		{"newest", map[string]string{"default": "node"}, []string{"v18.20.1", "v20.10.0", "v20.11.1"}, ""},
		{"system", map[string]string{"default": "system"}, installs, ""},
		{"no default", nil, installs, ""},
		{"unknown alias", map[string]string{"default": "lts/argon"}, nil, "offers none"},
		{"not installed", map[string]string{"default": "16"}, nil, "offers none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := env.New(t.TempDir())
			for _, v := range installs {
				write(t, filepath.Join(e.Home, ".nvm/versions/node", v, "bin/node"), "node "+v)
			}
			for name, data := range c.aliases {
				write(t, filepath.Join(e.Home, ".nvm/alias", name), data)
			}
			fake(t, nil, nil)

			items, notes := find(t, e)

			if got := quarantined(items); !slices.Equal(got, c.offered) {
				t.Errorf("offered %v, want %v", got, c.offered)
			}
			for _, it := range items {
				if it.Risk != rules.RiskRisky || it.Source != "packages" || it.Bytes <= 0 ||
					!strings.Contains(it.Explain, "a project may still use it through .nvmrc") {
					t.Errorf("item %+v", it)
				}
			}
			if c.note != "" && !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, c.note) }) {
				t.Errorf("notes %q lack %q", notes, c.note)
			}
		})
	}
}

func TestPyenvKeepsGlobalVersionsAndVirtualenvs(t *testing.T) {
	e := env.New(t.TempDir())
	versions := filepath.Join(e.Home, ".pyenv/versions")
	write(t, filepath.Join(versions, "3.12.1/bin/python"), "py")
	write(t, filepath.Join(versions, "3.11.4/envs/myenv/bin/python"), "venv") // holds the global virtualenv
	write(t, filepath.Join(versions, "3.10.0/envs/other/bin/python"), "venv") // holds another virtualenv
	write(t, filepath.Join(versions, "3.9.1/bin/python"), "py")
	write(t, filepath.Join(versions, "3.8.0/bin/python"), "py")
	if err := os.Symlink(filepath.Join(versions, "3.11.4/envs/myenv"), filepath.Join(versions, "myenv")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(versions, ".DS_Store"), "")
	fake(t, map[string]string{"pyenv": "/fake/pyenv"}, map[string]string{"/fake/pyenv global": "3.12.1\nmyenv"})

	items, notes := find(t, e)

	if got, want := quarantined(items), []string{"3.8.0", "3.9.1"}; !slices.Equal(got, want) {
		t.Errorf("offered %v, want %v", got, want)
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "kept 3.10.0") }) {
		t.Errorf("notes %q", notes)
	}
	for _, it := range items {
		if !strings.Contains(it.Explain, ".python-version") {
			t.Errorf("explain %q", it.Explain)
		}
	}
}

func TestVersionFilesWithoutTheProgram(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, filepath.Join(e.Home, ".pyenv/versions/3.12.1/bin/python"), "py")
	write(t, filepath.Join(e.Home, ".pyenv/versions/3.11.4/bin/python"), "py")
	write(t, filepath.Join(e.Home, ".pyenv/version"), "3.12\n")
	write(t, filepath.Join(e.Home, ".rbenv/versions/3.3.0/bin/ruby"), "rb")
	write(t, filepath.Join(e.Home, ".rbenv/versions/3.2.2/bin/ruby"), "rb")
	// no ~/.rbenv/version: rbenv uses the system Ruby
	fake(t, nil, nil)

	items, _ := find(t, e)

	if got, want := quarantined(items), []string{"3.11.4", "3.2.2", "3.3.0"}; !slices.Equal(got, want) {
		t.Errorf("offered %v, want %v", got, want)
	}
}

func TestPyenvGlobalNotInstalledOffersNothing(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, filepath.Join(e.Home, ".pyenv/versions/3.12.1/bin/python"), "py")
	fake(t, map[string]string{"pyenv": "/fake/pyenv"}, map[string]string{"/fake/pyenv global": "3.13.0"})

	items, notes := find(t, e)

	if len(items) != 0 || len(notes) != 1 || !strings.Contains(notes[0], `"3.13.0"`) {
		t.Errorf("items %+v, notes %q", items, notes)
	}
}

func TestFindCancelled(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, filepath.Join(e.Home, ".npm/_cacache/x"), "npm")
	fake(t, map[string]string{"npm": "/fake/npm"}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := Find(ctx, e); !errors.Is(err, context.Canceled) {
		t.Errorf("err %v, want context.Canceled", err)
	}
}

func keys(m map[string]plan.Item) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
