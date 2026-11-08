// Package plan defines the plan file: the exact list of items a run may touch.
// `sapu clean --plan` writes one, `sapu apply` reads it back, and apply never
// touches anything that is not in it (blueprint S2).
package plan

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/khmuhtadin/sapu/internal/rules"
)

// SchemaVersion of the plan file. Bump only for breaking changes.
const SchemaVersion = 1

// Plan kinds.
const (
	KindClean   = "clean"
	KindPurge   = "purge"
	KindAnalyze = "analyze"
	// M2 commands. The Source of their items is the same word, e.g. "backups",
	// which is also the source a protect allow names.
	KindUninstall  = "uninstall"
	KindLarge      = "large"
	KindBackups    = "backups"
	KindMessages   = "messages"
	KindTrash      = "trash"
	KindInstaller  = "installer"
	KindDupes      = "dupes"
	KindPackages   = "packages"
	KindOptimize   = "optimize"
	KindLoginItems = "login-items"
	// M3 commands.
	KindReset   = "reset"
	KindSimilar = "similar"
	KindTidy    = "tidy"
)

// Skip reasons.
const (
	ReasonProtected   = "protected"   // on the built-in protection list
	ReasonWhitelisted = "whitelisted" // on the user's whitelist
	ReasonInUse       = "in_use"      // open in a process, or a protect_if condition holds
	ReasonChanged     = "changed"     // identity differs from the plan
	ReasonGone        = "gone"        // no longer exists
	ReasonDuplicate   = "duplicate"   // already covered by another item
	ReasonUnreadable  = "unreadable"  // its contents could not be read to check for changes
	ReasonRequires    = "requires"    // the item named by Item.Requires was not moved in this run
)

// Identity pins one file or folder. Apply skips an item whose identity changed.
// For folders, Size is the folder entry's own size, not the recursive total.
type Identity struct {
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
	Mode    uint32 `json:"mode"`
	Size    int64  `json:"size"`
	MtimeNs int64  `json:"mtime_ns"`
}

// Item is one thing a run may touch.
//
// An item with Requires runs only when the item with that ID was moved in
// the same run. Apply moves the items without Requires first, then the
// others, and skips one whose required item was not moved (ReasonRequires).
// The required item must be an item to quarantine without Requires of its
// own; Apply refuses any other Requires. sapu uninstall points every
// leftover and the tccutil command at the app, so nothing of an app goes
// while the app itself stays.
type Item struct {
	ID          string            `json:"id"`     // set by engine.Plan: "0001", "0002", ...
	Source      string            `json:"source"` // rule id, "purge:<type>" or "analyze"
	Path        string            `json:"path"`   // absolute, cleaned
	Bytes       int64             `json:"bytes"`  // allocated bytes, recursive for folders
	Risk        rules.Risk        `json:"risk"`
	Regenerates bool              `json:"regenerates"`
	Explain     string            `json:"explain"` // already in the user's language
	Action      rules.Action      `json:"action"`
	Command     []string          `json:"command,omitempty"`    // official_command only
	ProtectIf   []rules.Condition `json:"protect_if,omitempty"` // re-checked right before the move
	KeepCopy    *KeepCopy         `json:"keep_copy,omitempty"`  // dupes: the copy that must stay
	CloneFrom   string            `json:"clone_from,omitempty"` // dupes --replace-with-clone: KeepCopy.Path, cloned into Path's place
	Requires    string            `json:"requires,omitempty"`   // ID of an item that must be moved in the same run first (uninstall: the app)
	Identity    Identity          `json:"identity"`
	Selected    bool              `json:"selected"`
}

// KeepCopy names the copy of a duplicate that stays. Apply moves the item
// only while that copy still exists with exactly this identity, so an edit
// to the kept copy after planning can never leave its only version in
// quarantine.
//
// An item with CloneFrom (always equal to KeepCopy.Path) is replaced instead
// of only moved: Apply moves it to quarantine, then puts an APFS clone of the
// kept copy at its path, with its mode and modification time. The file stays
// where it was with the same content, and the two copies share their data
// blocks until one of them is changed.
type KeepCopy struct {
	Path     string   `json:"path"`
	Identity Identity `json:"identity"`
}

type Skip struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Reason string `json:"reason"` // one of the Reason constants
	Detail string `json:"detail,omitempty"`
}

type Plan struct {
	SchemaVersion int       `json:"schema_version"`
	Kind          string    `json:"kind"`
	CreatedAt     time.Time `json:"created_at"`
	Home          string    `json:"home"` // apply refuses a plan made for another home
	Items         []Item    `json:"items"`
	Skipped       []Skip    `json:"skipped"`
}

// IdentityOf reads the identity of path without following symlinks.
func IdentityOf(path string) (Identity, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return Identity{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, fmt.Errorf("no stat data for %s", path)
	}
	return Identity{
		Dev:     uint64(st.Dev),
		Ino:     st.Ino,
		Mode:    uint32(st.Mode),
		Size:    st.Size,
		MtimeNs: st.Mtimespec.Nano(),
	}, nil
}

func Write(w io.Writer, p *Plan) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}

func Read(r io.Reader) (*Plan, error) {
	var p Plan
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	if p.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("plan schema_version %d is not supported (want %d)", p.SchemaVersion, SchemaVersion)
	}
	return &p, nil
}
