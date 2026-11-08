// Package protect decides which paths Sapu must never touch: the built-in
// protection list in rules/protect/*.yml ("caches that are really data",
// blueprint S4) and the user's whitelist file (S7).
//
// Check blocks a path when it EQUALS, is INSIDE, or CONTAINS a protected
// location or a whitelist entry. "Contains" matters: quarantining
// ~/Library/Application Support would take JetBrains settings with it.
// Protected entries may use the same globs as rules (see package rules).
// There is no switch to turn protection off.
//
// Whitelist file (env.WhitelistFile): one path or glob per line, "~" allowed,
// blank lines and lines starting with "#" ignored. A missing file means an
// empty whitelist.
//
// How a path is matched (protected entries and whitelist lines alike): the
// path and the pattern are split into segments, lower-cased (APFS ignores
// case), and compared one by one with filepath.Match over the length of the
// SHORTER of the two. If every compared segment matches, the path is blocked.
// That one rule covers "equal", "inside" and "contains".
//
// Symlinks: every pattern is expanded with Home and with Home's real path
// (macOS test homes under /var live in /private/var), and a path is checked
// as given and with the symlinks in its parent folder resolved. The literal
// part of each expanded pattern (the segments before the first one with a
// wildcard) is also resolved with filepath.EvalSymlinks when Load runs; when
// it leads somewhere else, the pattern is added with that part replaced. So
// a protected folder or whitelist entry that is a symlink (~/.claude ->
// ~/cache/sessions), or sits behind one, is protected where it really is too.
//
// Exceptions (allow): an entry may let one dedicated command offer single
// items inside it, e.g. sapu backups one backup folder in MobileSync:
//
//	allow:
//	  - source: backups
//	    paths: ["~/Library/Application Support/MobileSync/Backup/*"]
//
// An exception applies only when the item's Source (plan.Item.Source) is
// the allow's source AND the item's path matches one of its paths exactly:
// segment by segment with the same number of segments. So the pattern
// above lets one backup folder through, never MobileSync, Backup or
// anything deeper. The whitelist has no exceptions and always wins. The
// source must be a dedicated command (rules.ReservedSources), never a rule
// id; rules.Validate keeps rule ids off those names, so no cleaner rule's
// items can use an exception.
//
// More to protect (W8-D): Load also adds what Extra returns, the protection
// files of an installed rule bundle and the whitelist paths of sapu.toml.
// They can only add. Each added entry is a match of its own next to the
// built-in ones, and a path is blocked when ANY match blocks it, each with
// only its own allow. So an added entry with the id or the paths of a
// built-in one cannot remove it, narrow it or let another source in: the
// allow of a bundle's entry only ever opens that entry's own match, and the
// built-in match still blocks.
//
// Implemented in Wave 1 (W1-C).
package protect

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/safefile"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

// Entry is one protected location from rules/protect/*.yml.
type Entry struct {
	ID     string            `yaml:"id" json:"id"`
	Paths  []string          `yaml:"paths" json:"paths"`
	Reason map[string]string `yaml:"reason" json:"reason"` // "en" required
	// Allow lets a dedicated command offer single items here (see the
	// package comment). Cleanup rules, purge and analyze never can.
	Allow []Allow `yaml:"allow,omitempty" json:"allow,omitempty"`
}

// Allow is one exception of an Entry: items of Source (plan.Item.Source,
// e.g. "backups") whose path matches one of Paths exactly, with the same
// number of segments.
type Allow struct {
	Source string   `yaml:"source" json:"source"`
	Paths  []string `yaml:"paths" json:"paths"`
}

type Checker struct {
	protected []match
	whitelist []match
	lang      string
	reasons   map[string]map[string]string // entry id -> language -> reason
}

// match is one protected entry or whitelist line, ready to compare.
type match struct {
	reason   string     // what Check returns
	patterns [][]string // the pattern as segments, once per spelling of Home
	allow    []allowed  // exceptions (protected entries only)
}

// allowed is one Allow, ready to compare.
type allowed struct {
	source   string
	patterns [][]string // each path of the Allow, once per spelling of Home
}

// Extra, when set, returns more for Load to protect in e: the protection
// files of the installed rule bundle in use (nil when there is none) and the
// [whitelist] paths of sapu.toml. Package cli sets it before any command
// runs. Load only ever adds what it returns (see the package comment).
var Extra func(e env.Env) (files fs.FS, whitelist []string, err error)

// Load reads the embedded protection list and the user's whitelist, plus
// what Extra returns.
func Load(e env.Env) (*Checker, error) {
	sub, err := fs.Sub(ruledata.FS, "protect")
	if err != nil {
		return nil, err
	}
	entries, err := loadEntries(sub)
	if err != nil {
		return nil, err
	}
	lines, err := ReadWhitelist(e)
	if err != nil {
		return nil, err
	}
	if Extra != nil {
		files, more, err := Extra(e)
		if err != nil {
			return nil, err
		}
		if files != nil {
			added, err := loadEntries(files)
			if err != nil {
				return nil, fmt.Errorf("rule bundle: %w", err)
			}
			entries = append(entries, added...) // after the built-in ones: their reasons win
		}
		lines = append(lines, more...)
	}
	return newChecker(e, entries, lines)
}

// ValidateFS checks protection files the way Load reads them.
func ValidateFS(fsys fs.FS) error {
	_, err := loadEntries(fsys)
	return err
}

func newChecker(e env.Env, entries []Entry, whitelist []string) (*Checker, error) {
	c := &Checker{lang: e.Lang, reasons: map[string]map[string]string{}}
	for _, entry := range entries {
		if _, ok := c.reasons[entry.ID]; !ok {
			c.reasons[entry.ID] = entry.Reason
		}
		var allow []allowed
		for _, a := range entry.Allow {
			al := allowed{source: a.Source}
			for _, p := range a.Paths {
				al.patterns = append(al.patterns, spelled(e, p)...)
			}
			allow = append(allow, al)
		}
		for _, p := range entry.Paths {
			m := newMatch(e, "protected:"+entry.ID, p)
			m.allow = allow
			c.protected = append(c.protected, m)
		}
	}
	for _, line := range whitelist {
		if err := checkWhitelistEntry(line); err != nil {
			return nil, fmt.Errorf("whitelist %s: %w", e.WhitelistFile(), err)
		}
		c.whitelist = append(c.whitelist, newMatch(e, "whitelisted:"+line, line))
	}
	return c, nil
}

// loadEntries reads every *.yml file in fsys and validates each entry.
func loadEntries(fsys fs.FS) ([]Entry, error) {
	names, err := fs.Glob(fsys, "*.yml")
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, errors.New("protect: no protection files found")
	}
	var all []Entry
	ids := map[string]bool{}
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		var entries []Entry
		if err := dec.Decode(&entries); err != nil {
			return nil, fmt.Errorf("protect: %s: %w", name, err)
		}
		for _, entry := range entries {
			if err := validate(entry); err != nil {
				return nil, fmt.Errorf("protect: %s: %w", name, err)
			}
			if ids[entry.ID] {
				return nil, fmt.Errorf("protect: %s: duplicate id %q", name, entry.ID)
			}
			ids[entry.ID] = true
		}
		all = append(all, entries...)
	}
	return all, nil
}

func validate(e Entry) error {
	if e.ID == "" {
		return errors.New("entry without id")
	}
	if len(e.Paths) == 0 {
		return fmt.Errorf("entry %q has no paths", e.ID)
	}
	for _, p := range e.Paths {
		if !strings.HasPrefix(p, "~/") {
			return fmt.Errorf("entry %q: path %q must start with ~/", e.ID, p)
		}
		if err := checkGlob(p); err != nil {
			return fmt.Errorf("entry %q: %w", e.ID, err)
		}
	}
	if e.Reason["en"] == "" {
		return fmt.Errorf("entry %q has no reason.en", e.ID)
	}
	for _, a := range e.Allow {
		if !slices.Contains(rules.ReservedSources, a.Source) {
			return fmt.Errorf("entry %q: allow source %q must be a dedicated command (one of %s), never a rule id",
				e.ID, a.Source, strings.Join(rules.ReservedSources, ", "))
		}
		if len(a.Paths) == 0 {
			return fmt.Errorf("entry %q: allow for %q has no paths", e.ID, a.Source)
		}
		for _, p := range a.Paths {
			if !strings.HasPrefix(p, "~/") {
				return fmt.Errorf("entry %q: allow path %q must start with ~/", e.ID, p)
			}
			if err := checkGlob(p); err != nil {
				return fmt.Errorf("entry %q: allow: %w", e.ID, err)
			}
		}
	}
	return nil
}

// checkGlob rejects "**" and segments filepath.Match cannot parse.
func checkGlob(p string) error {
	if strings.Contains(p, "**") {
		return fmt.Errorf("%q: ** is not allowed", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if _, err := filepath.Match(seg, ""); err != nil {
			return fmt.Errorf("%q: %w", p, err)
		}
	}
	return nil
}

// CheckWhitelistEntry accepts one whitelist entry: a line of the whitelist
// file or a path in sapu.toml.
func CheckWhitelistEntry(entry string) error {
	return checkWhitelistEntry(entry)
}

// checkWhitelistEntry accepts one line of the whitelist file.
func checkWhitelistEntry(entry string) error {
	switch {
	case entry == "":
		return errors.New("empty whitelist entry")
	case strings.ContainsAny(entry, "\r\n"):
		return fmt.Errorf("%q: an entry is one line", entry)
	case strings.HasPrefix(entry, "#"):
		return fmt.Errorf("%q: an entry cannot start with #", entry)
	case entry != "~" && !strings.HasPrefix(entry, "~/") && !strings.HasPrefix(entry, "/"):
		return fmt.Errorf("%q: use an absolute path or one starting with ~/", entry)
	}
	return checkGlob(entry)
}

func newMatch(e env.Env, reason, pattern string) match {
	m := match{reason: reason, patterns: spelled(e, pattern)}
	for _, home := range homes(e) {
		expanded := env.Env{Home: home}.Expand(pattern)
		// A wildcard can match a symlink further down (~/keep/*/data with
		// ~/keep/a/data -> ~/cache/x). Protect where each existing match
		// really leads, too.
		if strings.ContainsAny(expanded, `*?[`) {
			found, _ := filepath.Glob(expanded) // a bad pattern was refused at Load
			for _, f := range found {
				if real, err := filepath.EvalSymlinks(f); err == nil && real != f {
					m.patterns = append(m.patterns, splitPath(real))
				}
			}
		}
	}
	return m
}

// spelled returns pattern as segments, expanded with each spelling of Home,
// and again with the symlinks in its literal prefix resolved.
func spelled(e env.Env, pattern string) [][]string {
	var out [][]string
	for _, home := range homes(e) {
		expanded := env.Env{Home: home}.Expand(pattern)
		out = append(out, splitPath(expanded))
		if resolved, ok := resolvePrefix(expanded); ok {
			out = append(out, splitPath(resolved))
		}
	}
	return out
}

// resolvePrefix resolves the symlinks in the literal prefix of pattern, the
// segments before the first one that holds a wildcard, and returns the
// pattern with that prefix replaced. ok is false when the prefix does not
// exist, cannot be resolved, or resolves to itself.
func resolvePrefix(pattern string) (resolved string, ok bool) {
	segs := strings.Split(filepath.Clean(pattern), "/")
	n := 0
	for n < len(segs) && !strings.ContainsAny(segs[n], `*?[\`) {
		n++
	}
	prefix := strings.Join(segs[:n], "/")
	if prefix == "" {
		return "", false
	}
	real, err := filepath.EvalSymlinks(prefix)
	if err != nil || real == prefix {
		return "", false
	}
	return filepath.Join(append([]string{real}, segs[n:]...)...), true
}

// homes returns Home, plus its symlink-resolved form when that differs.
func homes(e env.Env) []string {
	home := filepath.Clean(e.Home)
	real, err := filepath.EvalSymlinks(home)
	if err != nil || real == home {
		return []string{home}
	}
	return []string{home, real}
}

// splitPath cleans p and returns its lower-cased segments.
func splitPath(p string) []string {
	var segs []string
	for _, seg := range strings.Split(filepath.Clean(p), "/") {
		if seg != "" {
			segs = append(segs, strings.ToLower(seg))
		}
	}
	return segs
}

func (m match) blocks(path []string) bool {
	for _, pattern := range m.patterns {
		if segmentsMatch(pattern, path) {
			return true
		}
	}
	return false
}

// allows reports whether one of m's exceptions lets an item of source at
// path through: same source, and a pattern with exactly as many segments
// as path that matches each of them.
func (m match) allows(source string, path []string) bool {
	if source == "" {
		return false
	}
	for _, a := range m.allow {
		if a.source != source {
			continue
		}
		for _, pattern := range a.patterns {
			if len(pattern) == len(path) && segmentsMatch(pattern, path) {
				return true
			}
		}
	}
	return false
}

// segmentsMatch compares pattern and path over the length of the shorter one.
// Patterns are checked by checkGlob, so a Match error cannot happen; if it
// does, the segment counts as a match (blocking is the safe answer).
func segmentsMatch(pattern, path []string) bool {
	n := min(len(pattern), len(path))
	for i := range n {
		ok, err := filepath.Match(pattern[i], path[i])
		if err != nil {
			return true
		}
		if !ok {
			return false
		}
	}
	return true
}

// spellings returns path as given, plus the path with the symlinks in its
// parent folder resolved (when that works and differs).
func spellings(path string) []string {
	out := []string{path}
	real, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return out
	}
	if resolved := filepath.Join(real, filepath.Base(path)); resolved != path {
		out = append(out, resolved)
	}
	return out
}

// Check reports whether path is blocked. reason starts with "protected:" or
// "whitelisted:" followed by the entry id or whitelist line, so engine can map
// it to plan.ReasonProtected or plan.ReasonWhitelisted.
func (c *Checker) Check(path string) (blocked bool, reason string) {
	return c.CheckSource(path, "")
}

// Reason says in words why the protected entry with this id (the part after
// "protected:" in what Check returns) is on the list: its reason in the
// user's language, or in English when there is none. It is empty for an id
// that is not an entry of the list.
func (c *Checker) Reason(id string) string {
	if text := c.reasons[id][c.lang]; text != "" {
		return text
	}
	return c.reasons[id]["en"]
}

// CheckSource is Check for an item that source produced (plan.Item.Source).
// A protected entry lets the item through only when one of its exceptions
// names source and matches the path exactly (see the package comment),
// checked for each spelling of the path. The whitelist never does, and an
// empty source is never allowed.
func (c *Checker) CheckSource(path, source string) (blocked bool, reason string) {
	if !filepath.IsAbs(path) {
		return true, "protected:relative-path"
	}
	var paths [][]string
	for _, s := range spellings(filepath.Clean(path)) {
		paths = append(paths, splitPath(s))
	}
	for _, list := range [][]match{c.protected, c.whitelist} {
		for _, m := range list {
			for _, p := range paths {
				if m.blocks(p) && !m.allows(source, p) {
					return true, m.reason
				}
			}
		}
	}
	return false, ""
}

// ReadWhitelist returns the whitelist lines (comments and blanks removed).
func ReadWhitelist(e env.Env) ([]string, error) {
	raw, err := readWhitelistFile(e)
	if err != nil {
		return nil, err
	}
	return whitelistLines(raw), nil
}

func readWhitelistFile(e env.Env) (string, error) {
	data, err := os.ReadFile(e.WhitelistFile())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

func whitelistLines(raw string) []string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines
}

// AddWhitelist appends an entry if it is not already there.
func AddWhitelist(e env.Env, entry string) error {
	entry = strings.TrimSpace(entry)
	if err := checkWhitelistEntry(entry); err != nil {
		return err
	}
	raw, err := readWhitelistFile(e)
	if err != nil {
		return err
	}
	if slices.Contains(whitelistLines(raw), entry) {
		return nil
	}
	if raw != "" && !strings.HasSuffix(raw, "\n") {
		raw += "\n"
	}
	if err := os.MkdirAll(e.StateDir, 0o700); err != nil {
		return err
	}
	return safefile.Write(e.StateDir, e.WhitelistFile(), []byte(raw+entry+"\n"), 0o600)
}

// RemoveWhitelist removes an entry. Removing a missing entry is not an error.
func RemoveWhitelist(e env.Env, entry string) error {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return errors.New("empty whitelist entry")
	}
	raw, err := readWhitelistFile(e)
	if err != nil {
		return err
	}
	var kept []string
	removed := false
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == entry && !strings.HasPrefix(trimmed, "#") {
			removed = true
			continue
		}
		kept = append(kept, line)
	}
	if !removed {
		return nil
	}
	return safefile.Write(e.StateDir, e.WhitelistFile(), []byte(strings.Join(kept, "\n")), 0o600)
}
