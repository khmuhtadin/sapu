package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/loginitems"
)

func launchAgent(label, program string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Label</key><string>` + label + `</string>
<key>ProgramArguments</key><array><string>` + program + `</string><string>--flag</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><true/>
</dict></plist>
`
}

// loginHome builds a home with three user agents (one of them an orphan)
// and an orphan daemon in a stand-in for /Library/LaunchDaemons. launchd
// reports active and gone as loaded.
func loginHome(t *testing.T) (e env.Env, user, system string) {
	t.Helper()
	e = env.New(t.TempDir())
	allUsers, system := t.TempDir(), t.TempDir()
	oldDirs, oldLoaded := loginitems.Dirs, loginitems.Loaded
	loginitems.Dirs = []loginitems.Dir{
		{Path: "~/Library/LaunchAgents", Scope: loginitems.ScopeUser},
		{Path: allUsers, Scope: loginitems.ScopeAllUsers},
		{Path: system, Scope: loginitems.ScopeSystem},
	}
	loginitems.Loaded = func(context.Context) (map[string]bool, error) {
		return map[string]bool{"com.example.active": true, "com.example.gone": true}, nil
	}
	t.Cleanup(func() { loginitems.Dirs, loginitems.Loaded = oldDirs, oldLoaded })

	user = filepath.Join(e.Home, "Library", "LaunchAgents")
	program := filepath.Join(e.Home, "Applications", "Active.app", "Contents", "MacOS", "agent")
	writeFile(t, program, "#!/bin/sh\n")
	writeFile(t, filepath.Join(user, "com.example.active.plist"), launchAgent("com.example.active", program))
	gone := filepath.Join(e.Home, "Applications", "Gone.app", "Contents", "MacOS", "helper")
	writeFile(t, filepath.Join(user, "com.example.gone.plist"), launchAgent("com.example.gone", gone))
	writeFile(t, filepath.Join(user, "com.example.idle.plist"), launchAgent("com.example.idle", "/bin/sh"))
	writeFile(t, filepath.Join(system, "com.example.daemon.plist"), launchAgent("com.example.daemon", filepath.Join(e.Home, "gone-daemon")))
	return e, user, system
}

func TestLoginItemsList(t *testing.T) {
	e, _, _ := loginHome(t)
	before := snapshot(t, e)
	out, errOut, code := sapu(t, e, "", "login-items")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu login-items\n%s", out)
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)

	for _, line := range []string{
		"~/Library/LaunchAgents · Yours",
		"needs an admin account to change",
		"com.example.active", "~/Applications/Active.app/Contents/MacOS/agent",
		"app: Active · starts at login · kept running",
		"orphan: its program no longer exists",
		"com.example.daemon",
		"Orphans of yours: 1. Turn them all off: sapu login-items disable --orphans",
		"System Settings › General › Login Items",
	} {
		if !strings.Contains(strings.Join(strings.Fields(out), " "), line) {
			t.Errorf("output lacks %q", line)
		}
	}
	lines := strings.Split(out, "\n")
	if i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "  com.example.active ") }); i < 0 ||
		!strings.HasSuffix(lines[i], " loaded") {
		t.Errorf("active is not shown as loaded:\n%s", out)
	}
	if i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "  com.example.idle ") }); i < 0 ||
		!strings.HasSuffix(lines[i], "not loaded") {
		t.Errorf("idle is not shown as not loaded:\n%s", out)
	}

	out, _, code = sapu(t, e, "", "login-items", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "login-items", out)
	var d loginItemsData
	decodeData(t, out, &d)
	orphans := map[string]bool{}
	for _, it := range d.Items {
		orphans[it.Label] = it.Orphan
	}
	if len(d.Items) != 4 || !orphans["com.example.gone"] || !orphans["com.example.daemon"] || orphans["com.example.active"] {
		t.Errorf("items %+v", d.Items)
	}

	if _, _, code := sapu(t, e, "", "login-items", "bogus"); code != 2 {
		t.Errorf("login-items bogus: exit %d, want 2", code)
	}
}

func TestLoginItemsEmpty(t *testing.T) {
	e := env.New(t.TempDir())
	old, oldLoaded := loginitems.Dirs, loginitems.Loaded
	loginitems.Dirs = []loginitems.Dir{{Path: "~/Library/LaunchAgents", Scope: loginitems.ScopeUser}}
	loginitems.Loaded = func(context.Context) (map[string]bool, error) { return nil, nil }
	t.Cleanup(func() { loginitems.Dirs, loginitems.Loaded = old, oldLoaded })

	out, _, code := sapu(t, e, "", "login-items")
	if code != 0 || !strings.Contains(out, "(none)") {
		t.Errorf("exit %d\n%s", code, out)
	}
	out, _, code = sapu(t, e, "", "login-items", "--json")
	if code != 0 || !strings.Contains(out, `"items": []`) {
		t.Errorf("exit %d\n%s", code, out)
	}
	validate(t, "login-items", out)
}

func TestLoginItemsDisableAndUndo(t *testing.T) {
	e, user, _ := loginHome(t)
	before := snapshot(t, e)
	plist := filepath.Join(user, "com.example.gone.plist")

	out, errOut, code := sapu(t, e, "", "login-items", "disable", "com.example.gone", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu login-items disable\n%s", out)
	for _, line := range []string{
		"login-items · 1 item",
		"Its program no longer exists",
		"Moved 1 item",
		"These keep running until you log out",
		fmt.Sprintf("launchctl bootout gui/%d/com.example.gone", os.Getuid()),
	} {
		if !strings.Contains(strings.Join(strings.Fields(out), " "), line) {
			t.Errorf("output lacks %q", line)
		}
	}
	if exists(plist) {
		t.Error("the plist was not moved")
	}
	for _, kept := range []string{"com.example.active.plist", "com.example.idle.plist"} {
		if !exists(filepath.Join(user, kept)) {
			t.Errorf("%s was moved", kept)
		}
	}

	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))

	// An item that is not loaded needs nothing stopped; the file name works as a name too.
	out, _, code = sapu(t, e, "", "login-items", "disable", "com.example.idle", "--yes")
	if code != 0 || strings.Contains(out, "launchctl bootout") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestLoginItemsDisableOrphans(t *testing.T) {
	e, user, _ := loginHome(t)
	before := snapshot(t, e)
	file := filepath.Join(t.TempDir(), "plan.json")
	out, _, code := sapu(t, e, "", "login-items", "disable", "--orphans", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
	p := readPlanFile(t, file)
	// The daemon is an orphan too, but it is not the user's to change.
	if got := selectedPaths(p); !slices.Equal(got, []string{filepath.Join(user, "com.example.gone.plist")}) {
		t.Errorf("selected %v", got)
	}
	if p.Kind != "login-items" || len(p.Items) != 1 || p.Items[0].Source != "login-items" || p.Items[0].Risk != "moderate" {
		t.Errorf("plan %+v", p)
	}
}

func TestLoginItemsDisableRefuses(t *testing.T) {
	e, _, system := loginHome(t)
	before := snapshot(t, e)

	_, errOut, code := sapu(t, e, "", "login-items", "disable", "com.example.daemon", "--yes")
	if code != 1 || !strings.Contains(errOut, "needs an admin account") || !strings.Contains(errOut, system) {
		t.Errorf("daemon: exit %d, %s", code, errOut)
	}
	_, errOut, code = sapu(t, e, "", "login-items", "disable", "com.example.nope", "--yes")
	if code != 1 || !strings.Contains(errOut, `no login item "com.example.nope"`) {
		t.Errorf("unknown: exit %d, %s", code, errOut)
	}
	if _, _, code := sapu(t, e, "", "login-items", "disable"); code != 2 {
		t.Errorf("no label: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "login-items", "disable", "com.example.gone", "--json"); code != 2 {
		t.Errorf("--json without --yes: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "login-items", "disable", "com.example.gone"); code != 2 {
		t.Errorf("no answer: exit %d, want 2", code)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestLoginItemsDisableJSON(t *testing.T) {
	e, _, _ := loginHome(t)
	out, _, code := sapu(t, e, "", "login-items", "disable", "com.example.gone", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "login-items", out)
	var d runData
	decodeData(t, out, &d)
	if d.Result == nil || len(d.Result.Moved) != 1 {
		t.Errorf("data %+v", d)
	}

	out, _, code = sapu(t, e, "", "login-items", "disable", "com.example.daemon", "--yes", "--json")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "login-items", out)
	if command, errCode, _ := errorOf(t, out); command != "login-items-disable" || errCode != "error" {
		t.Errorf("command %q, code %q", command, errCode)
	}
}
