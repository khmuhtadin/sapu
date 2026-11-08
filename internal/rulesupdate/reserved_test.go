package rulesupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

// spoofRule is the final review's cleaner rule: the id "backups" gave its
// items the source of sapu backups, and with it the built-in exception that
// lets one iPhone backup through, so a normal clean moved a backup.
const spoofRule = `- id: backups
  category: apps
  paths: ["~/Library/Application Support/MobileSync/Backup/*"]
  risk: safe
  regenerates: true
  explain: {en: "disposable cache"}
  action: quarantine
  fixture: fake
`

func withSpoofRule(b *Bundle) { b.Cleaners["spoof.yml"] = spoofRule }

// A signed bundle with a cleaner rule whose id is a dedicated command's
// source is refused at install. Before the fix it was installed and used.
func TestInstallRefusesReservedRuleID(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())
	data, sig := newBundle(t, key, "2026.12.1", withSpoofRule)
	if _, err := Install(e, data, sig, "1.0.0"); err == nil {
		t.Fatal("installed a bundle with a rule named backups")
	}
	requireNoState(t, e)
	requireEmbedded(t, e, "")
}

// A bundle stored before this check (or put there by hand) with such a rule
// is not used: Active falls back to the built-in rules and says why.
func TestStoredBundleWithReservedRuleIDFallsBack(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())
	data, sig := newBundle(t, key, "2026.12.1", withSpoofRule)
	dir := filepath.Join(e.StateDir, "rules")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"2026.12.1.json": data, "2026.12.1.json.sig": sig, "active": []byte("2026.12.1\n")} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	requireEmbedded(t, e, "reserved")
}

// A bundle's protect allow may not name a rule id, its own rules' included.
// The rule id "bundle-only" has the shape the old check accepted, so only
// the list of dedicated sources refuses the allow.
func TestInstallRefusesAllowForRuleID(t *testing.T) {
	key := trustKey(t)
	e := env.New(t.TempDir())
	data, sig := newBundle(t, key, "2026.12.1", func(b *Bundle) {
		b.Cleaners["own.yml"] = strings.Replace(extraRule, "id: test.bundle-only", "id: bundle-only", 1)
		b.Protect["more.yml"] = "- id: bundle-more\n  paths: [\"~/Library/Precious\"]\n  reason: {en: x}\n" +
			"  allow:\n    - source: bundle-only\n      paths: [\"~/Library/Precious/*\"]\n"
	})
	if _, err := Install(e, data, sig, "1.0.0"); err == nil {
		t.Fatal("installed a bundle whose allow names a rule id")
	}
	requireNoState(t, e)
}
