// Package config reads sapu.toml, the user's settings file (blueprint O4).
// Sapu only reads it; `sapu config init` is the one command that writes it,
// and only when there is no file yet.
//
// Where it lives: Env.ConfigFile, which is $SAPU_CONFIG when that is set,
// else ~/.config/sapu/sapu.toml, so it can live in a dotfiles repository. A
// missing file means every setting has its default. Only internal/env reads
// the variable.
//
// Parsing is strict: an unknown key, a value of the wrong type and a value
// out of range are errors that name the file and the line. There is no key
// that turns protection off or opens a protected place: the whitelist can
// only add paths Sapu must not touch.
//
//	profile = "standard"          # conservative | standard | aggressive
//	[quarantine]
//	max_age = "7d"                # at least 1d
//	max_size = "20GB"             # at least 1GB
//	[clean]
//	exclude = ["chrome.cache"]    # rule ids or categories never selected
//	[purge]
//	roots = ["~/Projects"]        # folders searched when none are given
//	older_than = "30d"
//	[whitelist]
//	paths = ["~/Projects/keep"]   # added to whitelist.txt
//
// Implemented in Wave 8 (W8-D).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/protect"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Profiles and the highest risk each one selects.
const (
	Conservative = "conservative" // safe
	Standard     = "standard"     // safe and moderate (the default)
	Aggressive   = "aggressive"   // risky too
)

// Limits on the quarantine settings.
const (
	MinMaxAge   = 24 * time.Hour
	MinMaxBytes = 1_000_000_000 // 1 GB, as Sapu prints sizes
)

// Config is the settings in effect.
type Config struct {
	File    string // where sapu.toml is read from
	FromEnv bool   // File came from $SAPU_CONFIG
	Found   bool   // the file exists; false means every setting is the default

	Profile        string
	MaxAge         time.Duration // [quarantine] max_age
	MaxBytes       int64         // [quarantine] max_size
	CleanExclude   []string      // [clean] exclude
	PurgeRoots     []string      // [purge] roots, as written ("~/x" not expanded)
	PurgeOlderThan time.Duration // [purge] older_than; 0 means no filter
	Whitelist      []string      // [whitelist] paths

	// Set holds the keys the file gives, like "profile" and
	// "quarantine.max_age". Every other setting is the default.
	Set map[string]bool
}

// Keys are the settings, in the order `sapu config show` lists them.
var Keys = []string{"profile", "quarantine.max_age", "quarantine.max_size", "clean.exclude",
	"purge.roots", "purge.older_than", "whitelist.paths"}

// Defaults returns the settings without a file.
func Defaults() Config {
	return Config{
		Profile:  Standard,
		MaxAge:   fsop.DefaultMaxAge,
		MaxBytes: fsop.DefaultMaxBytes,
		Set:      map[string]bool{},
	}
}

// ProfileRisk returns the highest risk a profile selects.
func ProfileRisk(profile string) (rules.Risk, bool) {
	switch profile {
	case Conservative:
		return rules.RiskSafe, true
	case Standard:
		return rules.RiskModerate, true
	case Aggressive:
		return rules.RiskRisky, true
	}
	return "", false
}

// Path returns where sapu.toml is read from, and whether $SAPU_CONFIG chose
// it. $SAPU_CONFIG may start with "~/" but must not be relative.
func Path(e env.Env) (path string, fromEnv bool, err error) {
	if !filepath.IsAbs(e.ConfigFile) {
		if e.ConfigFromEnv {
			return "", true, fmt.Errorf("SAPU_CONFIG must be an absolute path, not %q", e.ConfigFile)
		}
		return "", false, fmt.Errorf("the settings file %q is not an absolute path", e.ConfigFile)
	}
	return filepath.Clean(e.ConfigFile), e.ConfigFromEnv, nil
}

// Load reads sapu.toml for e. A missing file gives the defaults.
func Load(e env.Env) (Config, error) {
	path, fromEnv, err := Path(e)
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		c := Defaults()
		c.File, c.FromEnv = path, fromEnv
		return c, nil
	}
	if err != nil {
		return Config{}, err
	}
	c, err := Parse(path, data)
	if err != nil {
		return Config{}, err
	}
	c.FromEnv = fromEnv
	return c, nil
}

// file is what sapu.toml may hold. Every value type checks itself, so a bad
// value is a toml.ParseError that knows its line.
type file struct {
	Profile    profileValue `toml:"profile"`
	Quarantine struct {
		MaxAge  retentionValue `toml:"max_age"`
		MaxSize sizeValue      `toml:"max_size"`
	} `toml:"quarantine"`
	Clean struct {
		Exclude []nameValue `toml:"exclude"`
	} `toml:"clean"`
	Purge struct {
		Roots     []rootValue `toml:"roots"`
		OlderThan ageValue    `toml:"older_than"`
	} `toml:"purge"`
	Whitelist struct {
		Paths []whitelistValue `toml:"paths"`
	} `toml:"whitelist"`
}

// Parse reads the text of a settings file; name is used in errors.
func Parse(name string, data []byte) (Config, error) {
	var f file
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) {
			if pe.LastKey != "" {
				return Config{}, fmt.Errorf("%s: line %d: %s: %s", name, pe.Position.Line, pe.LastKey, pe.Message)
			}
			return Config{}, fmt.Errorf("%s: line %d: %s", name, pe.Position.Line, pe.Message)
		}
		return Config{}, fmt.Errorf("%s: %w", name, err) // toml says "line N" in these too
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		k := keys[0]
		if n := lineOf(string(data), k); n > 0 {
			return Config{}, fmt.Errorf("%s: line %d: unknown setting %q", name, n, k.String())
		}
		return Config{}, fmt.Errorf("%s: unknown setting %q", name, k.String())
	}

	c := Defaults()
	c.File, c.Found = name, true
	for _, k := range Keys {
		if md.IsDefined(strings.Split(k, ".")...) {
			c.Set[k] = true
		}
	}
	if c.Set["profile"] {
		c.Profile = string(f.Profile)
	}
	if c.Set["quarantine.max_age"] {
		c.MaxAge = time.Duration(f.Quarantine.MaxAge)
	}
	if c.Set["quarantine.max_size"] {
		c.MaxBytes = int64(f.Quarantine.MaxSize)
	}
	c.PurgeOlderThan = time.Duration(f.Purge.OlderThan)
	for _, v := range f.Clean.Exclude {
		c.CleanExclude = append(c.CleanExclude, string(v))
	}
	for _, v := range f.Purge.Roots {
		c.PurgeRoots = append(c.PurgeRoots, string(v))
	}
	for _, v := range f.Whitelist.Paths {
		c.Whitelist = append(c.Whitelist, string(v))
	}
	return c, nil
}

// lineOf finds the line that sets key or starts its table, or returns 0. It
// knows the usual forms ("[table]" and "key = ..." below it, or a dotted
// key); toml does not report where an unknown key is.
func lineOf(text string, key toml.Key) int {
	want := key.String()
	table := ""
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") {
			name, _, _ := strings.Cut(line, "]")
			table = strings.TrimSpace(strings.Trim(name, "[ "))
			if table == want {
				return i + 1
			}
			continue
		}
		k, _, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		full := strings.ReplaceAll(strings.TrimSpace(k), " ", "")
		if table != "" {
			full = table + "." + full
		}
		if full == want {
			return i + 1
		}
	}
	return 0
}

// CheckExclude reports a name in [clean] exclude that is neither the id nor
// the category of a rule in rs. A stale name would silently exclude nothing.
func (c Config) CheckExclude(rs []rules.Rule) error {
	for _, name := range c.CleanExclude {
		known := slices.ContainsFunc(rs, func(r rules.Rule) bool { return r.ID == name || r.Category == name })
		if !known {
			return fmt.Errorf("%s: [clean] exclude: no cleanup rule or category named %q", c.File, name)
		}
	}
	return nil
}

// Excluded reports whether [clean] exclude names the rule or its category.
func (c Config) Excluded(r rules.Rule) bool {
	return slices.Contains(c.CleanExclude, r.ID) || slices.Contains(c.CleanExclude, r.Category)
}

type profileValue string

func (v *profileValue) UnmarshalText(b []byte) error {
	if _, ok := ProfileRisk(string(b)); !ok {
		return fmt.Errorf("must be %q, %q or %q, not %q", Conservative, Standard, Aggressive, b)
	}
	*v = profileValue(b)
	return nil
}

type retentionValue time.Duration

func (v *retentionValue) UnmarshalText(b []byte) error {
	d, err := ParseAge(string(b))
	if err != nil {
		return err
	}
	if d < MinMaxAge {
		return fmt.Errorf("must be at least 1d, not %q: undo needs time", b)
	}
	*v = retentionValue(d)
	return nil
}

type ageValue time.Duration

func (v *ageValue) UnmarshalText(b []byte) error {
	d, err := ParseAge(string(b))
	*v = ageValue(d)
	return err
}

type sizeValue int64

func (v *sizeValue) UnmarshalText(b []byte) error {
	n, err := ParseSize(string(b))
	if err != nil {
		return err
	}
	if n < MinMaxBytes {
		return fmt.Errorf("must be at least 1GB, not %q", b)
	}
	*v = sizeValue(n)
	return nil
}

type nameValue string

func (v *nameValue) UnmarshalText(b []byte) error {
	s := string(b)
	if strings.TrimSpace(s) == "" || s != strings.TrimSpace(s) {
		return fmt.Errorf("%q is not a rule id or category", s)
	}
	*v = nameValue(s)
	return nil
}

type rootValue string

func (v *rootValue) UnmarshalText(b []byte) error {
	s := string(b)
	if s != "~" && !strings.HasPrefix(s, "~/") && !strings.HasPrefix(s, "/") {
		return fmt.Errorf("%q: use an absolute path or one starting with ~/", s)
	}
	*v = rootValue(s)
	return nil
}

type whitelistValue string

func (v *whitelistValue) UnmarshalText(b []byte) error {
	if err := protect.CheckWhitelistEntry(string(b)); err != nil {
		return err
	}
	*v = whitelistValue(b)
	return nil
}

// ParseAge reads an age like "90m", "12h", "7d" or "2w".
func ParseAge(s string) (time.Duration, error) {
	n, unit, err := splitNumber(s, map[string]int64{
		"m": int64(time.Minute), "h": int64(time.Hour), "d": int64(24 * time.Hour), "w": int64(7 * 24 * time.Hour),
	})
	if err != nil {
		return 0, fmt.Errorf("%q is not an age like 7d: %w", s, err)
	}
	return time.Duration(n * unit), nil
}

// ParseSize reads a size like "500MB", "20GB" or "1TB" (1 GB is
// 1,000,000,000 bytes, as Sapu prints sizes). Units may be lower case.
func ParseSize(s string) (int64, error) {
	n, unit, err := splitNumber(strings.ToUpper(s), map[string]int64{
		"MB": 1_000_000, "GB": 1_000_000_000, "TB": 1_000_000_000_000,
	})
	if err != nil {
		return 0, fmt.Errorf("%q is not a size like 20GB: %w", s, err)
	}
	return n * unit, nil
}

// splitNumber splits "20GB" into 20 and the value of "GB" in units, and
// refuses a product that does not fit in an int64.
func splitNumber(s string, units map[string]int64) (n, unit int64, err error) {
	i := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if i <= 0 {
		return 0, 0, errors.New("it must start with a whole number")
	}
	unit, ok := units[strings.TrimSpace(s[i:])]
	if !ok {
		var names []string
		for name := range units {
			names = append(names, name)
		}
		slices.Sort(names)
		return 0, 0, fmt.Errorf("the unit must be one of %s", strings.Join(names, ", "))
	}
	n, err = strconv.ParseInt(s[:i], 10, 64)
	if err != nil || n > math.MaxInt64/unit {
		return 0, 0, errors.New("the number is too large")
	}
	return n, unit, nil
}

// FormatAge writes d the way the settings file does: "7d", "36h" or "90m".
func FormatAge(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}

// AgeWords says d in words, for texts: "7 days", "1 day" or "36 hours".
func AgeWords(d time.Duration) string {
	n, unit := int64(d/time.Minute), "minute"
	switch {
	case d%(24*time.Hour) == 0:
		n, unit = int64(d/(24*time.Hour)), "day"
	case d%time.Hour == 0:
		n, unit = int64(d/time.Hour), "hour"
	}
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s", n, unit)
}

// FormatSize writes n the way the settings file does: "20GB" or "1500MB".
func FormatSize(n int64) string {
	switch {
	case n%1_000_000_000_000 == 0:
		return fmt.Sprintf("%dTB", n/1_000_000_000_000)
	case n%1_000_000_000 == 0:
		return fmt.Sprintf("%dGB", n/1_000_000_000)
	}
	return fmt.Sprintf("%dMB", n/1_000_000)
}
