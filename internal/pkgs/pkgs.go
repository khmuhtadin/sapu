// Package pkgs finds package managers and version managers and turns them
// into plan items for sapu packages (A4). Sapu never deletes package files
// with its own code here:
//
//   - Each tool's cache becomes one official_command item that runs the
//     tool's own cleaner (`brew cleanup`, `npm cache clean --force`, ...).
//     Command[0] is the program's absolute path as found on PATH when the
//     plan is made, so the plan says exactly what will run. Path is the
//     cache folder and Bytes its size; for a tool without a folder Sapu can
//     measure (docker) Path is the program itself and Bytes 0. A tool whose
//     cache folder is missing or empty is left out.
//   - docker system prune is risky and NeverSelected: Sapu never selects it,
//     even with --risk risky.
//   - Old Node, Python and Ruby installs of nvm, pyenv and rbenv become
//     quarantine items, risky, so they are never selected by default. The
//     default or global version is never offered: an install is kept when
//     its folder name equals that version or starts with it and a dot ("20"
//     keeps every v20.x). When Sapu cannot tell which version is the
//     default, it offers none of that manager's installs and says why in a
//     note. Symlinks and pyenv installs that hold virtualenvs are never
//     offered.
//
// Every item has Source "packages". Apply runs an item's command only when
// it is one of Commands, with the program where LookPath finds it then.
//
// Find only reads: it looks programs up
// on PATH and runs commands that print where a cache is (`brew --cache`,
// `go env GOCACHE`, ...), in the home folder with tool downloads and update
// checks turned off. Both go through LookPath and Output, which tests
// replace.
package pkgs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// Source is the Source of every item, and the plan kind.
const Source = plan.KindPackages

// LookPath finds a program on PATH. Output runs a command that only prints
// (such as `brew --cache`) in the folder dir and returns its trimmed stdout.
// Tests replace both.
var (
	LookPath = exec.LookPath
	Output   = output
)

func output(ctx context.Context, dir, program string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, program, args...)
	c.Dir = dir
	// Keep discovery read-only: no toolchain or package-manager downloads,
	// no update checks.
	c.Env = append(os.Environ(), "HOME="+dir, "GOTOOLCHAIN=local", "COREPACK_ENABLE_NETWORK=0",
		"PIP_DISABLE_PIP_VERSION_CHECK=1", "HOMEBREW_NO_AUTO_UPDATE=1",
		"npm_config_update_notifier=false", "YARN_DISABLE_SELF_UPDATE_CHECK=true")
	out, err := c.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// tool is one package manager's cache and its cleaner.
type tool struct {
	program string     // looked up on PATH
	clean   []string   // the cleaner's arguments, after the program
	where   []string   // arguments that print the cache folder; nil when folder is fixed
	folder  string     // the cache folder ("~/..."), when where is nil
	risk    rules.Risk // safe and moderate are selected
	explain string
}

var tools = []tool{
	{
		program: "brew", clean: []string{"cleanup", "--prune=all", "-s"}, where: []string{"--cache"}, risk: rules.RiskModerate,
		explain: "Homebrew's downloads, and old versions of the formulae and apps it installed. `brew cleanup` removes them; Homebrew downloads again whatever you reinstall.",
	},
	{
		program: "npm", clean: []string{"cache", "clean", "--force"}, folder: "~/.npm/_cacache", risk: rules.RiskModerate,
		explain: "npm's download cache. npm downloads packages again on the next install, so that install is slower.",
	},
	{
		program: "pnpm", clean: []string{"store", "prune"}, where: []string{"store", "path"}, risk: rules.RiskModerate,
		explain: "pnpm's package store. `pnpm store prune` removes only the packages that no project on this Mac uses any more.",
	},
	{
		program: "yarn", clean: []string{"cache", "clean"}, where: []string{"cache", "dir"}, risk: rules.RiskModerate,
		explain: "Yarn's download cache. Yarn downloads packages again on the next install, so that install is slower.",
	},
	{
		program: "python3", clean: []string{"-m", "pip", "cache", "purge"}, where: []string{"-m", "pip", "cache", "dir"}, risk: rules.RiskModerate,
		explain: "pip's download and wheel cache. pip downloads packages again the next time you install them.",
	},
	{
		program: "uv", clean: []string{"cache", "clean"}, where: []string{"cache", "dir"}, risk: rules.RiskModerate,
		explain: "uv's cache of Python packages and builds. uv downloads them again the next time you sync an environment.",
	},
	{
		program: "go", clean: []string{"clean", "-cache"}, where: []string{"env", "GOCACHE"}, risk: rules.RiskModerate,
		explain: "Go's build cache. The next build of each project is slower while it fills again.",
	},
	{
		program: "go", clean: []string{"clean", "-modcache"}, where: []string{"env", "GOMODCACHE"}, risk: rules.RiskModerate,
		explain: "Downloaded Go modules. They download again at the next build, so builds without a network fail until then.",
	},
	{
		program: "pod", clean: []string{"cache", "clean", "--all"}, folder: "~/Library/Caches/CocoaPods", risk: rules.RiskModerate,
		explain: "CocoaPods' downloaded pod sources. They download again the next time you run `pod install`.",
	},
}

// The commands of the simulator cleanup and docker system prune, program
// name first.
var (
	simctlDelete = []string{"xcrun", "simctl", "delete", "unavailable"}
	dockerPrune  = []string{"docker", "system", "prune"}
)

// Commands returns every command Find can put in a plan, program name
// first: each tool's cleaner, the simulator cleanup and docker system
// prune. A plan item of Source "packages" with any other command is not
// one Sapu made, and Apply refuses to run it.
func Commands() [][]string {
	var out [][]string
	for _, t := range tools {
		out = append(out, append([]string{t.program}, t.clean...))
	}
	return append(out, simctlDelete, dockerPrune)
}

const dockerExplain = "Docker's leftovers: `docker system prune` removes every stopped container " +
	"(with any data inside it that is not in a volume), every network no container uses, " +
	"dangling images and the build cache. Named volumes are kept. A stopped container you meant " +
	"to start again is gone for good. Sapu never selects this; it asks before it removes anything, " +
	"so run it yourself in Terminal if you are sure."

// manager is a version manager whose old installs may be offered.
type manager struct {
	name     string // "nvm"
	versions string // folder of installs, "~/..."
	lang     string // "Node.js"
	hint     string // the file a project uses to pick a version
	program  string // asked for its global version ("pyenv global"); "" for nvm
	file     string // the global version file, "~/..."
}

var managers = []manager{
	{name: "nvm", versions: "~/.nvm/versions/node", lang: "Node.js", hint: ".nvmrc", file: "~/.nvm/alias/default"},
	{name: "pyenv", versions: "~/.pyenv/versions", lang: "Python", hint: ".python-version", program: "pyenv", file: "~/.pyenv/version"},
	{name: "rbenv", versions: "~/.rbenv/versions", lang: "Ruby", hint: ".ruby-version", program: "rbenv", file: "~/.rbenv/version"},
}

// Find returns the items for e's home and notes: one sentence for each
// thing Find left out on purpose and why. The error is ctx's.
func Find(ctx context.Context, e env.Env) ([]plan.Item, []string, error) {
	var items []plan.Item
	var notes []string
	for _, t := range tools {
		it, note, ok := cacheItem(ctx, e, t)
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if note != "" {
			notes = append(notes, note)
		}
		if ok {
			items = append(items, it)
		}
	}
	if it, note, ok := simulators(ctx, e); ok {
		items = append(items, it)
	} else if note != "" {
		notes = append(notes, note)
	}
	if path, err := LookPath(dockerPrune[0]); err == nil {
		if it, ok := programItem(path, dockerPrune[1:], rules.RiskRisky, dockerExplain); ok {
			items = append(items, it)
		}
	}
	for _, m := range managers {
		found, note, err := oldVersions(ctx, e, m)
		if err != nil {
			return nil, nil, err
		}
		if note != "" {
			notes = append(notes, note)
		}
		items = append(items, found...)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return items, notes, nil
}

// NeverSelected reports whether Sapu must leave it unselected whatever the
// risk flags say: docker system prune.
func NeverSelected(it plan.Item) bool {
	return it.Source == Source && it.Action == rules.ActionOfficialCommand &&
		len(it.Command) > 1 && filepath.Base(it.Command[0]) == "docker"
}

// cacheItem measures one tool's cache. ok is false when the tool is not
// installed or has nothing cached; note says why when that is not obvious.
func cacheItem(ctx context.Context, e env.Env, t tool) (plan.Item, string, bool) {
	path, err := LookPath(t.program)
	if err != nil {
		return plan.Item{}, "", false
	}
	if note := needsDeveloperTools(ctx, e, path); note != "" {
		return plan.Item{}, note, false
	}
	folder := e.Expand(t.folder)
	if t.where != nil {
		out, err := Output(ctx, e.Home, path, t.where...)
		if err != nil {
			return plan.Item{}, fmt.Sprintf("%s: `%s %s` failed, so Sapu left this tool out.", t.program, t.program, strings.Join(t.where, " ")), false
		}
		folder = lastLine(out)
		if !filepath.IsAbs(folder) {
			return plan.Item{}, fmt.Sprintf("%s: `%s %s` did not print a folder, so Sapu left this tool out.", t.program, t.program, strings.Join(t.where, " ")), false
		}
	}
	it, ok := measure(ctx, filepath.Clean(folder))
	if !ok || it.Bytes == 0 {
		return plan.Item{}, "", false
	}
	it.Source = Source
	it.Risk = t.risk
	it.Regenerates = true
	it.Explain = t.explain
	it.Action = rules.ActionOfficialCommand
	it.Command = append([]string{path}, t.clean...)
	return it, "", true
}

// needsDeveloperTools guards the macOS stubs /usr/bin/python3 and
// /usr/bin/xcrun: without the Xcode command line tools, running them opens
// an install dialog, so they are asked only when `xcode-select -p` works.
func needsDeveloperTools(ctx context.Context, e env.Env, path string) string {
	if path != "/usr/bin/python3" && path != "/usr/bin/xcrun" {
		return ""
	}
	sel, err := LookPath("xcode-select")
	if err == nil {
		if _, err = Output(ctx, e.Home, sel, "-p"); err == nil {
			return ""
		}
	}
	return fmt.Sprintf("%s: the Xcode command line tools are not installed, so Sapu did not ask it.", filepath.Base(path))
}

// simulators finds iOS (and other) simulators whose runtime is no longer
// installed. They cannot start any more.
func simulators(ctx context.Context, e env.Env) (plan.Item, string, bool) {
	devices := e.Expand("~/Library/Developer/CoreSimulator/Devices")
	if _, err := os.Lstat(devices); err != nil {
		return plan.Item{}, "", false
	}
	xcrun, err := LookPath(simctlDelete[0])
	if err != nil {
		return plan.Item{}, "", false
	}
	if note := needsDeveloperTools(ctx, e, xcrun); note != "" {
		return plan.Item{}, note, false
	}
	out, err := Output(ctx, e.Home, xcrun, "simctl", "list", "devices", "unavailable", "-j")
	if err != nil {
		return plan.Item{}, "xcrun simctl: listing the simulators failed, so Sapu left them out.", false
	}
	var list struct {
		Devices map[string][]struct {
			UDID string `json:"udid"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return plan.Item{}, "xcrun simctl: Sapu could not read its list of simulators, so it left them out.", false
	}
	var count int
	var bytes int64
	for _, runtime := range list.Devices {
		for _, d := range runtime {
			if d.UDID == "" || strings.ContainsAny(d.UDID, "/.") {
				continue
			}
			n, _, err := walk.Size(ctx, filepath.Join(devices, d.UDID), nil)
			if err != nil {
				continue
			}
			count++
			bytes += n
		}
	}
	if count == 0 {
		return plan.Item{}, "", false
	}
	id, err := plan.IdentityOf(devices)
	if err != nil {
		return plan.Item{}, "", false
	}
	return plan.Item{
		Source: Source, Path: devices, Bytes: bytes, Risk: rules.RiskModerate, Identity: id,
		Explain: fmt.Sprintf("%d simulators whose iOS, watchOS or tvOS version is no longer installed, so they cannot start. "+
			"`xcrun simctl delete unavailable` deletes them with the apps and data inside them.", count),
		Action:  rules.ActionOfficialCommand,
		Command: append([]string{xcrun}, simctlDelete[1:]...),
	}, "", true
}

// programItem is a command item whose Path is the program itself.
func programItem(path string, args []string, risk rules.Risk, explain string) (plan.Item, bool) {
	id, err := plan.IdentityOf(path)
	if err != nil {
		return plan.Item{}, false
	}
	return plan.Item{
		Source: Source, Path: filepath.Clean(path), Risk: risk, Explain: explain,
		Action: rules.ActionOfficialCommand, Command: append([]string{path}, args...), Identity: id,
	}, true
}

// measure returns an item with the Path, Bytes and Identity of folder.
func measure(ctx context.Context, folder string) (plan.Item, bool) {
	id, err := plan.IdentityOf(folder)
	if err != nil {
		return plan.Item{}, false
	}
	bytes, _, err := walk.Size(ctx, folder, nil)
	if err != nil {
		return plan.Item{}, false
	}
	return plan.Item{Path: folder, Bytes: bytes, Identity: id}, true
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// oldVersions returns the installs of m that are not its default version.
func oldVersions(ctx context.Context, e env.Env, m manager) ([]plan.Item, string, error) {
	dir := e.Expand(m.versions)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, fmt.Sprintf("%s: Sapu could not read %s, so it offers none of its versions.", m.name, m.versions), nil
	}
	var names []string
	for _, de := range entries {
		if !strings.HasPrefix(de.Name(), ".") {
			names = append(names, de.Name())
		}
	}
	keep, why := defaultVersions(ctx, e, m, names)
	if why != "" {
		return nil, fmt.Sprintf("%s: %s, so Sapu offers none of its versions.", m.name, why), nil
	}

	// A kept name may be a symlink into another install (a pyenv
	// virtualenv lives in <version>/envs/<name>); that install stays too.
	var keptReal []string
	for _, name := range names {
		if matchesAny(name, keep) {
			if real, err := filepath.EvalSymlinks(filepath.Join(dir, name)); err == nil {
				keptReal = append(keptReal, real)
			}
		}
	}

	var items []plan.Item
	var withEnvs []string
	for _, de := range entries {
		name := de.Name()
		path := filepath.Join(dir, name)
		if matchesAny(name, keep) || de.Type()&fs.ModeSymlink != 0 || !de.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if holdsAny(path, keptReal) {
			continue
		}
		if hasEntries(filepath.Join(path, "envs")) {
			withEnvs = append(withEnvs, name)
			continue
		}
		it, ok := measure(ctx, path)
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		it.Source = Source
		it.Risk = rules.RiskRisky
		it.Regenerates = false
		it.Explain = fmt.Sprintf("%s %s, installed with %s. It is not your default version, but a project may still use it through %s; %s can install it again.",
			m.lang, name, m.name, m.hint, m.name)
		it.Action = rules.ActionQuarantine
		items = append(items, it)
	}
	note := ""
	if len(withEnvs) > 0 {
		note = fmt.Sprintf("%s: kept %s, which holds virtualenvs.", m.name, strings.Join(withEnvs, ", "))
	}
	return items, note, nil
}

// defaultVersions returns the version names m uses by default; nil means
// none (the system version, or no default set). why is set when Sapu cannot
// tell, in which case nothing may be offered.
func defaultVersions(ctx context.Context, e env.Env, m manager, installed []string) (keep []string, why string) {
	var names []string
	if m.name == "nvm" {
		v, why := nvmDefault(e)
		if why != "" {
			return nil, why
		}
		switch v {
		case "":
			return nil, ""
		case "node", "stable":
			newest := newestVersion(installed)
			if newest == "" {
				return nil, fmt.Sprintf("its default is %q and Sapu cannot tell which install that is", v)
			}
			return []string{newest}, ""
		}
		names = []string{v}
	} else {
		var err error
		if names, err = globalVersions(ctx, e, m); err != nil {
			return nil, fmt.Sprintf("Sapu could not read %s", m.file)
		}
	}
	for _, name := range names {
		if name == "system" {
			continue
		}
		if !slices.ContainsFunc(installed, func(dir string) bool { return matches(dir, name) }) {
			return nil, fmt.Sprintf("its default version %q matches none of the installs", name)
		}
		keep = append(keep, name)
	}
	return keep, ""
}

// nvmDefault reads ~/.nvm/alias/default and follows aliases such as
// "lts/*" -> "lts/iron" -> "v20.18.0". It returns "" when no default is set.
func nvmDefault(e env.Env) (string, string) {
	aliases := e.Expand("~/.nvm/alias")
	data, err := os.ReadFile(filepath.Join(aliases, "default"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", ""
	}
	if err != nil {
		return "", "Sapu could not read ~/.nvm/alias/default"
	}
	v := strings.TrimSpace(string(data))
	for range 5 {
		if v == "" || strings.Contains(v, "..") {
			break
		}
		next, err := os.ReadFile(filepath.Join(aliases, v))
		if err != nil {
			break
		}
		v = strings.TrimSpace(string(next))
	}
	if v == "" {
		return "", "~/.nvm/alias/default is empty"
	}
	return v, ""
}

// globalVersions asks `pyenv global` (or rbenv), and reads the version file
// when the program is not on PATH. A missing file means the system version.
func globalVersions(ctx context.Context, e env.Env, m manager) ([]string, error) {
	if path, err := LookPath(m.program); err == nil {
		if out, err := Output(ctx, e.Home, path, "global"); err == nil {
			return strings.Fields(out), nil
		}
	}
	data, err := os.ReadFile(e.Expand(m.file))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{"system"}, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(data)), nil
}

// matches reports whether the install folder dir is version name: equal, or
// name followed by a dot ("20" matches "v20.10.0"), with or without a "v".
func matches(dir, name string) bool {
	d, n := strings.TrimPrefix(dir, "v"), strings.TrimPrefix(name, "v")
	return n != "" && (d == n || strings.HasPrefix(d, n+"."))
}

func matchesAny(dir string, names []string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return matches(dir, n) })
}

// holdsAny reports whether one of paths is dir or inside it. dir is resolved
// first, as paths are.
func holdsAny(dir string, paths []string) bool {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return true // cannot tell: keep it
	}
	for _, p := range paths {
		if p == real || strings.HasPrefix(p, real+"/") {
			return true
		}
	}
	return false
}

func hasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// newestVersion returns the highest "vX.Y.Z" among names, or "" when one
// cannot be read as a version.
func newestVersion(names []string) string {
	best, bestParts := "", []int(nil)
	for _, name := range names {
		parts, ok := versionParts(name)
		if !ok {
			return ""
		}
		if bestParts == nil || slices.Compare(parts, bestParts) > 0 {
			best, bestParts = name, parts
		}
	}
	return best
}

func versionParts(name string) ([]int, bool) {
	var parts []int
	for _, s := range strings.Split(strings.TrimPrefix(name, "v"), ".") {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}
