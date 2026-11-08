package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/menu"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/review"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// useHome points loadEnv at e for the test.
func useHome(t *testing.T, e env.Env) {
	t.Helper()
	old := loadEnv
	loadEnv = func() (env.Env, error) { return e, nil }
	t.Cleanup(func() { loadEnv = old })
}

// useTools replaces the tool list for the test.
func useTools(t *testing.T, list []tool) {
	t.Helper()
	old := tools
	tools = list
	t.Cleanup(func() { tools = old })
}

// The menu gets every tool, in order, with the same words.
func TestMenuToolsAreTheTools(t *testing.T) {
	useHome(t, env.New(t.TempDir()))
	got, err := menuTools()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(tools) {
		t.Fatalf("%d tools, want %d", len(got), len(tools))
	}
	for i, mt := range got {
		tt := tools[i]
		if mt.ID != tt.ID || mt.Title != tt.Title || mt.Short != tt.Short || mt.Folders != tt.Folders || mt.App != tt.App {
			t.Errorf("tool %d: %+v, want %+v", i, mt, tt)
		}
	}
}

// Default folders show with home as ~; an error reading them shows.
func TestMenuToolsShowDefaultFolders(t *testing.T) {
	e := env.New(t.TempDir())
	useHome(t, e)
	useTools(t, []tool{
		{ID: "purge", Title: "Build folders", Folders: true, Plan: notBuilt,
			DefaultDirs: func(e env.Env) ([]string, error) {
				return []string{filepath.Join(e.Home, "Developer"), filepath.Join(e.Home, "Projects"), "/Volumes/Work"}, nil
			}},
		{ID: "trash", Title: "Empty the Trash", Plan: notBuilt},
	})
	got, err := menuTools()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"~/Developer", "~/Projects", "/Volumes/Work"}; !reflect.DeepEqual(got[0].Dirs, want) {
		t.Errorf("dirs %q, want %q", got[0].Dirs, want)
	}
	if got[1].Dirs != nil {
		t.Errorf("a tool without folders has dirs %q", got[1].Dirs)
	}

	useTools(t, []tool{{ID: "large", Title: "Large old files", Folders: true, Plan: notBuilt,
		DefaultDirs: func(env.Env) ([]string, error) { return nil, errors.New("no home") }}})
	if _, err := menuTools(); err == nil || !strings.Contains(err.Error(), "no home") {
		t.Errorf("want the error, got %v", err)
	}
}

func TestMenuFolder(t *testing.T) {
	e := env.New(t.TempDir())
	useHome(t, e)
	writeFile(t, filepath.Join(e.Home, "Code", "app", "main.go"), "package main")
	writeFile(t, filepath.Join(e.Home, "notes.txt"), "a file")
	for _, c := range []struct {
		typed, want, err string
	}{
		{"~/Code", filepath.Join(e.Home, "Code"), ""},
		{"~", e.Home, ""},
		{filepath.Join(e.Home, "Code", "app"), filepath.Join(e.Home, "Code", "app"), ""},
		{"~/Code/../Code", filepath.Join(e.Home, "Code"), ""},
		{"~/Nope", "", "it does not exist"},
		{"~/notes.txt", "", "it is a file, not a folder"},
	} {
		got, err := menuFolder(c.typed)
		if got != c.want || (err == nil) != (c.err == "") || (err != nil && err.Error() != c.err) {
			t.Errorf("%q: %q %v, want %q %q", c.typed, got, err, c.want, c.err)
		}
	}
}

// The app list leaves out Apple's apps, as uninstall refuses them, and is
// sorted by name.
func TestMenuApps(t *testing.T) {
	e := env.New(t.TempDir())
	useHome(t, e)
	sys := filepath.Join(t.TempDir(), "Applications")
	pointAppDirs(t, sys, "~/Applications")
	slack := fakeAppBundle(t, sys, "Slack.app", slackID, "Slack")
	fakeAppBundle(t, sys, "Safari.app", "com.apple.Safari", "Safari")
	docker := fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Docker.app", "com.docker.docker", "docker")
	got, err := menuApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []menu.App{{Name: "docker", Path: docker}, {Name: "Slack", Path: slack}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("apps %+v, want %+v", got, want)
	}
}

// RunTool builds the tool's plan from the id, folders and app the menu
// picked, and names the screen after the tool.
func TestToolScreen(t *testing.T) {
	e := env.New(t.TempDir())
	type call struct {
		home string
		dirs []string
		app  string
	}
	var calls []call
	planOf := func(e env.Env, dirs []string, app string) review.PlanFunc {
		calls = append(calls, call{e.Home, dirs, app})
		return func(context.Context, func(walk.Event)) (*plan.Plan, error) { return nil, errors.New("only a test") }
	}
	useTools(t, []tool{
		{ID: "uninstall", Title: "Uninstall an app", App: true, Plan: planOf},
		{ID: "purge", Title: "Build folders", Folders: true, Plan: planOf},
		{ID: "backups", Title: "iPhone and iPad backups", Plan: planOf},
	})
	for _, c := range []struct {
		id    string
		dirs  []string
		app   string
		title string
	}{
		{"purge", nil, "", "build folders"},
		{"purge", []string{"/tmp/work"}, "", "build folders"},
		{"uninstall", nil, "/Applications/Slack Beta.app", "uninstall Slack Beta"},
		{"backups", nil, "", "iPhone and iPad backups"},
	} {
		calls = nil
		title, planFn, err := toolScreen(e, c.id, c.dirs, c.app)
		if err != nil || title != c.title || planFn == nil {
			t.Errorf("%s: %q %v, want %q", c.id, title, err, c.title)
		}
		if want := []call{{e.Home, c.dirs, c.app}}; !reflect.DeepEqual(calls, want) {
			t.Errorf("%s: Plan got %+v, want %+v", c.id, calls, want)
		}
	}
	if _, _, err := toolScreen(e, "nope", nil, ""); err == nil {
		t.Error("an unknown tool should be an error")
	}
}

// Picking a tool in the menu changes nothing on disk: the menu only lists,
// checks a folder and reads the apps.
func TestToolScreensChangeNothing(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	e := newHome(t)
	useHome(t, e)
	writeFile(t, filepath.Join(e.Home, "Projects", "shop", "node_modules", "x.js"), "x")
	pointAppDirs(t, "~/Applications")
	fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Slack.app", slackID, "Slack")
	before := snapshot(t, e)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	backend := menuBackend(cmd)
	ran := 0
	backend.RunTool = func(context.Context, string, []string, string) error { ran++; return nil }
	for _, keys := range [][]string{
		{"3", "2", "f", "~/Projects", "enter", "esc", "esc"},
		{"3", "2", "f", "~/Nope", "enter", "esc", "esc", "esc"},
		{"3", "1", "sl", "esc", "esc", "esc"},
	} {
		m := menu.New(context.Background(), backend, false)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
		if _, quit := driveMenu(t, m, keys...); quit {
			t.Errorf("%v left the menu", keys)
		}
	}
	m := menu.New(context.Background(), backend, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	view, _ := driveMenu(t, m, "3", "1")
	if !strings.Contains(view, "Slack") {
		t.Errorf("the app picker should list Slack:\n%s", view)
	}
	view, _ = driveMenu(t, m, "esc", "2", "f", "~/Nope", "enter")
	if !strings.Contains(view, "Cannot use ~/Nope: it does not exist.") {
		t.Errorf("a missing folder should show why:\n%s", view)
	}
	if ran != 0 {
		t.Errorf("the menu ran a tool %d times", ran)
	}
	sameSnapshot(t, before, snapshot(t, e))
	if _, err := os.Stat(e.StateDir); err == nil {
		t.Error("the menu wrote Sapu's state folder")
	}
}

// notBuilt is a Plan for fake tools whose plan the test never runs.
func notBuilt(env.Env, []string, string) review.PlanFunc {
	return func(context.Context, func(walk.Event)) (*plan.Plan, error) {
		return nil, errors.New("not built")
	}
}
