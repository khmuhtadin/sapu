package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/khmuhtadin/sapu/internal/env"
)

// Example is what `sapu config init` writes: every setting, commented out,
// with its default. Removing the "# " in front of a setting line gives a
// valid file (a test checks this).
const Example = `# Sapu settings (sapu.toml). Sapu only reads this file.
# Every setting below is commented out, so Sapu uses its default.
# Remove the "# " in front of a setting to change it.
# Check the result with: sapu config show

# Which items sapu clean and sapu purge select at the start:
#   "conservative"  safe items only
#   "standard"      safe and moderate items (the default)
#   "aggressive"    risky items too; the confirmation still lists everything
# --risk and --profile on the command line win over this.
# profile = "standard"

[quarantine]
# How long cleaned items stay in quarantine, where sapu undo can bring them
# back. At least 1d. Units: m, h, d, w.
# max_age = "7d"
# How much the quarantine may hold before its oldest runs are deleted.
# At least 1GB. Units: MB, GB, TB.
# max_size = "20GB"

[clean]
# Cleanup rules or categories sapu clean never selects (the scheduled clean
# leaves them alone too). sapu clean titles each group with its rule id.
# exclude = ["xcode.derived-data", "browsers"]

[purge]
# Folders sapu purge searches when you give it none.
# roots = ["~/Projects"]
# Only projects that nobody touched for this long.
# older_than = "30d"

[whitelist]
# Paths Sapu must never touch, on top of the ones in sapu whitelist list.
# Paths start with ~/ or /, and may use * globs.
# paths = ["~/Projects/keep-this"]
`

// Init writes Example to the settings file of e and returns its path. It
// refuses when anything (a file, a folder, a symlink) is already there.
func Init(e env.Env) (string, error) {
	path, _, err := Path(e)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("%s already exists; Sapu does not change it", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(Example); err != nil {
		return "", errors.Join(err, f.Close())
	}
	return path, f.Close()
}
