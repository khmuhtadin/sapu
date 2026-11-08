package apps

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

// plistXML renders a dictionary of strings as an XML property list.
func plistXML(values map[string]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<plist version=\"1.0\">\n<dict>\n")
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "\t<key>%s</key>\n\t<string>%s</string>\n", html.EscapeString(k), html.EscapeString(values[k]))
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
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

// makeApp builds dir/file with an Info.plist and an executable.
func makeApp(t *testing.T, dir, file, id, name string) string {
	t.Helper()
	p := filepath.Join(dir, file)
	exe := strings.TrimSuffix(file, ".app")
	write(t, filepath.Join(p, "Contents", "Info.plist"), plistXML(map[string]string{
		"CFBundleIdentifier": id, "CFBundleName": name, "CFBundleExecutable": exe, "CFBundleShortVersionString": "1.0",
	}))
	write(t, filepath.Join(p, "Contents", "MacOS", exe), "#!/bin/sh\n")
	return p
}

func setDirs(t *testing.T, dirs ...string) {
	t.Helper()
	old := Dirs
	Dirs = dirs
	t.Cleanup(func() { Dirs = old })
}

func paths(all []App) []string {
	var out []string
	for _, a := range all {
		out = append(out, a.Path)
	}
	slices.Sort(out)
	return out
}

func TestListTopLevelAndOneFolderDeep(t *testing.T) {
	e := env.New(t.TempDir())
	sys := t.TempDir()
	setDirs(t, sys, "~/Applications")

	top := makeApp(t, sys, "Slack.app", "com.tinyspeck.slackmacgap", "Slack")
	inner := makeApp(t, filepath.Join(sys, "Utilities"), "Tool.app", "dev.example.tool", "Tool")
	makeApp(t, filepath.Join(sys, "A", "B"), "TooDeep.app", "dev.example.deep", "Deep")
	mine := makeApp(t, filepath.Join(e.Home, "Applications"), "Mine.app", "dev.example.mine", "Mine")
	write(t, filepath.Join(sys, "NotAnApp.app"), "a file, not a bundle")
	if err := os.Symlink(top, filepath.Join(sys, "Linked.app")); err != nil {
		t.Fatal(err)
	}
	// A bundle without Info.plist is still listed, by its file name.
	if err := os.MkdirAll(filepath.Join(sys, "Bare.app"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := List(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(sys, "Bare.app"), top, inner, mine}
	slices.Sort(want)
	if !slices.Equal(paths(got), want) {
		t.Fatalf("listed %v\nwant %v", paths(got), want)
	}
	for _, a := range got {
		switch a.Path {
		case top:
			if a.ID != "com.tinyspeck.slackmacgap" || a.Name != "Slack" || a.Executable != "Slack" || a.Version != "1.0" {
				t.Errorf("Slack read as %+v", a)
			}
		case filepath.Join(sys, "Bare.app"):
			if a.ID != "" || a.Name != "Bare" {
				t.Errorf("bare bundle read as %+v", a)
			}
		}
	}
}

func TestListReadsBinaryInfoPlist(t *testing.T) {
	e := env.New(t.TempDir())
	sys := t.TempDir()
	setDirs(t, sys)
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "apps", "binary-Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(sys, "Binary Fixture.app", "Contents", "Info.plist"), string(data))
	got, err := List(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "dev.sapu.fixture.binary" || got[0].Name != "Binary Fixture" || got[0].Version != "2.1" {
		t.Fatalf("got %+v", got)
	}
}

func TestFind(t *testing.T) {
	all := []App{
		{Path: "/Applications/Slack.app", ID: "com.tinyspeck.slackmacgap", Name: "Slack"},
		{Path: "/Applications/Visual Studio Code.app", ID: "com.microsoft.VSCode", Name: "Code"},
	}
	for query, want := range map[string]string{
		"Slack":                                "/Applications/Slack.app",
		"slack.app":                            "/Applications/Slack.app",
		"COM.TINYSPECK.SLACKMACGAP":            "/Applications/Slack.app",
		"/Applications/Slack.app/":             "/Applications/Slack.app",
		"Code":                                 "/Applications/Visual Studio Code.app",
		"visual studio code":                   "/Applications/Visual Studio Code.app",
		"/Applications/Visual Studio Code.app": "/Applications/Visual Studio Code.app",
	} {
		got := Find(all, query)
		if len(got) != 1 || got[0].Path != want {
			t.Errorf("Find(%q) = %v, want %s", query, paths(got), want)
		}
	}
	for _, query := range []string{"Slac", "com.tinyspeck", "/Applications/Other.app", "Slack/"} {
		if got := Find(all, query); len(got) != 0 {
			t.Errorf("Find(%q) = %v, want nothing", query, paths(got))
		}
	}
}

func TestIsApple(t *testing.T) {
	for id, want := range map[string]bool{
		"com.apple.Safari": true, "COM.APPLE.dt.Xcode": true,
		"com.applesauce.app": false, "com.tinyspeck.slackmacgap": false, "": false,
	} {
		if IsApple(id) != want {
			t.Errorf("IsApple(%q) = %v", id, !want)
		}
	}
}

func TestLeftoversByBundleID(t *testing.T) {
	e := env.New(t.TempDir())
	lib := filepath.Join(e.Home, "Library")
	id := "com.tinyspeck.slackmacgap"
	match := []string{
		"Application Support/" + id + "/data.json",
		"Caches/" + id + "/Cache.db",
		"Preferences/" + id + ".plist",
		"Preferences/ByHost/" + id + ".0F3A-11AA.plist",
		"Containers/" + id + "/Data/x",
		"Group Containers/BQR82RBBHL." + id + "/x",
		"Group Containers/group." + id + "/x",
		"Saved Application State/" + id + ".savedState/windows.plist",
		"HTTPStorages/" + id + "/httpstorages.sqlite",
		"HTTPStorages/" + id + ".binarycookies",
		"WebKit/" + id + "/WebsiteData/x",
		"Logs/" + id + "/app.log",
		"Cookies/" + id + ".binarycookies",
		"LaunchAgents/" + id + ".plist",
		"LaunchAgents/" + id + ".ShipIt.plist",
		"Application Scripts/" + id + "/x",
	}
	decoys := []string{
		"Caches/" + id + "2/x",                           // another id that starts the same
		"Caches/" + id + ".helper/x",                     // a helper's own id: not this app
		"Preferences/" + id + ".helper.plist",            // ditto
		"Preferences/" + id + "2.plist",                  // another id
		"LaunchAgents/" + id + "2.plist",                 // another id
		"LaunchAgents/" + id + ".plist.bak",              // not a plist
		"Group Containers/a.b." + id + "/x",              // two words before the id
		"Group Containers/" + id + "/x",                  // no team id
		"Application Support/Slack/x",                    // a name match, not asked for
		"Saved Application State/" + id + "/x",           // missing .savedState
		"Cookies/" + id + "/x",                           // a folder, not the cookie file
		"Application Support/x/" + id + "/nested",        // deeper than the top level
		"Containers/com.tinyspeck.slackmacgap-beta/x",    // another id
		"Mail/" + id + "/x",                              // a folder Sapu never looks in
		"Application Scripts/group." + id + "/script.sh", // not a Group Containers folder
	}
	for _, rel := range append(slices.Clone(match), decoys...) {
		write(t, filepath.Join(lib, rel), "x")
	}

	found, unreadable, err := Leftovers(e, id, "")
	if err != nil || len(unreadable) != 0 {
		t.Fatalf("err %v, unreadable %v", err, unreadable)
	}
	var want []string
	for _, rel := range match {
		// The leftover is the top entry in its Library folder.
		folder, rest, _ := strings.Cut(rel, "/")
		if folder == "Preferences" && strings.HasPrefix(rest, "ByHost/") {
			folder, rest = "Preferences/ByHost", strings.TrimPrefix(rest, "ByHost/")
		}
		top, _, _ := strings.Cut(rest, "/")
		want = append(want, filepath.Join(lib, folder, top))
	}
	slices.Sort(want)
	want = slices.Compact(want)
	var got []string
	for _, lo := range found {
		got = append(got, lo.Path)
		if lo.ByName {
			t.Errorf("%s is marked as a name match", lo.Path)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("found:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for _, lo := range found {
		if lo.Path == filepath.Join(lib, "Preferences/ByHost", id+".0F3A-11AA.plist") && lo.Folder != "Preferences/ByHost" {
			t.Errorf("ByHost leftover has folder %q", lo.Folder)
		}
	}
}

// APFS ignores case, so the match does too, and the path keeps the
// spelling on disk: undo then restores the exact name.
func TestLeftoversKeepTheSpellingOnDisk(t *testing.T) {
	e := env.New(t.TempDir())
	write(t, filepath.Join(e.Home, "Library", "Caches", "Dev.Example.App", "x"), "x")
	found, _, err := Leftovers(e, "dev.example.APP", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || filepath.Base(found[0].Path) != "Dev.Example.App" {
		t.Errorf("found %+v", found)
	}
}

func TestLeftoversByName(t *testing.T) {
	e := env.New(t.TempDir())
	lib := filepath.Join(e.Home, "Library")
	for _, rel := range []string{
		"Application Support/Slack/x", "Caches/slack/x", "Logs/Slack/x",
		"Preferences/Slack/x", "Application Support/Slack Helper/x", "Application Support/Slack2/x",
	} {
		write(t, filepath.Join(lib, rel), "x")
	}
	found, _, err := Leftovers(e, "", "Slack")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, lo := range found {
		got = append(got, lo.Path)
		if !lo.ByName {
			t.Errorf("%s is not marked as a name match", lo.Path)
		}
	}
	want := []string{
		filepath.Join(lib, "Application Support/Slack"),
		filepath.Join(lib, "Caches/slack"),
		filepath.Join(lib, "Logs/Slack"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("found %v\nwant %v", got, want)
	}
}

func TestLeftoversSkipsAFolderItCannotRead(t *testing.T) {
	e := env.New(t.TempDir())
	lib := filepath.Join(e.Home, "Library")
	id := "dev.example.app"
	write(t, filepath.Join(lib, "Cookies", id+".binarycookies"), "x")
	write(t, filepath.Join(lib, "Caches", id, "x"), "x")
	cookies := filepath.Join(lib, "Cookies")
	if err := os.Chmod(cookies, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cookies, 0o755) })

	found, unreadable, err := Leftovers(e, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Path != filepath.Join(lib, "Caches", id) {
		t.Errorf("found %+v", found)
	}
	if !slices.Equal(unreadable, []string{cookies}) {
		t.Errorf("unreadable %v", unreadable)
	}
}

func TestMovable(t *testing.T) {
	dir := t.TempDir()
	app := makeApp(t, dir, "Mine.app", "dev.example.mine", "Mine")
	if !Movable(app) {
		t.Fatal("an app the user owns is not movable")
	}
	for _, locked := range []string{app, dir} {
		if err := os.Chmod(locked, 0o555); err != nil {
			t.Fatal(err)
		}
		if Movable(app) {
			t.Errorf("movable while %s is read-only", locked)
		}
		if err := os.Chmod(locked, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidBundleID(t *testing.T) {
	for id, want := range map[string]bool{
		"com.tinyspeck.slackmacgap": true,
		"Slack":                     false,
		"":                          false,
		".hidden.id":                false,
		"a/b.c":                     false,
		"has space.id":              false,
	} {
		if got := ValidBundleID(id); got != want {
			t.Errorf("ValidBundleID(%q) = %v", id, got)
		}
	}
}

// DataLeftovers finds the app's settings and data, and neither its login item
// nor its script folder; name matches are found as before.
func TestDataLeftovers(t *testing.T) {
	e := env.New(t.TempDir())
	lib := filepath.Join(e.Home, "Library")
	id := "com.tinyspeck.slackmacgap"
	data := []string{
		"Application Support/" + id + "/data.json",
		"Caches/" + id + "/Cache.db",
		"Preferences/" + id + ".plist",
		"Preferences/ByHost/" + id + ".0F3A-11AA.plist",
		"Containers/" + id + "/Data/x",
		"Group Containers/BQR82RBBHL." + id + "/x",
		"Saved Application State/" + id + ".savedState/windows.plist",
		"HTTPStorages/" + id + ".binarycookies",
		"WebKit/" + id + "/x",
		"Logs/" + id + "/app.log",
		"Cookies/" + id + ".binarycookies",
	}
	others := []string{
		"LaunchAgents/" + id + ".ShipIt.plist",
		"Application Scripts/" + id + "/x",
	}
	for _, rel := range append(slices.Clone(data), others...) {
		write(t, filepath.Join(lib, rel), "x")
	}
	write(t, filepath.Join(lib, "Application Support/Slack/x"), "x")

	found, _, err := DataLeftovers(e, id, "Slack")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	byName := 0
	for _, lo := range found {
		got = append(got, lo.Path)
		if lo.ByName {
			byName++
			if lo.Path != filepath.Join(lib, "Application Support/Slack") {
				t.Errorf("name match %s", lo.Path)
			}
		}
	}
	if len(found) != len(data)+1 || byName != 1 {
		t.Errorf("found %d (%d by name), want %d and 1:\n  %s", len(found), byName, len(data)+1, strings.Join(got, "\n  "))
	}
	for _, p := range got {
		for _, rel := range others {
			if strings.HasPrefix(p, filepath.Join(lib, rel[:strings.Index(rel, "/")])+"/") {
				t.Errorf("%s is not data", p)
			}
		}
	}
}

func TestOthers(t *testing.T) {
	slack := App{Path: "/A/Slack.app", ID: "com.slack", Name: "Slack"}
	same := App{Path: "/B/Slack Old.app", ID: "COM.SLACK", Name: "Slack Old"}
	namesake := App{Path: "/C/Slack.app", ID: "com.other", Name: "Slack"}
	unrelated := App{Path: "/D/Mail.app", ID: "com.mail", Name: "Mail"}

	sameID, sameName := Others([]App{slack, same, namesake, unrelated}, slack)
	if !slices.Equal(paths(sameID), []string{same.Path}) {
		t.Errorf("same id: %v", paths(sameID))
	}
	if !slices.Equal(paths(sameName), []string{namesake.Path}) {
		t.Errorf("same name: %v", paths(sameName))
	}
}
