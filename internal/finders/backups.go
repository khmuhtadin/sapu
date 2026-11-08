package finders

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// BackupsDir is where Finder keeps iPhone and iPad backups, inside the home folder.
const BackupsDir = "Library/Application Support/MobileSync/Backup"

// PlistValue returns the value of key in a plist file, as plutil prints it
// ("raw"): dates as 2026-05-01T10:00:00Z. A key that is not there is an
// error. Tests replace it. (`plutil -convert json` cannot be used: it fails
// on the dates that Info.plist holds.)
var PlistValue = func(ctx context.Context, file, key string) (string, error) {
	out, err := exec.CommandContext(ctx, "plutil", "-extract", key, "raw", "-o", "-", file).Output()
	return strings.TrimSpace(string(out)), err
}

// Backup is one backup folder with what its Info.plist says.
type Backup struct {
	Item       plan.Item
	Device     string    // "Name's iPhone"; empty when Info.plist does not say
	Model      string    // "iPhone14,2"
	LastBackup time.Time // zero when unknown
	Newest     bool      // the newest backup of this device; may be its only copy
}

// Backups lists the iPhone and iPad backups, newest first. Items are risky:
// a backup cannot be made again once the phone has changed. A backup folder
// without a readable Info.plist is listed without device details.
func Backups(ctx context.Context, e env.Env) ([]Backup, error) {
	dir := filepath.Join(e.Home, BackupsDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, readError(e.Home, dir, err)
	}

	var backups []Backup
	var keys []string // the device each backup belongs to
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		b := Backup{}
		var udid string
		plist := filepath.Join(path, "Info.plist")
		f, err := os.Open(plist)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// an interrupted backup: listed without details
		case err != nil:
			return nil, readError(e.Home, plist, err)
		default:
			_ = f.Close() // read only: a failed close loses nothing
			b.Device = plistText(ctx, plist, "Device Name")
			b.Model = plistText(ctx, plist, "Product Type")
			udid = plistText(ctx, plist, "Unique Identifier")
			if when, err := time.Parse(time.RFC3339, plistText(ctx, plist, "Last Backup Date")); err == nil {
				b.LastBackup = when
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item, ok, err := newItem(ctx, "backups", path, rules.RiskRisky, false, "")
		if err != nil {
			return nil, readError(e.Home, path, err)
		}
		if !ok {
			continue
		}
		b.Item = item
		backups = append(backups, b)
		keys = append(keys, cmp.Or(udid, b.Device, entry.Name()))
	}

	// The newest backup of each device. Equal dates (or none at all) count
	// as newest for every backup, so the warning is never left out by mistake.
	newest := map[string]time.Time{}
	for i, b := range backups {
		if b.LastBackup.After(newest[keys[i]]) {
			newest[keys[i]] = b.LastBackup
		}
	}
	for i := range backups {
		b := &backups[i]
		b.Newest = b.LastBackup.Equal(newest[keys[i]])
		b.Item.Explain = backupExplain(*b)
	}
	slices.SortFunc(backups, func(a, b Backup) int {
		return cmp.Or(b.LastBackup.Compare(a.LastBackup), cmp.Compare(a.Item.Path, b.Item.Path))
	})
	return backups, nil
}

// plistText reads one key; a missing key is empty text.
func plistText(ctx context.Context, file, key string) string {
	v, err := PlistValue(ctx, file, key)
	if err != nil {
		return ""
	}
	return v
}

func backupExplain(b Backup) string {
	what := "Backup of an iPhone or iPad"
	if b.Device != "" {
		what = fmt.Sprintf("Backup of %q", b.Device)
		if b.Model != "" {
			what += " (" + b.Model + ")"
		}
	}
	text := what + "."
	if !b.LastBackup.IsZero() {
		text += " Last backup " + day(b.LastBackup) + "."
	}
	if b.Newest {
		text += " This is the newest backup of that device: it may be the only copy of that iPhone."
	} else {
		text += " A newer backup of that device exists."
	}
	return text
}
