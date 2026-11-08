// Package loginitems lists what launchd starts for this Mac (blueprint A1):
// your own agents in ~/Library/LaunchAgents, and, read-only, the agents and
// daemons in /Library that need an admin account to change. It only reads;
// turning an item off is a plan item the engine moves.
//
// For each plist it reports the label, the program (Program, or the first of
// ProgramArguments), RunAtLoad and KeepAlive, the app the program is in, and
// whether launchd has the job loaded in this user's session (`launchctl
// list`; daemons run in the system session, which a user cannot list). An
// item whose program no longer exists is an orphan.
//
// Background items that macOS manages itself (`sfltool dumpbtm`) need an
// admin account to read, so they are not listed.
package loginitems

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/khmuhtadin/sapu/internal/apps"
	"github.com/khmuhtadin/sapu/internal/env"
)

// Where an item lives.
const (
	ScopeUser     = "user"      // ~/Library/LaunchAgents: yours; Sapu may turn these off
	ScopeAllUsers = "all_users" // /Library/LaunchAgents: every user's session; needs an admin
	ScopeSystem   = "system"    // /Library/LaunchDaemons: runs as root; needs an admin
)

// Whether launchd has an item loaded.
const (
	StateLoaded    = "loaded"
	StateNotLoaded = "not_loaded"
	StateUnknown   = "unknown" // daemons: a user cannot list the system session
)

type Dir struct {
	Path  string // "~" is the home folder
	Scope string
}

// Dirs are the folders listed, in order. Tests point them at temp folders.
var Dirs = []Dir{
	{"~/Library/LaunchAgents", ScopeUser},
	{"/Library/LaunchAgents", ScopeAllUsers},
	{"/Library/LaunchDaemons", ScopeSystem},
}

// Loaded returns the labels launchd has loaded in this user's session. Tests
// replace it, so they never depend on the real launchd.
var Loaded = func(ctx context.Context) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, "launchctl", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("launchctl list: %w", err)
	}
	return ParseList(string(out)), nil
}

// ParseList reads `launchctl list` output: a header, then lines of
// "PID<tab>Status<tab>Label".
func ParseList(out string) map[string]bool {
	labels := map[string]bool{}
	for i, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "\t")
		if i == 0 || len(fields) < 3 {
			continue
		}
		labels[strings.TrimSpace(fields[2])] = true
	}
	return labels
}

type Item struct {
	Path      string `json:"path"`  // the plist
	Scope     string `json:"scope"` // ScopeUser, ScopeAllUsers or ScopeSystem
	Label     string `json:"label"`
	Program   string `json:"program"`
	RunAtLoad bool   `json:"run_at_load"`
	KeepAlive bool   `json:"keep_alive"`    // true too when KeepAlive has conditions
	App       string `json:"app,omitempty"` // the .app the program is in
	State     string `json:"state"`         // StateLoaded, StateNotLoaded or StateUnknown
	Orphan    bool   `json:"orphan"`        // the program does not exist any more
	Error     string `json:"error,omitempty"`
}

// Name is the label, or the file name without .plist when there is no label.
func (it Item) Name() string {
	if it.Label != "" {
		return it.Label
	}
	return strings.TrimSuffix(filepath.Base(it.Path), ".plist")
}

// List reads every *.plist in Dirs. A folder that does not exist is
// skipped. A plist that cannot be read is listed with Error set.
func List(ctx context.Context, e env.Env) ([]Item, error) {
	loaded, err := Loaded(ctx)
	if err != nil {
		return nil, err
	}
	var items []Item
	for _, d := range Dirs {
		dir := e.Expand(d.Path)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".plist") {
				continue
			}
			it := read(ctx, filepath.Join(dir, entry.Name()), d.Scope)
			switch {
			case d.Scope == ScopeSystem:
				it.State = StateUnknown
			case loaded[it.Label]:
				it.State = StateLoaded
			default:
				it.State = StateNotLoaded
			}
			items = append(items, it)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func read(ctx context.Context, path, scope string) Item {
	it := Item{Path: path, Scope: scope}
	m, err := apps.ReadPlist(ctx, path)
	if errors.Is(err, fs.ErrPermission) {
		it.Error = "cannot read it without an admin account"
		return it
	}
	if err != nil {
		it.Error = err.Error()
		return it
	}
	it.Label, _ = m["Label"].(string)
	it.Program, _ = m["Program"].(string)
	if args, ok := m["ProgramArguments"].([]any); ok && it.Program == "" && len(args) > 0 {
		it.Program, _ = args[0].(string)
	}
	it.RunAtLoad, _ = m["RunAtLoad"].(bool)
	switch v := m["KeepAlive"].(type) {
	case bool:
		it.KeepAlive = v
	case map[string]any:
		it.KeepAlive = true
	}
	it.App = appOf(it.Program)
	if filepath.IsAbs(it.Program) {
		_, err := os.Stat(it.Program)
		it.Orphan = errors.Is(err, fs.ErrNotExist)
	}
	return it
}

// appOf returns the .app bundle that program is in, or "".
func appOf(program string) string {
	segs := strings.Split(program, "/")
	for i, seg := range segs {
		if i > 0 && strings.HasSuffix(strings.ToLower(seg), ".app") {
			return strings.Join(segs[:i+1], "/")
		}
	}
	return ""
}

// Find returns the items whose label, or file name without .plist, is name.
func Find(items []Item, name string) []Item {
	var out []Item
	for _, it := range items {
		if it.Label == name || strings.TrimSuffix(filepath.Base(it.Path), ".plist") == name {
			out = append(out, it)
		}
	}
	return out
}
