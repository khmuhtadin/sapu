package protect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

// A protected folder or whitelist entry that is a symlink, or sits behind
// one, was protected only under the link's name. The folder it leads to could
// be cleaned under its real name: with ~/.claude -> ~/cache/sessions, a rule
// for ~/cache/** would take the Claude sessions. (Found by an independent
// review, Nov 2026.)
func TestProtectionFollowsSymlinkedEntries(t *testing.T) {
	type check struct {
		path   string
		reason string // "" means not blocked
	}
	cases := []struct {
		name      string
		links     map[string]string // link -> target, both relative to the home
		entries   []Entry           // nil: the embedded protection list
		whitelist []string
		checks    []check
	}{
		{
			name:  "protected folder is a symlink",
			links: map[string]string{".claude": "cache/sessions"},
			checks: []check{
				{"cache/sessions", "protected:claude"},
				{"cache/sessions/s.jsonl", "protected:claude"},
				{"cache", "protected:claude"},
				{"cache/other", ""},
			},
		},
		{
			name:      "whitelist entry is a symlink",
			links:     map[string]string{"keep": "Library/Caches/thing"},
			whitelist: []string{"~/keep"},
			checks: []check{
				{"Library/Caches/thing", "whitelisted:~/keep"},
				{"Library/Caches/thing/x.db", "whitelisted:~/keep"},
				{"Library/Caches/other", ""},
			},
		},
		{
			name:      "glob whose literal part is a symlink",
			links:     map[string]string{"apps": "Library/Caches/apps", "wl": "Library/Caches/wl"},
			entries:   []Entry{{ID: "test", Paths: []string{"~/apps/*/data"}, Reason: map[string]string{"en": "test"}}},
			whitelist: []string{"~/wl/*.txt"},
			checks: []check{
				{"Library/Caches/apps/x/data", "protected:test"},
				{"Library/Caches/apps/x/data/f", "protected:test"},
				{"Library/Caches/apps/x/cache", ""},
				{"Library/Caches/wl/a.txt", "whitelisted:~/wl/*.txt"},
				{"Library/Caches/wl/a.bin", ""},
			},
		},
		{
			// Found by the second independent review (Nov 2026).
			name:      "wildcard matches a symlink further down",
			links:     map[string]string{"keep/a/data": "cache/only-copy"},
			whitelist: []string{"~/keep/*/data"},
			checks: []check{
				{"cache/only-copy", "whitelisted:~/keep/*/data"},
				{"cache/only-copy/f", "whitelisted:~/keep/*/data"},
				{"cache/other", ""},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := env.New(t.TempDir())
			for link, target := range tc.links {
				mkdir(t, filepath.Join(e.Home, target))
				mkdir(t, filepath.Dir(filepath.Join(e.Home, link)))
				if err := os.Symlink(filepath.Join(e.Home, target), filepath.Join(e.Home, link)); err != nil {
					t.Fatal(err)
				}
			}
			entries := tc.entries
			if entries == nil {
				entries = coreEntries(t)
			}
			c, err := newChecker(e, entries, tc.whitelist)
			if err != nil {
				t.Fatal(err)
			}
			for _, ch := range tc.checks {
				path := filepath.Join(e.Home, ch.path)
				mkdir(t, filepath.Dir(path)) // a real item exists when it is checked
				blocked, reason := c.Check(path)
				if blocked != (ch.reason != "") || reason != ch.reason {
					t.Errorf("Check(~/%s) = %v, %q; want %q", ch.path, blocked, reason, ch.reason)
				}
			}
		})
	}
}

// A symlink prefix that resolves to itself or does not exist adds nothing.
func TestResolvePrefix(t *testing.T) {
	home := t.TempDir()
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(real, "target", "x"))
	if err := os.Symlink(filepath.Join(real, "target"), filepath.Join(real, "link")); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		filepath.Join(real, "link"):           filepath.Join(real, "target"),
		filepath.Join(real, "link", "*", "y"): filepath.Join(real, "target", "*", "y"),
		filepath.Join(real, "link", "x"):      filepath.Join(real, "target", "x"),
		filepath.Join(real, "*", "link"):      "",
		filepath.Join(real, "target"):         "",
		filepath.Join(real, "missing", "x"):   "",
		"/*":                                  "",
	}
	for pattern, want := range cases {
		got, ok := resolvePrefix(pattern)
		if ok != (want != "") || got != want {
			t.Errorf("resolvePrefix(%s) = %q, %v; want %q", pattern, got, ok, want)
		}
	}
}
