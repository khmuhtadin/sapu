package protect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

func TestCheckSourceLetsOnlyListedCommandsThrough(t *testing.T) {
	e := env.New(t.TempDir())
	entries := []Entry{
		{ID: "backups", Paths: []string{"~/Backups"}, Reason: map[string]string{"en": "test"},
			Allow: []Allow{{Source: "backups", Paths: []string{"~/Backups/*"}}}},
		{ID: "notes", Paths: []string{"~/Notes"}, Reason: map[string]string{"en": "test"}},
	}
	c, err := newChecker(e, entries, []string{"~/Backups/keep"})
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(e.Home, "Backups", "phone-1")
	cases := []struct {
		path, source string
		blocked      bool
	}{
		{backup, "backups", false},   // the dedicated command
		{backup, "", true},           // Check
		{backup, "large", true},      // another command
		{backup, "purge:node", true}, // purge
		{filepath.Join(e.Home, "Backups", "keep"), "backups", true}, // the whitelist always wins
		{filepath.Join(e.Home, "Notes", "n.db"), "backups", true},   // entries without allow
		{filepath.Join(e.Home, "Backups"), "backups", true},         // the folder itself: fewer segments
		{filepath.Join(backup, "Manifest.db"), "backups", true},     // inside one backup: more segments
		{e.Home, "backups", true},                                   // the folder that holds it all
	}
	for _, tc := range cases {
		if blocked, why := c.CheckSource(tc.path, tc.source); blocked != tc.blocked {
			t.Errorf("CheckSource(%s, %q) = %v (%s), want %v", tc.path, tc.source, blocked, why, tc.blocked)
		}
	}
}

// The built-in list: a hand-edited plan cannot pass Messages off as sapu
// messages, or all of MobileSync as sapu backups. One backup folder of sapu
// backups still passes, unless it is whitelisted.
func TestBuiltInExceptionsAreExact(t *testing.T) {
	e := env.New(t.TempDir())
	backups := filepath.Join(e.Home, "Library/Application Support/MobileSync/Backup")
	one := filepath.Join(backups, "00008110-001A2B3C4D5E")
	if err := os.MkdirAll(one, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.WhitelistFile(), []byte("~/Library/Application Support/MobileSync/Backup/kept-*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(e)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path, source string
		blocked      bool
	}{
		{filepath.Join(e.Home, "Library/Messages"), "messages", true},
		{filepath.Join(e.Home, "Library/Messages/Attachments/ab/11/at_0/IMG_1.heic"), "messages", true},
		{filepath.Join(e.Home, "Library/Messages/chat.db"), "messages", true},
		{filepath.Join(e.Home, "Library/Application Support/MobileSync"), "backups", true},
		{backups, "backups", true},
		{filepath.Join(one, "Manifest.db"), "backups", true},
		{filepath.Join(e.Home, "Library/Application Support"), "backups", true},
		{one, "backups", false},
		{one, "large", true},
		{one, "", true},
		{filepath.Join(backups, "kept-ana"), "backups", true}, // whitelisted
	}
	for _, tc := range cases {
		if blocked, why := c.CheckSource(tc.path, tc.source); blocked != tc.blocked {
			t.Errorf("CheckSource(~%s, %q) = %v (%s), want %v", tc.path[len(e.Home):], tc.source, blocked, why, tc.blocked)
		}
	}
	if blocked, why := c.CheckSource(filepath.Join(backups, "kept-ana"), "backups"); why != "whitelisted:~/Library/Application Support/MobileSync/Backup/kept-*" {
		t.Errorf("whitelisted backup: %v %q", blocked, why)
	}
}

func TestValidateAllow(t *testing.T) {
	for name, a := range map[string]Allow{
		"bad source":    {Source: "Not Valid", Paths: []string{"~/x/*"}},
		"empty source":  {Source: "", Paths: []string{"~/x/*"}},
		"no paths":      {Source: "backups"},
		"absolute path": {Source: "backups", Paths: []string{"/opt/x/y"}},
		"double star":   {Source: "backups", Paths: []string{"~/x/**"}},
		"bad glob":      {Source: "backups", Paths: []string{"~/x/[a"}},
	} {
		bad := Entry{ID: "x", Paths: []string{"~/x"}, Reason: map[string]string{"en": "x"}, Allow: []Allow{a}}
		if err := validate(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := Entry{ID: "x", Paths: []string{"~/x"}, Reason: map[string]string{"en": "x"},
		Allow: []Allow{{Source: "backups", Paths: []string{"~/x/*"}}}}
	if err := validate(good); err != nil {
		t.Errorf("valid allow: %v", err)
	}
}
