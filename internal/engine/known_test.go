package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

// These tests check that Apply runs only the commands Sapu itself makes
// (sapuCommand), never the Source "test.command" of the other command
// tests. They put fake programs on PATH, so they are not parallel.

// fakePrograms puts an executable script for each name into a new folder
// that becomes all of PATH (with the system folders after it, for lsof),
// and returns the folder and the log every script appends "name args..." to.
func fakePrograms(t *testing.T, names ...string) (dir, log string) {
	t.Helper()
	dir = t.TempDir()
	log = filepath.Join(t.TempDir(), "ran.log")
	for _, name := range names {
		fakeProgram(t, dir, name, log)
	}
	t.Setenv("PATH", strings.Join([]string{dir, "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, string(os.PathListSeparator)))
	return dir, log
}

// fakeProgram writes dir/name, a script that appends its name and
// arguments to log.
func fakeProgram(t *testing.T, dir, name, log string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := "#!/bin/sh\necho \"" + name + " $*\" >> \"" + log + "\"\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// sourced is an official-command item of source for ~/rel.
func sourced(t *testing.T, e env.Env, rel, source string, args ...string) plan.Item {
	t.Helper()
	it := item(t, e, rel, rules.RiskSafe)
	it.Source = source
	it.Action = rules.ActionOfficialCommand
	it.Command = args
	return it
}

// gitRepo makes ~/rel a main checkout (a folder with a .git folder) and
// returns its path.
func gitRepo(t *testing.T, e env.Env, rel string) string {
	t.Helper()
	repo := homePath(e, rel)
	mkdirAll(t, filepath.Join(repo, ".git", "worktrees", "wt"))
	return repo
}

func ranLines(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// Every source of commands Sapu makes still runs: a cleanup rule's command
// (bare, as scan writes it, or with the program's path), sapu packages',
// sapu optimize's, the git worktree suggestion's and uninstall's tccutil.
func TestKnownCommandsRun(t *testing.T) {
	dir, log := fakePrograms(t, "go", "brew", "npm", "docker", "xcrun", "qlmanage", "git", "tccutil")
	e := newHome(t, map[string]string{
		"Library/Caches/tool/": "",
		"code/wt/.git":         "gitdir: elsewhere",
	})
	repo := gitRepo(t, e, "code/main")
	wt := homePath(e, "code/wt")
	tool := "Library/Caches/tool"
	p := makePlan(t, e,
		sourced(t, e, tool, "go.build-cache", "go", "clean", "-cache"),
		sourced(t, e, tool, "go.module-cache", filepath.Join(dir, "go"), "clean", "-modcache"),
		sourced(t, e, tool, "homebrew.downloads", "brew", "cleanup", "--prune=all"),
		sourced(t, e, tool, "packages", filepath.Join(dir, "npm"), "cache", "clean", "--force"),
		sourced(t, e, tool, "packages", filepath.Join(dir, "brew"), "cleanup", "--prune=all", "-s"),
		sourced(t, e, tool, "packages", filepath.Join(dir, "docker"), "system", "prune"),
		sourced(t, e, tool, "packages", filepath.Join(dir, "xcrun"), "simctl", "delete", "unavailable"),
		sourced(t, e, tool, "optimize", filepath.Join(dir, "qlmanage"), "-r", "cache"),
		sourced(t, e, "code/wt", "purge:git-worktree", "git", "-C", repo, "worktree", "remove", wt),
		sourced(t, e, tool, "uninstall", "tccutil", "reset", "All", "com.example.app"),
	)

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Failed) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("failed %+v, skipped %+v", res.Failed, res.Skipped)
	}
	want := []string{
		"go clean -cache",
		"go clean -modcache",
		"brew cleanup --prune=all",
		"npm cache clean --force",
		"brew cleanup --prune=all -s",
		"docker system prune",
		"xcrun simctl delete unavailable",
		"qlmanage -r cache",
		"tccutil reset All com.example.app",
		"git -C " + repo + " worktree remove " + wt, // path order: ~/code before ~/Library
	}
	got := ranLines(t, log)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, cr := range res.Commands {
		if cr.Failed() {
			t.Errorf("%v: %+v", cr.Item.Command, cr)
		}
	}
}

// A plan edited by hand cannot run anything Sapu did not make for the
// item's Source: other programs, wrapped programs, other arguments, another
// copy of the program, or a command of another source.
func TestUnknownCommandsAreRefused(t *testing.T) {
	dir, log := fakePrograms(t, "go", "brew", "npm", "python3", "qlmanage", "git", "tccutil", "find")
	other := t.TempDir()
	otherNpm := fakeProgram(t, other, "npm", log)
	e := newHome(t, map[string]string{
		"Library/Caches/tool/": "",
		"code/wt/.git":         "gitdir: elsewhere",
		"code/plain/":          "",
	})
	repo := gitRepo(t, e, "code/main")
	link := homePath(e, "code/link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	wt := homePath(e, "code/wt")
	marker := filepath.Join(t.TempDir(), "ran")
	tool := "Library/Caches/tool"
	npm := filepath.Join(dir, "npm")
	cases := []plan.Item{
		// other programs and wrappers, under real sources
		sourced(t, e, tool, "packages", "/usr/bin/perl", "-e", "open(F, '>"+marker+"')"),
		sourced(t, e, tool, "packages", "python3", "-c", "open('"+marker+"', 'w')"),
		sourced(t, e, tool, "go.build-cache", "find", e.Home, "-name", "x", "-exec", "touch", marker, ";"),
		sourced(t, e, tool, "packages", "env", npm, "cache", "clean", "--force"),
		sourced(t, e, tool, "packages", "/usr/bin/env", "npm", "cache", "clean", "--force"),
		sourced(t, e, tool, "packages", "/usr/bin/xargs", "touch", marker),
		// a known command with an argument changed or added
		sourced(t, e, tool, "packages", npm, "cache", "clean", "--force", marker),
		sourced(t, e, tool, "packages", npm, "cache", "clean"),
		sourced(t, e, tool, "packages", npm, "cache", "clean", "--forc"),
		sourced(t, e, tool, "go.build-cache", "go", "clean", "-cache", "-x"),
		sourced(t, e, tool, "optimize", "qlmanage", "-r", "cache", marker),
		sourced(t, e, tool, "uninstall", "tccutil", "reset", "Camera", "com.example.app"),
		sourced(t, e, tool, "uninstall", "tccutil", "reset", "All", "not-a-bundle-id"),
		sourced(t, e, tool, "uninstall", "tccutil", "reset", "All", "../com.example.app"),
		sourced(t, e, "code/wt", "purge:git-worktree", "git", "-C", repo, "worktree", "remove", "--force", wt),
		sourced(t, e, "code/wt", "purge:git-worktree", "git", "-C", repo, "worktree", "remove", homePath(e, "code/plain")),
		sourced(t, e, "code/wt", "purge:git-worktree", "git", "-C", homePath(e, "code/plain"), "worktree", "remove", wt),
		sourced(t, e, "code/wt", "purge:git-worktree", "git", "-C", link, "worktree", "remove", wt),
		// the right arguments, another program
		sourced(t, e, tool, "packages", otherNpm, "cache", "clean", "--force"),
		sourced(t, e, tool, "go.build-cache", filepath.Join(other, "go"), "clean", "-cache"),
		// the command of another source
		sourced(t, e, tool, "optimize", npm, "cache", "clean", "--force"),
		sourced(t, e, tool, "go.build-cache", "go", "clean", "-modcache"),
		sourced(t, e, tool, "npm.cache", npm, "cache", "clean", "--force"), // a quarantine rule
		sourced(t, e, tool, "no.such-rule", "brew", "cleanup", "--prune=all"),
		sourced(t, e, tool, "uninstall", filepath.Join(dir, "brew"), "cleanup", "--prune=all", "-s"),
		sourced(t, e, tool, "packages", "tccutil", "reset", "All", "com.example.app"),
		sourced(t, e, tool, "purge:node_modules", "git", "-C", repo, "worktree", "remove", wt),
		sourced(t, e, tool, "analyze", "brew", "cleanup", "--prune=all"),
	}
	p := makePlan(t, e, cases...)
	if len(p.Items) != len(cases) {
		t.Fatalf("plan has %d items, want %d (skipped %+v)", len(p.Items), len(cases), p.Skipped)
	}

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := ranLines(t, log); len(got) != 0 {
		t.Errorf("ran %q", got)
	}
	requireMissing(t, marker)
	if len(res.Commands) != 0 || len(res.Skipped) != 0 {
		t.Errorf("commands %+v, skipped %+v", res.Commands, res.Skipped)
	}
	if len(res.Failed) != len(cases) {
		t.Fatalf("%d failed, want all %d refused: %+v", len(res.Failed), len(cases), res.Failed)
	}
	for _, f := range res.Failed {
		denied := strings.HasPrefix(f.Error, "refused: Sapu never runs ")
		if !denied && !strings.HasPrefix(f.Error, "refused: "+unknownCommand) {
			t.Errorf("%s %v: %q, want %q", f.Item.Source, f.Item.Command, f.Error, unknownCommand)
		}
	}
	if n := len(eventsOf(t, e, res.RunID, runlog.EventCommandBegin)); n != 0 {
		t.Errorf("%d command_begin events", n)
	}
}

// The deny list stays as a second layer: a known command whose program on
// PATH is really a shell is refused.
func TestKnownCommandBehindAShellIsRefused(t *testing.T) {
	dir, _ := fakePrograms(t)
	if err := os.Symlink("/bin/sh", filepath.Join(dir, "npm")); err != nil {
		t.Fatal(err)
	}
	e := newHome(t, map[string]string{"Library/Caches/tool/": ""})
	p := makePlan(t, e, sourced(t, e, "Library/Caches/tool", "packages", "npm", "cache", "clean", "--force"))

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Error, "which Sapu never runs") || len(res.Commands) != 0 {
		t.Errorf("failed %+v, commands %+v", res.Failed, res.Commands)
	}
}

// A known command whose program is not on PATH is skipped as gone.
func TestKnownCommandNotInstalledIsGone(t *testing.T) {
	fakePrograms(t)
	e := newHome(t, map[string]string{"Library/Caches/tool/": ""})
	p := makePlan(t, e, sourced(t, e, "Library/Caches/tool", "go.build-cache", "go", "clean", "-cache"))

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != plan.ReasonGone || len(res.Failed) != 0 {
		t.Errorf("skipped %+v, failed %+v", res.Skipped, res.Failed)
	}
}
