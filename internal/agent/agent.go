// Package agent is Sapu's background helper: one LaunchAgent that runs
// "sapu agent run" every hour for alerts (O2) and scheduled cleanup (O3).
//
// Files:
//
//	~/.sapu/agent.json        what is on (Config), written by sapu schedule/alerts
//	~/.sapu/agent-state.json  free-space samples, last notifications, last clean (State)
//	~/.sapu/agent.log         one line per step of every run
//	~/Library/LaunchAgents/dev.sapu.agent.plist   the LaunchAgent, written by Install
//
// Rules (each one has a test):
//  1. Run does nothing at all when another sapu holds fsop.Lock; it holds the
//     lock for the whole run otherwise.
//  2. The scheduled clean uses only rules that are risk safe with action
//     quarantine, selects only safe quarantine items, and applies with
//     engine.DefaultOptions() and the [quarantine] settings of sapu.toml:
//     never Permanent, never RunCommands.
//  3. An alert of one kind is shown at most once per 24 hours.
//  4. Install bootstraps the agent (booting out the loaded copy first);
//     Uninstall boots it out and removes the plist only through
//     fsop.RemoveOwnLaunchAgent. sapu schedule/alerts on install; off
//     uninstalls only when both are off.
//  5. launchctl, osascript, free space and the path of the running binary go
//     through package variables, so tests never touch launchd or show a
//     notification.
//
// Files are written in place (O_TRUNC, never a rename: only fsop moves files)
// and never through a symlink.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/safefile"
)

// Label is the launchd label of Sapu's only LaunchAgent.
const Label = fsop.AgentLabelPrefix + "agent"

// Interval is how often launchd runs the agent.
const Interval = time.Hour

// How often the scheduled clean runs.
const (
	EveryDaily  = "daily"
	EveryWeekly = "weekly"
)

// Default alert thresholds, base 1000 as Sapu prints sizes.
const (
	DefaultMinFree = 10_000_000_000 // 10 GB
	DefaultDrop    = 5_000_000_000  // 5 GB a day
)

// Config is ~/.sapu/agent.json.
type Config struct {
	Schedule Schedule `json:"schedule"`
	Alerts   Alerts   `json:"alerts"`
}

type Schedule struct {
	Enabled bool   `json:"enabled"`
	Every   string `json:"every"` // EveryDaily or EveryWeekly
}

type Alerts struct {
	Enabled         bool  `json:"enabled"`
	MinFreeBytes    int64 `json:"min_free_bytes"`     // alert when free space is below this; 0 = never
	DropBytesPerDay int64 `json:"drop_bytes_per_day"` // alert when free space fell more than this in 24 h; 0 = never
}

// State is ~/.sapu/agent-state.json. Only Run writes it.
type State struct {
	LastCheck *time.Time           `json:"last_check,omitempty"` // last agent run
	Samples   []Sample             `json:"samples"`              // free space, about 8 days of hourly samples
	Notified  map[string]time.Time `json:"notified,omitempty"`   // alert kind -> last notification
	LastClean *Clean               `json:"last_clean,omitempty"`
}

type Sample struct {
	Time      time.Time `json:"time"`
	FreeBytes int64     `json:"free_bytes"`
}

// Clean is the outcome of the last scheduled clean.
type Clean struct {
	Time   time.Time `json:"time"`
	Result string    `json:"result"`           // CleanMoved, CleanNothing or CleanFailed
	RunID  string    `json:"run_id,omitempty"` // the run to undo
	Items  int       `json:"items"`            // items moved to quarantine
	Bytes  int64     `json:"bytes"`            // bytes moved to quarantine
	Error  string    `json:"error,omitempty"`  // CleanFailed only
}

// Results of a scheduled clean.
const (
	CleanMoved   = "moved"
	CleanNothing = "nothing"
	CleanFailed  = "failed"
)

// Alert kinds, the keys of State.Notified.
const (
	AlertLow    = "low"    // free space below Alerts.MinFreeBytes
	AlertDrop   = "drop"   // free space fell more than Alerts.DropBytesPerDay in 24 h
	AlertFailed = "failed" // a run failed
)

func ConfigPath(e env.Env) string { return filepath.Join(e.StateDir, "agent.json") }
func StatePath(e env.Env) string  { return filepath.Join(e.StateDir, "agent-state.json") }
func LogPath(e env.Env) string    { return filepath.Join(e.StateDir, "agent.log") }
func PlistPath(e env.Env) string {
	return filepath.Join(e.Home, "Library", "LaunchAgents", Label+".plist")
}

// DefaultConfig is everything off, weekly cleanup, 10 GB and 5 GB alerts.
func DefaultConfig() Config {
	return Config{
		Schedule: Schedule{Every: EveryWeekly},
		Alerts:   Alerts{MinFreeBytes: DefaultMinFree, DropBytesPerDay: DefaultDrop},
	}
}

// Period is the time between scheduled cleans.
func Period(every string) (time.Duration, error) {
	switch every {
	case EveryDaily:
		return 24 * time.Hour, nil
	case EveryWeekly:
		return 7 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("every must be %s or %s, not %q", EveryDaily, EveryWeekly, every)
}

// LoadConfig reads agent.json. A missing file is DefaultConfig; fields the
// file leaves out keep their default.
func LoadConfig(e env.Env) (Config, error) {
	c := DefaultConfig()
	b, err := os.ReadFile(ConfigPath(e))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", ConfigPath(e), err)
	}
	if _, err := Period(c.Schedule.Every); err != nil {
		return c, fmt.Errorf("%s: schedule: %w", ConfigPath(e), err)
	}
	return c, nil
}

// SaveConfig writes agent.json.
func SaveConfig(e env.Env, c Config) error {
	if _, err := Period(c.Schedule.Every); err != nil {
		return err
	}
	return writeJSON(e, ConfigPath(e), c)
}

// LoadState reads agent-state.json. A missing file is an empty State.
func LoadState(e env.Env) (State, error) {
	var st State
	b, err := os.ReadFile(StatePath(e))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("%s: %w", StatePath(e), err)
	}
	return st, nil
}

func saveState(e env.Env, st State) error {
	return writeJSON(e, StatePath(e), st)
}

// writeJSON replaces the contents of path, a file in the state folder, with
// v as JSON. It never writes through a symlink or a hard link, or outside the
// state folder (package safefile).
func writeJSON(e env.Env, path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return safefile.Write(e.StateDir, path, append(b, '\n'), 0o600)
}

// FreeBytes returns the free bytes of the volume that holds the home folder.
// Tests replace it.
var FreeBytes = func(e env.Env) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(e.Home, &st); err != nil {
		return 0, fmt.Errorf("read free space: %w", err)
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
