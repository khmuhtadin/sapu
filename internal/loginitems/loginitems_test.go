package loginitems

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func agent(label, program string, runAtLoad bool) string {
	load := "<false/>"
	if runAtLoad {
		load = "<true/>"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Label</key><string>` + label + `</string>
<key>Program</key><string>` + program + `</string>
<key>RunAtLoad</key>` + load + `
</dict></plist>
`
}

// fakeLaunchd points Dirs at temp folders and Loaded at a fixed answer.
func fakeLaunchd(t *testing.T, loaded ...string) (allUsers, system string) {
	t.Helper()
	allUsers, system = t.TempDir(), t.TempDir()
	oldDirs, oldLoaded := Dirs, Loaded
	Dirs = []Dir{
		{"~/Library/LaunchAgents", ScopeUser},
		{allUsers, ScopeAllUsers},
		{system, ScopeSystem},
	}
	Loaded = func(context.Context) (map[string]bool, error) {
		m := map[string]bool{}
		for _, l := range loaded {
			m[l] = true
		}
		return m, nil
	}
	t.Cleanup(func() { Dirs, Loaded = oldDirs, oldLoaded })
	return allUsers, system
}

func TestParseList(t *testing.T) {
	out := "PID\tStatus\tLabel\n" +
		"-\t0\tcom.apple.SafariHistoryServiceAgent\n" +
		"56920\t0\tapplication.at.studio.AsideBrowser.45006012.75162427\n" +
		"-\t-9\tcom.example.agent\n" +
		"garbage line\n"
	got := ParseList(out)
	for _, l := range []string{"com.apple.SafariHistoryServiceAgent", "application.at.studio.AsideBrowser.45006012.75162427", "com.example.agent"} {
		if !got[l] {
			t.Errorf("%s not loaded", l)
		}
	}
	if len(got) != 3 || got["Label"] {
		t.Errorf("got %v", got)
	}
}

func TestList(t *testing.T) {
	e := env.New(t.TempDir())
	allUsers, system := fakeLaunchd(t, "com.example.active", "com.example.everyone")
	user := filepath.Join(e.Home, "Library", "LaunchAgents")
	program := filepath.Join(e.Home, "bin", "active")
	write(t, program, "#!/bin/sh\n")
	write(t, filepath.Join(user, "com.example.active.plist"), agent("com.example.active", program, true))
	gone := filepath.Join(e.Home, "Applications", "Gone.app", "Contents", "MacOS", "helper")
	write(t, filepath.Join(user, "com.example.gone.plist"), agent("com.example.gone", gone, false))
	write(t, filepath.Join(user, "notes.txt"), "not a plist")
	write(t, filepath.Join(user, "broken.plist"), "this is not a property list")
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "apps", "agent.plist"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(allUsers, "dev.sapu.fixture.agent.plist"), string(fixture))
	write(t, filepath.Join(allUsers, "everyone.plist"), agent("com.example.everyone", "/bin/sh", true))
	write(t, filepath.Join(system, "com.example.daemon.plist"), agent("com.example.daemon", "/bin/sh", true))
	locked := filepath.Join(system, "com.example.locked.plist")
	write(t, locked, agent("com.example.locked", "/bin/sh", true))
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}

	items, err := List(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Item{}
	for _, it := range items {
		byName[it.Name()] = it
	}
	if len(items) != 7 {
		t.Fatalf("listed %d items: %+v", len(items), items)
	}

	if it := byName["com.example.active"]; it.Scope != ScopeUser || it.Program != program || !it.RunAtLoad ||
		it.State != StateLoaded || it.Orphan || it.App != "" {
		t.Errorf("active: %+v", it)
	}
	if it := byName["com.example.gone"]; !it.Orphan || it.State != StateNotLoaded ||
		it.App != filepath.Join(e.Home, "Applications", "Gone.app") {
		t.Errorf("gone: %+v", it)
	}
	if it := byName["broken"]; it.Error == "" || it.Label != "" {
		t.Errorf("broken: %+v", it)
	}
	if it := byName["dev.sapu.fixture.agent"]; it.Scope != ScopeAllUsers || !it.KeepAlive || !it.RunAtLoad ||
		it.Program != "/Applications/Sapu Fixture.app/Contents/MacOS/helper" || it.App != "/Applications/Sapu Fixture.app" ||
		!it.Orphan || it.State != StateNotLoaded {
		t.Errorf("fixture agent: %+v", it)
	}
	if it := byName["com.example.everyone"]; it.State != StateLoaded || it.Orphan {
		t.Errorf("everyone: %+v", it)
	}
	if it := byName["com.example.daemon"]; it.Scope != ScopeSystem || it.State != StateUnknown {
		t.Errorf("daemon: %+v", it)
	}
	if it := byName["com.example.locked"]; it.Error != "cannot read it without an admin account" {
		t.Errorf("locked: %+v", it)
	}
}

func TestListWithoutFolders(t *testing.T) {
	e := env.New(t.TempDir())
	fakeLaunchd(t)
	Dirs = []Dir{{"~/Library/LaunchAgents", ScopeUser}, {filepath.Join(e.Home, "missing"), ScopeSystem}}
	items, err := List(context.Background(), e)
	if err != nil || len(items) != 0 {
		t.Errorf("items %v, err %v", items, err)
	}
}

func TestFind(t *testing.T) {
	items := []Item{
		{Path: "/h/Library/LaunchAgents/com.example.a.plist", Label: "com.example.a"},
		{Path: "/h/Library/LaunchAgents/odd-file-name.plist", Label: "com.example.b"},
		{Path: "/h/Library/LaunchAgents/nolabel.plist"},
	}
	for name, want := range map[string]string{
		"com.example.a": "com.example.a.plist",
		"com.example.b": "odd-file-name.plist",
		"odd-file-name": "odd-file-name.plist",
		"nolabel":       "nolabel.plist",
	} {
		got := Find(items, name)
		if len(got) != 1 || filepath.Base(got[0].Path) != want {
			t.Errorf("Find(%q) = %+v", name, got)
		}
	}
	if got := Find(items, "com.example"); len(got) != 0 {
		t.Errorf("a prefix matched: %+v", got)
	}
}

func TestAppOf(t *testing.T) {
	for program, want := range map[string]string{
		"/Applications/Slack.app/Contents/MacOS/Slack":                           "/Applications/Slack.app",
		"/Applications/Tools/X.app/Contents/Library/LoginItems/H.app/Contents/x": "/Applications/Tools/X.app",
		"/usr/local/bin/thing": "",
		"relative.app/x":       "",
		"":                     "",
	} {
		if got := appOf(program); got != want {
			t.Errorf("appOf(%q) = %q, want %q", program, got, want)
		}
	}
}
