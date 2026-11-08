package rulesupdate

import (
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
)

// The protection files of a bundle are checked with the new allow shape: a
// malformed allow, or the old allow_sources list, refuses the whole update.
// A well-formed allow is accepted; it can only open its own entry's match
// (see package protect and test/regression TestHostileRuleUpdateKeepsEveryProtection).
func TestBundleAllowIsChecked(t *testing.T) {
	entry := func(allow string) func(b *Bundle) {
		return func(b *Bundle) {
			b.Protect["more.yml"] = "- id: bundle-more\n  paths: [\"~/Library/Precious\"]\n  reason: {en: x}\n" + allow
		}
	}
	bad := map[string]string{
		"old allow_sources": "  allow_sources: [backups]\n",
		"no source":         "  allow:\n    - paths: [\"~/Library/Precious/*\"]\n",
		"bad source":        "  allow:\n    - source: Not Valid\n      paths: [\"~/Library/Precious/*\"]\n",
		"no paths":          "  allow:\n    - source: backups\n",
		"absolute path":     "  allow:\n    - source: backups\n      paths: [\"/opt/x/Library/Precious/a\"]\n",
		"double star":       "  allow:\n    - source: backups\n      paths: [\"~/Library/Precious/**\"]\n",
		"unknown field":     "  allow:\n    - source: backups\n      paths: [\"~/Library/Precious/*\"]\n      everything: true\n",
	}
	for name, allow := range bad {
		t.Run(name, func(t *testing.T) {
			key := trustKey(t)
			e := env.New(t.TempDir())
			data, sig := newBundle(t, key, "2026.12.1", entry(allow))
			if _, err := Install(e, data, sig, "1.0.0"); err == nil {
				t.Fatal("installed")
			}
			requireNoState(t, e)
		})
	}

	key := trustKey(t)
	e := env.New(t.TempDir())
	data, sig := newBundle(t, key, "2026.12.1", entry("  allow:\n    - source: backups\n      paths: [\"~/Library/Precious/*\"]\n"))
	if _, err := Install(e, data, sig, "1.0.0"); err != nil {
		t.Fatalf("a valid allow: %v", err)
	}
}
