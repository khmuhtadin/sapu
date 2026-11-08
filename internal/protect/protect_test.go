package protect

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/khmuhtadin/sapu/internal/env"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

func newTestChecker(t *testing.T) (*Checker, env.Env) {
	t.Helper()
	e := env.New(t.TempDir())
	c, err := Load(e)
	if err != nil {
		t.Fatal(err)
	}
	return c, e
}

func coreEntries(t *testing.T) []Entry {
	t.Helper()
	sub, err := fs.Sub(ruledata.FS, "protect")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := loadEntries(sub)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// concrete turns a pattern into a path that matches it.
func concrete(t *testing.T, pattern string) string {
	t.Helper()
	if strings.ContainsAny(pattern, "?[") {
		t.Fatalf("%s: test cannot build a path from ? or [", pattern)
	}
	return strings.ReplaceAll(pattern, "*", "x")
}

func TestEveryEntryBlocksItselfInsideAndAncestor(t *testing.T) {
	c, e := newTestChecker(t)
	for _, entry := range coreEntries(t) {
		for _, p := range entry.Paths {
			path := e.Expand(concrete(t, p))
			want := "protected:" + entry.ID
			// An ancestor is blocked by every entry below it, so only
			// itself and inside must name this entry.
			for name, got := range map[string]string{
				"itself": path,
				"inside": filepath.Join(path, "sub", "file.txt"),
			} {
				blocked, reason := c.Check(got)
				if !blocked || reason != want {
					t.Errorf("%s (%s) %s: Check(%q) = %v, %q; want blocked, %q", entry.ID, p, name, got, blocked, reason, want)
				}
			}
			ancestor := filepath.Dir(path)
			if blocked, reason := c.Check(ancestor); !blocked || !strings.HasPrefix(reason, "protected:") {
				t.Errorf("%s (%s) ancestor: Check(%q) = %v, %q", entry.ID, p, ancestor, blocked, reason)
			}
		}
	}
}

func TestEveryEntryLeavesSiblingsAlone(t *testing.T) {
	c, e := newTestChecker(t)
	for _, entry := range coreEntries(t) {
		for _, p := range entry.Paths {
			sibling := filepath.Join(filepath.Dir(e.Expand(concrete(t, p))), "zz-sapu-unrelated")
			for _, path := range []string{sibling, filepath.Join(sibling, "file.txt")} {
				if blocked, reason := c.Check(path); blocked {
					t.Errorf("%s (%s): Check(%q) blocked as %q", entry.ID, p, path, reason)
				}
			}
		}
	}
}

func TestUnrelatedPathsAreNotBlocked(t *testing.T) {
	c, e := newTestChecker(t)
	for _, p := range []string{
		"~/Pictures/Vacation",
		"~/Pictures/Vacation/img.jpg",
		"~/Library/Caches/com.example.app",
		"~/Library/Caches/com.example.app/data",
		"~/Library/Application Support/Slack/Cache",
		"~/Library/Application Support/Google/Chrome/Default/Cache",
		"~/Library/Application Support/Code/Cache",
		"~/Library/Developer/Xcode/DerivedData",
		"~/.claude-other",
		"~/.cache/pip",
		"~/Projects/app/node_modules",
		"/tmp/whatever",
		"/Library/Caches/x",
	} {
		path := e.Expand(p)
		if blocked, reason := c.Check(path); blocked {
			t.Errorf("Check(%q) blocked as %q", path, reason)
		}
	}
}

func TestGlobEntries(t *testing.T) {
	c, e := newTestChecker(t)
	for path, want := range map[string]bool{
		"~/Pictures":                                       true, // contains the pattern
		"~/Pictures/Family.photoslibrary":                  true,
		"~/Pictures/Family.photoslibrary/x/y":              true,
		"~/Pictures/Vacation":                              false,
		"~/Pictures/photoslibrary":                         false,
		"~/Library/Group Containers/ABC.dev.orbstack/data": true,
		"~/Library/Group Containers/ABC.other":             false,
	} {
		blocked, _ := c.Check(e.Expand(path))
		if blocked != want {
			t.Errorf("Check(%s) = %v, want %v", path, blocked, want)
		}
	}
}

func TestCaseInsensitive(t *testing.T) {
	c, e := newTestChecker(t)
	for _, p := range []string{
		"~/library/application support/jetbrains/clion/x",
		"~/LIBRARY/CACHES/JETBRAINS",
		"~/.CLAUDE/projects",
		"~/Pictures/Family.PhotosLibrary/db",
	} {
		if blocked, _ := c.Check(e.Expand(p)); !blocked {
			t.Errorf("Check(%s) not blocked", p)
		}
	}
}

func TestSymlinkedParentIntoProtectedFolder(t *testing.T) {
	c, e := newTestChecker(t)
	mkdir(t, filepath.Join(e.Home, ".claude", "projects"))
	mkdir(t, filepath.Join(e.Home, "Library", "Caches"))
	link := filepath.Join(e.Home, "Library", "Caches", "link")
	if err := os.Symlink(filepath.Join(e.Home, ".claude"), link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(link, "x"),
		filepath.Join(link, "projects", "deep"),
	} {
		blocked, reason := c.Check(path)
		if !blocked || reason != "protected:claude" {
			t.Errorf("Check(%q) = %v, %q; want blocked, protected:claude", path, blocked, reason)
		}
	}
}

func TestSymlinkedParentIntoSafeFolder(t *testing.T) {
	c, e := newTestChecker(t)
	mkdir(t, filepath.Join(e.Home, "Library", "Caches"))
	mkdir(t, filepath.Join(e.Home, "scratch"))
	link := filepath.Join(e.Home, "Library", "Caches", "link")
	if err := os.Symlink(filepath.Join(e.Home, "scratch"), link); err != nil {
		t.Fatal(err)
	}
	if blocked, reason := c.Check(filepath.Join(link, "x")); blocked {
		t.Errorf("blocked as %q", reason)
	}
}

func TestRealHomeSpelling(t *testing.T) {
	// On macOS t.TempDir() is under /var, which is a symlink to /private/var.
	c, e := newTestChecker(t)
	real, err := filepath.EvalSymlinks(e.Home)
	if err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{e.Home, real} {
		if blocked, _ := c.Check(filepath.Join(home, ".claude", "x")); !blocked {
			t.Errorf("home spelled %s: not blocked", home)
		}
	}
}

func TestRootAndHomeAreBlocked(t *testing.T) {
	c, e := newTestChecker(t)
	for _, path := range []string{"/", e.Home} {
		if blocked, _ := c.Check(path); !blocked {
			t.Errorf("Check(%q) not blocked", path)
		}
	}
}

func TestRelativePathIsBlocked(t *testing.T) {
	c, _ := newTestChecker(t)
	if blocked, _ := c.Check("some/relative/path"); !blocked {
		t.Error("relative path not blocked")
	}
}

func TestWhitelistBlocksOnlyItsEntry(t *testing.T) {
	e := env.New(t.TempDir())
	if err := AddWhitelist(e, "~/.Trash/keep.txt"); err != nil {
		t.Fatal(err)
	}
	if err := AddWhitelist(e, "~/Downloads/*.dmg"); err != nil {
		t.Fatal(err)
	}
	c, err := Load(e)
	if err != nil {
		t.Fatal(err)
	}
	// Mole #163: one whitelisted file in the Trash must not protect the rest.
	for path, want := range map[string]string{
		"~/.Trash/keep.txt":        "whitelisted:~/.Trash/keep.txt",
		"~/.Trash":                 "whitelisted:~/.Trash/keep.txt", // contains the entry
		"~/.Trash/other.txt":       "",
		"~/.Trash/keep.txt.bak":    "",
		"~/.Trash/folder/keep.txt": "",
		"~/Downloads/app.dmg":      "whitelisted:~/Downloads/*.dmg",
		"~/Downloads/app.zip":      "",
		"~/Downloads/sub/app.dmg":  "",
	} {
		blocked, reason := c.Check(e.Expand(path))
		if blocked != (want != "") || reason != want {
			t.Errorf("Check(%s) = %v, %q; want %q", path, blocked, reason, want)
		}
	}
}

func TestWhitelistAddRemoveRead(t *testing.T) {
	e := env.New(t.TempDir())

	lines, err := ReadWhitelist(e)
	if err != nil || len(lines) != 0 {
		t.Fatalf("missing file: got %v, %v", lines, err)
	}
	if err := RemoveWhitelist(e, "~/a"); err != nil {
		t.Fatalf("removing from a missing file: %v", err)
	}
	if _, err := os.Stat(e.WhitelistFile()); err == nil {
		t.Fatal("RemoveWhitelist created the file")
	}

	if err := AddWhitelist(e, "~/a"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(e.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode %v, want 0700", info.Mode().Perm())
	}

	// A hand-edited file with comments, a blank line, no final newline.
	hand := "# my list\n\n~/a\n  # note  \n~/b\n~/c"
	writeFile(t, e.WhitelistFile(), hand)

	if err := AddWhitelist(e, "~/b"); err != nil { // already there
		t.Fatal(err)
	}
	if got := readFile(t, e.WhitelistFile()); got != hand {
		t.Errorf("adding an existing entry changed the file:\n%q", got)
	}

	if err := AddWhitelist(e, "~/d"); err != nil {
		t.Fatal(err)
	}
	wantAdd := "# my list\n\n~/a\n  # note  \n~/b\n~/c\n~/d\n"
	if got := readFile(t, e.WhitelistFile()); got != wantAdd {
		t.Errorf("after add:\n%q\nwant:\n%q", got, wantAdd)
	}

	if err := RemoveWhitelist(e, "~/a"); err != nil {
		t.Fatal(err)
	}
	wantRemove := "# my list\n\n  # note  \n~/b\n~/c\n~/d\n"
	if got := readFile(t, e.WhitelistFile()); got != wantRemove {
		t.Errorf("after remove:\n%q\nwant:\n%q", got, wantRemove)
	}

	if err := RemoveWhitelist(e, "~/missing"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWhitelist(e, "# my list"); err != nil { // comments are not entries
		t.Fatal(err)
	}
	if got := readFile(t, e.WhitelistFile()); got != wantRemove {
		t.Errorf("removing a missing entry changed the file:\n%q", got)
	}

	lines, err = ReadWhitelist(e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, "|") != "~/b|~/c|~/d" {
		t.Errorf("ReadWhitelist = %v", lines)
	}
}

func TestAddWhitelistRejectsBadEntries(t *testing.T) {
	e := env.New(t.TempDir())
	for _, entry := range []string{"", "   ", "relative/path", "# comment", "~/a/**", "~/a[", "~/a\n~/b"} {
		if err := AddWhitelist(e, entry); err == nil {
			t.Errorf("AddWhitelist(%q) succeeded", entry)
		}
	}
	if _, err := os.Stat(e.WhitelistFile()); err == nil {
		t.Error("a rejected entry created the file")
	}
}

func TestLoadRejectsBrokenWhitelist(t *testing.T) {
	e := env.New(t.TempDir())
	writeFile(t, e.WhitelistFile(), "~/ok\nrelative/path\n")
	if _, err := Load(e); err == nil {
		t.Error("Load accepted a relative whitelist entry")
	}
}

func TestLoadEntriesRejectsBadFiles(t *testing.T) {
	good := "- id: a\n  paths: [\"~/a\"]\n  reason: {en: why}\n"
	cases := map[string]string{
		"invalid yaml":   "- id: [oops\n",
		"not a list":     "id: a\n",
		"unknown field":  "- id: a\n  paths: [\"~/a\"]\n  reason: {en: why}\n  extra: 1\n",
		"empty id":       "- id: \"\"\n  paths: [\"~/a\"]\n  reason: {en: why}\n",
		"no paths":       "- id: a\n  reason: {en: why}\n",
		"path without ~": "- id: a\n  paths: [\"/etc/a\"]\n  reason: {en: why}\n",
		"relative path":  "- id: a\n  paths: [\"a/b\"]\n  reason: {en: why}\n",
		"double star":    "- id: a\n  paths: [\"~/a/**\"]\n  reason: {en: why}\n",
		"bad glob":       "- id: a\n  paths: [\"~/a[\"]\n  reason: {en: why}\n",
		"no reason":      "- id: a\n  paths: [\"~/a\"]\n",
		"empty reason":   "- id: a\n  paths: [\"~/a\"]\n  reason: {en: \"\"}\n",
		"only id reason": "- id: a\n  paths: [\"~/a\"]\n  reason: {id: why}\n",
		"duplicate id":   good + good,
	}
	for name, content := range cases {
		fsys := fstest.MapFS{"x.yml": {Data: []byte(content)}}
		if _, err := loadEntries(fsys); err == nil {
			t.Errorf("%s: loadEntries succeeded", name)
		}
	}
	if _, err := loadEntries(fstest.MapFS{"x.yml": {Data: []byte(good)}}); err != nil {
		t.Errorf("good file: %v", err)
	}
	if _, err := loadEntries(fstest.MapFS{"x.txt": {Data: []byte(good)}}); err == nil {
		t.Error("no .yml files: loadEntries succeeded")
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReasonUsesTheUsersLanguageThenEnglish(t *testing.T) {
	entries := []Entry{
		{ID: "docker", Paths: []string{"~/d"}, Reason: map[string]string{"en": "Docker's disks", "id": "Disk Docker"}},
		{ID: "ollama", Paths: []string{"~/o"}, Reason: map[string]string{"en": "AI models"}},
	}
	e := env.New(t.TempDir())
	e.Lang = "id"
	c, err := newChecker(e, entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"docker": "Disk Docker", "ollama": "AI models", "nope": ""} {
		if got := c.Reason(id); got != want {
			t.Errorf("Reason(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestReasonOfEveryBuiltInEntryIsNotEmpty(t *testing.T) {
	c, _ := newTestChecker(t)
	for _, entry := range coreEntries(t) {
		if c.Reason(entry.ID) == "" {
			t.Errorf("entry %s has no reason in words", entry.ID)
		}
	}
}
