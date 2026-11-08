package protect

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// An allow may only name a dedicated command's source, never a rule id: a
// bundle's protect entry with "allow: source: chrome.cache" would otherwise
// let a cleaner rule's items through.
func TestValidateAllowOnlyReservedSources(t *testing.T) {
	entry := func(source string) Entry {
		return Entry{ID: "x", Paths: []string{"~/x"}, Reason: map[string]string{"en": "x"},
			Allow: []Allow{{Source: source, Paths: []string{"~/x/*"}}}}
	}
	for _, source := range []string{"chrome.cache", "spoof", "user-caches", "purge:node_modules"} {
		if err := validate(entry(source)); err == nil || !strings.Contains(err.Error(), "never a rule id") {
			t.Errorf("allow source %q: err %v", source, err)
		}
	}
	for _, source := range rules.ReservedSources {
		if err := validate(entry(source)); err != nil {
			t.Errorf("allow source %q: %v", source, err)
		}
	}
}

// A rule bundle whose protection list has an allow for a rule id (or any
// other source that is not a dedicated command) stops Load. "spoof" has the
// shape the old check accepted (a command-like name), so only the list of
// dedicated sources refuses it.
func TestExtraAllowForRuleIDStopsLoad(t *testing.T) {
	e := env.New(t.TempDir())
	old := Extra
	t.Cleanup(func() { Extra = old })
	Extra = func(env.Env) (fs.FS, []string, error) {
		return fstest.MapFS{"spoof.yml": {Data: []byte(`- id: spoof
  paths: ["~/Library/Caches/com.example.spoof"]
  reason: {en: "spoof"}
  allow:
    - source: spoof
      paths: ["~/Library/Caches/com.example.spoof/*"]
`)}}, nil, nil
	}
	if _, err := Load(e); err == nil || !strings.Contains(err.Error(), "never a rule id") {
		t.Fatalf("Load: %v", err)
	}
}
