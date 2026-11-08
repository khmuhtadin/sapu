package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The settings file of the person running the tests must never leak in.
func init() {
	if err := os.Unsetenv("SAPU_CONFIG"); err != nil {
		panic(err)
	}
}

// The scheduled clean uses sapu.toml: [quarantine] reaches the engine
// options, and [clean] exclude leaves rules out.
func TestScheduledCleanUsesSettings(t *testing.T) {
	_, e := fake(t)
	plans, opts := withTestRules(t)
	fixtureHome(t, e)
	turnOn(t, e, true, false)
	put(t, filepath.Join(e.Home, ".config", "sapu", "sapu.toml"),
		"[quarantine]\nmax_age = \"3d\"\nmax_size = \"2GB\"\n[clean]\nexclude = [\"logs\"]\n")

	run(t, e, time.Now())

	if len(*opts) != 1 {
		t.Fatalf("Apply called %d times", len(*opts))
	}
	if o := (*opts)[0]; o.MaxAge != 3*24*time.Hour || o.MaxBytes != 2_000_000_000 || o.Permanent || o.RunCommands {
		t.Errorf("Apply options: MaxAge=%v MaxBytes=%d Permanent=%v RunCommands=%v", o.MaxAge, o.MaxBytes, o.Permanent, o.RunCommands)
	}
	for _, it := range (*plans)[0].Items {
		if strings.HasPrefix(it.Source, "logs.") {
			t.Errorf("excluded category logs is in the plan: %s", it.Path)
		}
	}
	if !exists(filepath.Join(e.Home, "Library/Logs/Homebrew/x")) {
		t.Error("an excluded item was moved")
	}
	if exists(filepath.Join(e.Home, "Library/Caches/node-gyp")) {
		t.Error("the safe item that is not excluded was not moved")
	}
}

// A broken settings file stops the scheduled clean before it moves anything.
func TestScheduledCleanStopsOnBadSettings(t *testing.T) {
	_, e := fake(t)
	_, opts := withTestRules(t)
	fixtureHome(t, e)
	turnOn(t, e, true, false)
	put(t, filepath.Join(e.Home, ".config", "sapu", "sapu.toml"), "[quarantine]\nmax_age = \"1h\"\n")

	err := Run(t.Context(), e, time.Now())
	if err == nil || !strings.Contains(err.Error(), "line 2: quarantine.max_age") {
		t.Errorf("Run: err = %v, want the settings error", err)
	}
	if len(*opts) != 0 {
		t.Error("Apply ran with a broken settings file")
	}
	for _, rel := range safeItems {
		if !exists(filepath.Join(e.Home, rel)) {
			t.Errorf("%s was moved", rel)
		}
	}
}
