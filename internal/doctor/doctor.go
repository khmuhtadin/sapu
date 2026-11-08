// Package doctor checks the environment: Full Disk Access (blueprint U3),
// the state folder, volumes and the macOS version. Read-only, except that
// OpenFullDiskAccessSettings opens System Settings.
// Implemented in Wave 2 (W2-D).
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/env"
)

type Check struct {
	ID     string `json:"id"` // e.g. "full_disk_access"
	Title  string `json:"title"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"` // what to do when not OK
}

const (
	minMacOS     = 13
	minFreeBytes = 5_000_000_000

	// This URL opens the Full Disk Access pane on macOS 13 and later; System
	// Settings still accepts the identifier that System Preferences used.
	fullDiskAccessURL = "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"
)

// Files that macOS protects with TCC, relative to the home folder. Reading
// one of them fails with EPERM unless the terminal has Full Disk Access.
var tccProbes = []string{
	"Library/Application Support/com.apple.TCC/TCC.db",
	"Library/Safari/Bookmarks.plist",
	"Library/Mail",
}

// Tests replace these.
var (
	termProgram  = func() string { return os.Getenv("TERM_PROGRAM") }
	macOSVersion = func() (string, error) { return unix.Sysctl("kern.osproductversion") }
	lookPath     = exec.LookPath
)

// Run returns the checks in a fixed order. It never changes anything.
func Run(ctx context.Context, e env.Env) []Check {
	return []Check{
		checkFullDiskAccess(e),
		checkStateDir(e),
		checkSameVolume(e),
		checkMacOSVersion(),
		checkLsof(),
		checkFreeSpace(e),
	}
}

// HasFullDiskAccess reports whether this process can read TCC-protected files.
// A probe that does not exist is skipped. If no probe gives an answer, it
// reports false, because access could not be confirmed.
func HasFullDiskAccess(e env.Env) bool {
	for _, rel := range tccProbes {
		f, err := os.Open(filepath.Join(e.Home, rel))
		if err == nil {
			_ = f.Close() // read only: a failed close loses nothing
			return true
		}
		if errors.Is(err, fs.ErrPermission) {
			return false
		}
	}
	return false
}

// OpenFullDiskAccessSettings opens Privacy & Security > Full Disk Access.
func OpenFullDiskAccessSettings() error {
	return exec.Command("open", fullDiskAccessURL).Run()
}

func checkFullDiskAccess(e env.Env) Check {
	c := Check{ID: "full_disk_access", Title: "Full Disk Access"}
	if HasFullDiskAccess(e) {
		c.OK = true
		c.Detail = "This terminal can read protected folders."
		return c
	}
	owner, app := "your terminal app", "your terminal app"
	if name := terminalName(); name != "" {
		owner, app = "the terminal app ("+name+")", name
	}
	c.Detail = fmt.Sprintf("The permission belongs to %s, not to sapu. "+
		"Without it Sapu cannot measure Messages, iPhone backups and other protected folders.", owner)
	c.Fix = fmt.Sprintf("Open System Settings > Privacy & Security > Full Disk Access, turn on %s, then restart it. "+
		"`sapu doctor --open` opens that pane.", app)
	return c
}

// terminalName is the name of the terminal app, or "" when TERM_PROGRAM is not set.
func terminalName() string {
	switch name := termProgram(); name {
	case "":
		return ""
	case "Apple_Terminal":
		return "Terminal"
	case "ghostty":
		return "Ghostty"
	default:
		return name
	}
}

// nearestExisting returns path, or the closest parent folder that exists.
func nearestExisting(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

func checkStateDir(e env.Env) Check {
	c := Check{ID: "state_dir", Title: "Sapu state folder"}
	fi, err := os.Stat(e.StateDir)
	switch {
	case err == nil && !fi.IsDir():
		c.Detail = e.StateDir + " exists but is not a folder."
		c.Fix = "Move that file away, or set SAPU_HOME to another folder."
	case err == nil:
		if unix.Access(e.StateDir, unix.W_OK) != nil {
			c.Detail = e.StateDir + " is not writable."
			c.Fix = "Make it writable, or set SAPU_HOME to another folder."
			return c
		}
		c.OK = true
		c.Detail = e.StateDir + " exists and is writable."
	case errors.Is(err, fs.ErrNotExist):
		parent := filepath.Dir(e.StateDir)
		if unix.Access(parent, unix.W_OK) != nil {
			c.Detail = e.StateDir + " does not exist and " + parent + " is not writable, so it cannot be created."
			c.Fix = "Make " + parent + " writable, or set SAPU_HOME to another folder."
			return c
		}
		c.OK = true
		c.Detail = e.StateDir + " does not exist yet; Sapu will create it."
	default:
		c.Detail = "Cannot read " + e.StateDir + ": " + err.Error()
		c.Fix = "Set SAPU_HOME to another folder."
	}
	return c
}

func checkSameVolume(e env.Env) Check {
	c := Check{ID: "same_volume", Title: "Home and state folder on one volume"}
	home, err := deviceOf(nearestExisting(e.Home))
	if err != nil {
		c.Detail = "Cannot read the home folder: " + err.Error()
		return c
	}
	state, err := deviceOf(nearestExisting(e.StateDir))
	if err != nil {
		c.Detail = "Cannot read the state folder: " + err.Error()
		return c
	}
	if home != state {
		c.Detail = "The state folder is on a different volume than your home folder, so quarantine cannot be an instant rename."
		c.Fix = "Set SAPU_HOME to a folder inside your home folder."
		return c
	}
	c.OK = true
	c.Detail = "Quarantine is an instant rename."
	return c
}

func deviceOf(path string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil
}

func checkMacOSVersion() Check {
	c := Check{ID: "macos_version", Title: "macOS version"}
	v, err := macOSVersion()
	if err != nil {
		c.Detail = "Cannot read the macOS version: " + err.Error()
		return c
	}
	major, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	if err != nil {
		c.Detail = "Unexpected macOS version: " + v
		return c
	}
	c.Detail = "macOS " + v
	if major < minMacOS {
		c.Fix = fmt.Sprintf("Sapu needs macOS %d or newer.", minMacOS)
		return c
	}
	c.OK = true
	return c
}

func checkLsof() Check {
	c := Check{ID: "lsof", Title: "lsof"}
	path, err := lookPath("lsof")
	if err != nil {
		c.Detail = "lsof was not found on PATH. Sapu uses it to see which files are open."
		c.Fix = "Make sure /usr/sbin is on your PATH."
		return c
	}
	c.OK = true
	c.Detail = path
	return c
}

// FreeBytes returns the space available on the volume that holds path.
// Tests that must not depend on the real disk replace it.
var FreeBytes = func(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

func checkFreeSpace(e env.Env) Check {
	c := Check{ID: "free_space", Title: "Free space on the home volume"}
	free, err := FreeBytes(e.Home)
	if err != nil {
		c.Detail = "Cannot read free space: " + err.Error()
		return c
	}
	c.Detail = fmt.Sprintf("%.1f GB free", float64(free)/1e9)
	if free < minFreeBytes {
		c.Fix = "Free some space: run `sapu clean --plan` to see what Sapu can remove."
		return c
	}
	c.OK = true
	return c
}
