// Package rules defines cleanup rules and loads them from YAML.
//
// Path semantics (shared by every rule and by the protection list):
//   - "~" and "~/..." mean the user's home folder (env.Env.Expand).
//   - Each path segment may use filepath.Match wildcards: *, ?, [...].
//   - A final "/**" means "every direct child of this folder": the folder
//     itself is kept, its children become separate plan items.
//   - "**" anywhere else is not allowed.
package rules

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)
	versionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

type Risk string

const (
	RiskSafe     Risk = "safe"     // pure cache; selected by default
	RiskModerate Risk = "moderate" // rebuilt, but costs time or a re-download; selected by default
	RiskRisky    Risk = "risky"    // may lose something; never selected by default
)

type Action string

const (
	ActionQuarantine      Action = "quarantine"       // move into Sapu's quarantine (undoable)
	ActionOfficialCommand Action = "official_command" // run the owning tool's own cleanup command
)

// Categories group rules in the plan table.
const (
	CategorySystem   = "system"
	CategoryLogs     = "logs"
	CategoryBrowsers = "browsers"
	CategoryDev      = "dev"
	CategoryApps     = "apps"
	CategoryMedia    = "media"
)

// ReservedSources are the item sources (plan.Item.Source) of Sapu's
// dedicated commands: the same words as their plan kinds. A cleaner rule may
// not use one as its id, because its items would carry that source and get
// the protection exceptions (protect allow) meant for the command, e.g. a
// rule "backups" would move an iPhone backup during a normal clean. A protect
// allow may name only one of these. Purge items have the source
// "purge:<type>"; Reserved covers that prefix.
var ReservedSources = []string{
	"backups", "messages", "uninstall", "reset", "large", "trash", "installer",
	"dupes", "similar", "tidy", "packages", "optimize", "login-items", "analyze",
}

// Reserved reports whether id is the source of a dedicated command: one of
// ReservedSources, or "purge" alone or followed by a separator.
func Reserved(id string) bool {
	if slices.Contains(ReservedSources, id) {
		return true
	}
	rest, ok := strings.CutPrefix(id, "purge")
	return ok && (rest == "" || strings.ContainsAny(rest[:1], ".:_-"))
}

type Rule struct {
	ID          string            `yaml:"id" json:"id"`
	Category    string            `yaml:"category" json:"category"`
	App         string            `yaml:"app,omitempty" json:"app,omitempty"` // bundle id of the owning app
	Paths       []string          `yaml:"paths" json:"paths"`
	Exclude     []string          `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	Risk        Risk              `yaml:"risk" json:"risk"`
	Regenerates *bool             `yaml:"regenerates" json:"regenerates"` // pointer so a missing field is caught
	Explain     map[string]string `yaml:"explain" json:"explain"`         // "en" is required
	ProtectIf   []Condition       `yaml:"protect_if,omitempty" json:"protect_if,omitempty"`
	Action      Action            `yaml:"action" json:"action"`
	Command     []string          `yaml:"command,omitempty" json:"command,omitempty"` // official_command only
	MinMacOS    string            `yaml:"min_macos,omitempty" json:"min_macos,omitempty"`
	Fixture     string            `yaml:"fixture" json:"fixture"` // folder name under testdata/fixtures/rules/
}

// Condition skips an item while it holds. Exactly one field is set.
type Condition struct {
	ProcessRunning string `yaml:"process_running,omitempty" json:"process_running,omitempty"` // process name, e.g. "CapCut"
	ModifiedWithin string `yaml:"modified_within,omitempty" json:"modified_within,omitempty"` // age, e.g. "3d"
}

// ParseAge parses ages like "90m", "12h", "3d" and "2w".
func ParseAge(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid age %q", s)
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid age %q", s)
	}
	unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[s[len(s)-1]]
	if unit == 0 {
		return 0, fmt.Errorf("invalid age %q: use m, h, d or w", s)
	}
	return time.Duration(n) * unit, nil
}
