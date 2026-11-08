package menu

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// oneLine joins the view's words, so a check does not depend on wrapping.
func oneLine(view string) string { return strings.Join(strings.Fields(view), " ") }

func TestToolsListsEveryTool(t *testing.T) {
	m := newTestModel(newFake(), 80, 24, false)
	press(m, "3")
	if m.screen != screenTools {
		t.Fatalf("3 should open the tools, screen %d", m.screen)
	}
	view := oneLine(m.View())
	for _, want := range []string{"sapu / tools", "> 1 Uninstall an app", "Remove an app and the files it left behind",
		"2 Build folders", "Nothing moves until you confirm there", "esc back"} {
		if !strings.Contains(view, want) {
			t.Errorf("tools screen lacks %q:\n%s", want, m.View())
		}
	}
	t.Logf("tools at 80x24:\n%s", m.View())

	// Moving to the last tool scrolls it into view; 0 opens the tenth.
	for range 20 {
		press(m, "down")
	}
	if m.toolCursor != len(fakeTools)-1 || !strings.Contains(m.View(), "> 0  Login items") || !strings.Contains(m.View(), "Stop things from starting at login") {
		t.Errorf("down to the end: cursor %d\n%s", m.toolCursor, m.View())
	}
	press(m, "up")
	if m.toolCursor != len(fakeTools)-2 {
		t.Errorf("up: cursor %d", m.toolCursor)
	}
	press(m, "esc")
	if m.screen != screenMenu || m.cursor != 2 {
		t.Errorf("esc should go back to the menu at Tools: screen %d cursor %d", m.screen, m.cursor)
	}
}

// A tool without anything to choose leaves the menu for its review screen
// at once; 0 is the tenth tool.
func TestToolWithNothingToChooseRunsAtOnce(t *testing.T) {
	for _, c := range []struct {
		keys []string
		id   string
	}{
		{[]string{"3", "8"}, "trash"},
		{[]string{"3", "9"}, "backups"},
		{[]string{"3", "0"}, "login-items"},
		{[]string{"3", "down", "down", "down", "down", "down", "down", "down", "enter"}, "trash"},
	} {
		m := newTestModel(newFake(), 80, 24, false)
		if !press(m, c.keys...) {
			t.Errorf("%v should leave the menu", c.keys)
		}
		if m.launch != launchTool || !reflect.DeepEqual(m.run, toolRun{id: c.id}) {
			t.Errorf("%v: launch %d run %+v, want %s with the default folders", c.keys, m.launch, m.run, c.id)
		}
	}
}

func TestFolderToolShowsWhereItLooks(t *testing.T) {
	m := newTestModel(newFake(), 80, 24, false)
	press(m, "3", "2")
	if m.screen != screenFolders {
		t.Fatalf("a folder tool should show where it looks, screen %d", m.screen)
	}
	view := oneLine(m.View())
	for _, want := range []string{"sapu / build folders", "Find build folders in your projects", "Looks in: ~/Developer, ~/Projects",
		"enter look", "f change folder", "esc back"} {
		if !strings.Contains(view, want) {
			t.Errorf("folder screen lacks %q:\n%s", want, m.View())
		}
	}
	t.Logf("folder tool at 80x24:\n%s", m.View())

	m = newTestModel(newFake(), 80, 24, false)
	if !press(m, "3", "2", "enter") || m.launch != launchTool || !reflect.DeepEqual(m.run, toolRun{id: "purge"}) {
		t.Errorf("enter should run purge in its default folders: launch %d run %+v", m.launch, m.run)
	}
}

func TestFolderInput(t *testing.T) {
	f := newFake()
	m := newTestModel(f, 80, 24, false)
	press(m, "3", "2", "f")
	if !m.editing || !strings.Contains(m.View(), "Folder: _") {
		t.Fatalf("f should open the folder input:\n%s", m.View())
	}

	// A path that is not there: the reason shows, the input stays.
	press(m, "~", "/", "N", "o", "p", "e", "enter")
	if !m.editing || !strings.Contains(oneLine(m.View()), "Cannot use ~/Nope: it does not exist.") {
		t.Errorf("a missing folder should show why:\n%s", m.View())
	}
	t.Logf("folder input with an error at 80x24:\n%s", m.View())
	// A file is not a folder.
	press(m, "ctrl+u")
	m.Update(pasteKey("/etc/hosts"))
	press(m, "enter")
	if !m.editing || !strings.Contains(oneLine(m.View()), "Cannot use /etc/hosts: it is a file, not a folder.") {
		t.Errorf("a file should be refused:\n%s", m.View())
	}

	// ~ is handed to Folder, which expands it; the folder replaces the defaults.
	press(m, "ctrl+u")
	for _, r := range "~/Cod" {
		press(m, string(r))
	}
	press(m, "x", "backspace", "e", "enter")
	if m.editing || m.folderPath != "/nonexistent/home/Code" || !strings.Contains(m.View(), "Looks in: ~/Code") || strings.Contains(m.View(), "~/Projects") {
		t.Errorf("~/Code should replace the defaults: %q\n%s", m.folderPath, m.View())
	}
	if want := []string{"~/Nope", "/etc/hosts", "~/Code"}; !reflect.DeepEqual(f.folders, want) {
		t.Errorf("Folder got %q, want %q", f.folders, want)
	}
	if !press(m, "enter") || !reflect.DeepEqual(m.run, toolRun{id: "purge", dirs: []string{"/nonexistent/home/Code"}}) {
		t.Errorf("enter should run purge in ~/Code: %+v", m.run)
	}
	if len(f.ran) != 0 {
		t.Error("the model must leave running the tool to Run")
	}
}

func TestFolderInputEscKeepsTheDefaults(t *testing.T) {
	m := newTestModel(newFake(), 80, 24, false)
	press(m, "3", "2", "f", "~", "/", "C", "o", "d", "e", "esc")
	if m.editing || m.folderPath != "" || !strings.Contains(m.View(), "Looks in: ~/Developer, ~/Projects") {
		t.Errorf("esc in the input should keep the defaults:\n%s", m.View())
	}
	// An empty input asks for a path.
	press(m, "f", "enter")
	if !m.editing || !strings.Contains(oneLine(m.View()), "Type the path of a folder") {
		t.Errorf("an empty path should ask for one:\n%s", m.View())
	}
	// q and the other letters are text while typing, not keys.
	press(m, "q")
	if m.screen != screenFolders || m.input != "q" {
		t.Errorf("q should be typed: screen %d input %q", m.screen, m.input)
	}
}

func TestFolderToolWithoutDefaults(t *testing.T) {
	m := newTestModel(newFake(), 80, 24, false)
	press(m, "3", "4")
	if !strings.Contains(m.View(), "sapu / duplicate files") || !strings.Contains(m.View(), "Looks in: its default folders") {
		t.Errorf("a tool without listed folders:\n%s", m.View())
	}
}

func TestAppPicker(t *testing.T) {
	m := newTestModel(newFake(), 80, 24, false)
	press(m, "3", "1")
	if m.screen != screenApps {
		t.Fatalf("uninstall should open the app picker, screen %d", m.screen)
	}
	view := oneLine(m.View())
	for _, want := range []string{"sapu / uninstall an app", "Pick the app to remove", "Type to filter.",
		"> Docker", "Slack /Applications/Slack.app", "Zoom", "enter pick", "esc back"} {
		if !strings.Contains(view, want) {
			t.Errorf("app picker lacks %q:\n%s", want, m.View())
		}
	}
	t.Logf("app picker at 80x24:\n%s", m.View())

	// Typing filters by name or path; letters such as q and j are text.
	press(m, "s", "l", "a")
	view = m.View()
	if !strings.Contains(view, "Filter: sla_") || strings.Contains(view, "Docker") || !strings.Contains(view, "> Slack ") || !strings.Contains(view, "Slack Beta") {
		t.Errorf("filter sla:\n%s", view)
	}
	t.Logf("app picker filtered at 80x24:\n%s", view)
	press(m, "down", "down", "down")
	if m.sub != 1 {
		t.Errorf("down stops at the last match: %d", m.sub)
	}
	if !press(m, "enter") || m.launch != launchTool || !reflect.DeepEqual(m.run, toolRun{id: "uninstall", app: "/nonexistent/home/Applications/Slack Beta.app"}) {
		t.Errorf("enter should uninstall the app under the cursor: %+v", m.run)
	}

	m = newTestModel(newFake(), 80, 24, false)
	press(m, "3", "1", "z", "o", "o", "m", "q")
	if m.screen != screenApps || !strings.Contains(m.View(), "No app matches the filter") {
		t.Errorf("q is part of the filter:\n%s", m.View())
	}
	press(m, "backspace")
	if !strings.Contains(m.View(), "> Zoom") {
		t.Errorf("backspace should drop the q:\n%s", m.View())
	}
	// esc clears the filter, then goes back to the tools.
	press(m, "esc")
	if m.screen != screenApps || m.appFilter != "" || !strings.Contains(m.View(), "Docker") {
		t.Errorf("esc should clear the filter first:\n%s", m.View())
	}
	press(m, "esc")
	if m.screen != screenTools || m.toolCursor != 0 {
		t.Errorf("esc should go back to the tools: screen %d", m.screen)
	}
}

func TestAppPickerWithNothingToPick(t *testing.T) {
	f := newFake()
	b := f.backend()
	b.Apps = func(ctx context.Context) ([]App, error) { return nil, nil }
	m := newModel(context.Background(), b, false)
	press(m, "3", "1", "enter")
	if m.launch != launchNone || !strings.Contains(m.View(), "No apps that Sapu can remove") {
		t.Errorf("no apps: launch %d\n%s", m.launch, m.View())
	}
	b.Apps = func(ctx context.Context) ([]App, error) { return nil, errors.New("no access") }
	m = newModel(context.Background(), b, false)
	press(m, "3", "1", "enter")
	if m.launch != launchNone || !strings.Contains(m.View(), "Could not read the installed apps: no access") {
		t.Errorf("an error: launch %d\n%s", m.launch, m.View())
	}
	// Enter while the list loads picks nothing.
	m = newModel(context.Background(), f.backend(), false)
	m.Update(key("3"))
	m.Update(key("1")) // the list is still on its way
	if _, cmd := m.Update(key("enter")); cmd != nil || m.launch != launchNone {
		t.Error("enter before the apps arrive should pick nothing")
	}
}

func TestToolsError(t *testing.T) {
	b := newFake().backend()
	b.Tools = func() ([]Tool, error) { return nil, errors.New("no home") }
	m := newModel(context.Background(), b, false)
	press(m, "3", "enter", "1")
	if m.launch != launchNone || !strings.Contains(m.View(), "Could not read the tools: no home") {
		t.Errorf("tools error: launch %d\n%s", m.launch, m.View())
	}
	press(m, "esc")
	if m.screen != screenMenu {
		t.Error("esc should go back")
	}
}

// esc and q on every tool screen go back and never run a tool.
func TestEscNeverRunsATool(t *testing.T) {
	for _, keys := range [][]string{
		{"3", "esc"},
		{"3", "q"},
		{"3", "2", "esc", "esc"},
		{"3", "2", "q", "esc"},
		{"3", "2", "f", "~", "/", "C", "o", "d", "e", "enter", "esc", "esc"},
		{"3", "2", "f", "/", "n", "o", "enter", "esc", "esc", "esc"},
		{"3", "1", "esc", "esc"},
		{"3", "1", "s", "l", "esc", "esc", "esc"},
		{"3", "1", "down", "left", "h", "esc", "esc", "esc"},
	} {
		f := newFake()
		m := newTestModel(f, 80, 24, false)
		if press(m, keys...) || m.launch != launchNone || len(f.ran) != 0 {
			t.Errorf("%v ran something: launch %d %+v", keys, m.launch, m.run)
		}
		if m.screen != screenMenu || m.cursor != 2 {
			t.Errorf("%v should end on the menu at Tools: screen %d cursor %d", keys, m.screen, m.cursor)
		}
	}
}

// After the menu closes for a tool, Run hands it to RunTool and opens the
// menu again at Tools.
func TestRunHandsTheToolToRunTool(t *testing.T) {
	f := newFake()
	b := f.backend()
	m := newTestModel(f, 80, 24, false)
	press(m, "3", "2", "f", "~", "/", "C", "o", "d", "e", "enter", "enter")
	again, err := runLaunched(context.Background(), b, m)
	if !again || err != nil {
		t.Fatalf("again %v err %v", again, err)
	}
	if want := []toolRun{{id: "purge", dirs: []string{"/nonexistent/home/Code"}}}; !reflect.DeepEqual(f.ran, want) {
		t.Errorf("RunTool got %+v, want %+v", f.ran, want)
	}
	if m.cursor != 2 {
		t.Errorf("the menu cursor should stay on Tools: %d", m.cursor)
	}

	f = newFake()
	b = f.backend()
	m = newTestModel(f, 80, 24, false)
	press(m, "3", "1", "z", "enter")
	if _, err := runLaunched(context.Background(), b, m); err != nil {
		t.Fatal(err)
	}
	if want := []toolRun{{id: "uninstall", app: "/Applications/zoom.us.app"}}; !reflect.DeepEqual(f.ran, want) {
		t.Errorf("RunTool got %+v, want %+v", f.ran, want)
	}

	// q on the menu runs nothing and ends Run.
	f = newFake()
	m = newTestModel(f, 80, 24, false)
	press(m, "3", "esc", "q")
	if again, err := runLaunched(context.Background(), f.backend(), m); again || err != nil || len(f.ran) != 0 {
		t.Errorf("q: again %v err %v ran %+v", again, err, f.ran)
	}
}
