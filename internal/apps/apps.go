// Package apps finds installed apps and the files they leave in ~/Library,
// for sapu uninstall (blueprint F2). It only reads; the engine moves.
//
//   - List finds X.app bundles in Dirs, at the top level of each folder and
//     one folder deep. Symlinks are not followed, so a linked app is not
//     listed. Info.plist is read with plutil.
//   - Find matches a query to apps by path, bundle id or name.
//   - Leftovers finds what an app left in ~/Library. Matches by bundle id are
//     exact (APFS ignores case, so the comparison does too). Matches by name
//     are only candidates: another app may use the same name.
//   - DataLeftovers is the same search limited to the folders that hold the
//     app's settings and data, for sapu reset, which keeps the app.
//
// Every match uses the name the entry has on disk, so a path Sapu moves and
// later restores keeps its exact spelling.
package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/env"
)

// Dirs are the folders searched for apps; "~" is the home folder. Tests
// point it at temp folders.
var Dirs = []string{"/Applications", "~/Applications"}

type App struct {
	Path       string // the .app bundle, absolute
	ID         string // CFBundleIdentifier; may be empty
	Name       string // CFBundleName, or the bundle's file name without .app
	Executable string // CFBundleExecutable; may be empty
	Version    string // CFBundleShortVersionString; may be empty
}

// FileName is the bundle's file name without ".app", e.g. "Slack".
func (a App) FileName() string {
	base := filepath.Base(a.Path)
	return base[:len(base)-len(filepath.Ext(base))]
}

// ValidBundleID reports whether id looks like a bundle id: words joined by
// dots, at least two of them, nothing that could be a path.
func ValidBundleID(id string) bool {
	return strings.Contains(id, ".") && !strings.ContainsAny(id, "/ ") && !strings.HasPrefix(id, ".")
}

// IsApple reports whether id is one of Apple's bundle ids.
func IsApple(id string) bool {
	return strings.HasPrefix(strings.ToLower(id), "com.apple.")
}

// List returns the apps in Dirs. A folder that does not exist is skipped. An
// app whose Info.plist cannot be read is listed with only Path and Name.
func List(ctx context.Context, e env.Env) ([]App, error) {
	var found []App
	for _, dir := range Dirs {
		dir = e.Expand(dir)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			p := filepath.Join(dir, entry.Name())
			if isBundle(entry.Name()) {
				found = append(found, read(ctx, p))
				continue
			}
			inner, err := os.ReadDir(p)
			if err != nil {
				continue // a folder we cannot read holds no app we could move
			}
			for _, in := range inner {
				if in.IsDir() && isBundle(in.Name()) {
					found = append(found, read(ctx, filepath.Join(p, in.Name())))
				}
			}
		}
	}
	return found, ctx.Err()
}

func isBundle(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".app")
}

// read builds an App from the bundle's Info.plist.
func read(ctx context.Context, path string) App {
	a := App{Path: path}
	info, err := ReadPlist(ctx, filepath.Join(path, "Contents", "Info.plist"))
	if err == nil {
		a.ID = stringOf(info, "CFBundleIdentifier")
		a.Name = stringOf(info, "CFBundleName")
		a.Executable = stringOf(info, "CFBundleExecutable")
		a.Version = stringOf(info, "CFBundleShortVersionString")
	}
	if a.Name == "" {
		a.Name = a.FileName()
	}
	return a
}

func stringOf(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

// ReadPlist reads a property list (XML or binary) and returns its top-level
// dictionary. plutil does the parsing.
func ReadPlist(ctx context.Context, path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "plutil", "-convert", "json", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s is not a readable property list: %s", path, strings.TrimSpace(stderr.String()))
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("%s is not a dictionary: %w", path, err)
	}
	return m, nil
}

// Find returns the apps that query names. A query with a "/" is a path to
// the bundle; anything else matches the bundle id, the name or the file name
// ("Slack" or "Slack.app"), ignoring case.
func Find(all []App, query string) []App {
	var out []App
	if strings.Contains(query, "/") {
		abs, err := filepath.Abs(query)
		if err != nil {
			return nil
		}
		for _, a := range all {
			if strings.EqualFold(a.Path, abs) {
				out = append(out, a)
			}
		}
		return out
	}
	name := query
	if isBundle(name) {
		name = name[:len(name)-len(".app")]
	}
	for _, a := range all {
		if strings.EqualFold(a.ID, query) || strings.EqualFold(a.Name, name) || strings.EqualFold(a.FileName(), name) {
			out = append(out, a)
		}
	}
	return out
}

// Others returns the other apps in all that share a's bundle id (sameID) and
// the ones that share its name (sameName). Their files in ~/Library may be
// the same files as a's.
func Others(all []App, a App) (sameID, sameName []App) {
	for _, other := range all {
		if other.Path == a.Path {
			continue
		}
		if a.ID != "" && strings.EqualFold(other.ID, a.ID) {
			sameID = append(sameID, other)
		}
		if strings.EqualFold(other.Name, a.Name) || strings.EqualFold(other.FileName(), a.Name) {
			sameName = append(sameName, other)
		}
	}
	return sameID, sameName
}

// Leftover is a file or folder in ~/Library that belongs to an app.
type Leftover struct {
	Path   string
	Folder string // the folder under ~/Library it is in, e.g. "Caches" or "Preferences/ByHost"
	ByName bool   // matched by the app's name only, not by its bundle id
}

// idFolders are the folders under ~/Library where apps keep files named
// after their bundle id. matchID says which names count.
var idFolders = []string{
	"Application Scripts",
	"Application Support",
	"Caches",
	"Containers",
	"Cookies",
	"Group Containers",
	"HTTPStorages",
	"LaunchAgents",
	"Logs",
	"Preferences",
	"Preferences/ByHost",
	"Saved Application State",
	"WebKit",
}

// dataFolders are the idFolders that hold what the app keeps for itself:
// settings, sign-ins, data and caches. Application Scripts and LaunchAgents
// are left out: they are not data, and a reset keeps the app and its login
// item working.
var dataFolders = []string{
	"Application Support",
	"Caches",
	"Containers",
	"Cookies",
	"Group Containers",
	"HTTPStorages",
	"Logs",
	"Preferences",
	"Preferences/ByHost",
	"Saved Application State",
	"WebKit",
}

// nameFolders are the folders where apps often keep files named after the
// app instead of its bundle id.
var nameFolders = []string{"Application Support", "Caches", "Logs"}

// matchID reports whether name, an entry of ~/Library/<folder>, belongs to
// the app with bundle id id. Both are lower case.
func matchID(folder, name, id string) bool {
	switch folder {
	case "Preferences":
		return name == id+".plist"
	case "Preferences/ByHost":
		// <id>.<host uuid>.plist
		return strings.HasPrefix(name, id+".") && strings.HasSuffix(name, ".plist")
	case "LaunchAgents":
		// <id>.plist or <id>.<helper>.plist, never <id>2.plist
		return strings.HasPrefix(name, id+".") && strings.HasSuffix(name, ".plist")
	case "Group Containers":
		// <team id>.<id> or group.<id>: one word before the id
		team, ok := strings.CutSuffix(name, "."+id)
		return ok && team != "" && !strings.Contains(team, ".")
	case "Saved Application State":
		return name == id+".savedstate"
	case "HTTPStorages":
		return name == id || name == id+".binarycookies"
	case "Cookies":
		return name == id+".binarycookies"
	}
	return name == id
}

// Leftovers returns what the app with bundle id id and name name left in
// ~/Library: exact bundle-id matches, then, when name is not empty, entries
// in nameFolders called name. An empty id finds only name matches.
//
// A folder macOS does not let Sapu read (~/Library/Cookies needs Full Disk
// Access) is not an error: it is returned in unreadable, and nothing in it is
// listed.
func Leftovers(e env.Env, id, name string) (found []Leftover, unreadable []string, err error) {
	return leftovers(e, id, name, idFolders)
}

// DataLeftovers is Leftovers limited to the folders that hold the app's own
// settings and data (dataFolders).
func DataLeftovers(e env.Env, id, name string) (found []Leftover, unreadable []string, err error) {
	return leftovers(e, id, name, dataFolders)
}

func leftovers(e env.Env, id, name string, folders []string) (found []Leftover, unreadable []string, err error) {
	lib := filepath.Join(e.Home, "Library")
	var out []Leftover
	seen := map[string]bool{}
	add := func(folder string, byName bool, match func(lower string) bool) error {
		dir := filepath.Join(lib, folder)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if errors.Is(err, fs.ErrPermission) {
			if !slices.Contains(unreadable, dir) {
				unreadable = append(unreadable, dir)
			}
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			p := filepath.Join(dir, entry.Name())
			if !seen[p] && match(strings.ToLower(entry.Name())) {
				seen[p] = true
				out = append(out, Leftover{Path: p, Folder: folder, ByName: byName})
			}
		}
		return nil
	}
	if id != "" {
		lowerID := strings.ToLower(id)
		for _, folder := range folders {
			if err := add(folder, false, func(n string) bool { return matchID(folder, n, lowerID) }); err != nil {
				return nil, nil, err
			}
		}
	}
	if name != "" {
		lowerName := strings.ToLower(name)
		for _, folder := range nameFolders {
			if err := add(folder, true, func(n string) bool { return n == lowerName }); err != nil {
				return nil, nil, err
			}
		}
	}
	slices.SortFunc(out, func(a, b Leftover) int { return strings.Compare(a.Path, b.Path) })
	return out, unreadable, nil
}

// Movable reports whether this user may move the bundle at path: moving a
// folder to another parent needs write permission on the folder itself and
// on the folder that holds it. Apps installed by an installer package or the
// App Store are usually owned by root, and then this is false.
func Movable(path string) bool {
	return unix.Access(path, unix.W_OK) == nil && unix.Access(filepath.Dir(path), unix.W_OK) == nil
}
